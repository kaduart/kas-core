package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Piloto "renovação de pacote" (Frente 3), testes R1–R5. Clínica, famílias e
// pacotes fictícios. A execução só termina como "renovado" quando o CRM
// confirma o identificador do PACOTE NOVO criado para aquele pedido; promessa
// ou mensagem enviada nunca fecham. Precisa de KAS_TEST_DATABASE_URL.

// renewalSample is the same definition the Studio opens (studio/samples).
const renewalSample = "../../studio/samples/renovacao-pacote.json"

func (e *env) setupRenewal() {
	e.t.Helper()
	raw, err := os.ReadFile(renewalSample)
	if err != nil {
		e.t.Fatalf("read sample: %v", err)
	}
	e.publish(tenantClinica, string(raw))
	e.roles(tenantClinica, "secretaria", "ator-secretaria-1")
	e.roles(tenantClinica, "supervisao", "ator-supervisao-1")
}

// startRenewal starts the process for an ending package, as the CRM does: the
// business_ref is the ending package id and the start_key is unique per package.
func (e *env) startRenewal(pkg string) int64 {
	e.t.Helper()
	id, _, err := e.eng.StartInstance(context.Background(), StartRequest{TenantID: tenantClinica, ServiceID: crmA,
		ProcessKey: "renovacao-pacote", BusinessRef: pkg, StartKey: "renovacao-" + pkg, ActorID: "crm-rotina-pacotes",
		OccurredAt: e.clock.Now()})
	if err != nil {
		e.t.Fatalf("start renewal: %v", err)
	}
	return id
}

func (e *env) answerFamily(id int64, outcome string) {
	e.t.Helper()
	task := e.openTask(tenantClinica, id)
	expect(e.t, strings.Join(task.Roles, ","), "secretaria", "task roles")
	if _, err := e.submit(tenantClinica, task.ID, outcome, "ator-secretaria-1", "secretaria", e.clock.Now()); err != nil {
		e.t.Fatalf("submit %s: %v", outcome, err)
	}
}

// historyLines renders the history the way a reviewer reads it.
func historyLines(v InstanceView) []string {
	var out []string
	for _, h := range v.History {
		var d map[string]any
		_ = json.Unmarshal(h.Detail, &d)
		var parts []string
		for _, k := range []string{"cause", "kind", "outcome", "command_key", "query", "status", "result_ref", "result"} {
			if val, ok := d[k]; ok && val != "" {
				s := fmt.Sprint(val)
				if k == "command_key" && len(s) > 10 {
					s = s[:10] + "…"
				}
				parts = append(parts, k+"="+s)
			}
		}
		out = append(out, fmt.Sprintf("%s %-22s %-20s %s", h.RecordedAt.In(loc).Format("01-02 15:04"), h.Kind, h.Step, strings.Join(parts, " ")))
	}
	return out
}

func kindsInOrder(v InstanceView) []string {
	out := make([]string, 0, len(v.History))
	for _, h := range v.History {
		out = append(out, h.Kind)
	}
	return out
}

// containsInOrder reports whether want appears in got as a subsequence.
func containsInOrder(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

func historyDetail(t *testing.T, v InstanceView, kind string) map[string]any {
	t.Helper()
	for _, h := range v.History {
		if h.Kind == kind {
			var d map[string]any
			if err := json.Unmarshal(h.Detail, &d); err != nil {
				t.Fatal(err)
			}
			return d
		}
	}
	t.Fatalf("history has no %s", kind)
	return nil
}

// R1: caminho feliz. A secretaria registra "renovou", o CRM cria o pacote novo
// e confirma com o id dele; só então a execução termina como "renovado".
func TestR1RenewalHappyPathRecordsNewPackageID(t *testing.T) {
	e := newEnv(t)
	e.setupRenewal()
	id := e.startRenewal("pacote-encerrando-101")
	e.answerFamily(id, "renovou")
	v := e.inst(tenantClinica, id)
	expect(t, v.Status, "running", "status after the family answered (a promise does not close it)")
	expect(t, v.CurrentStep, "criar_pacote_novo", "step")
	key := v.Commands[0].CommandKey

	e.crm.drain()
	v = e.inst(tenantClinica, id)
	expect(t, v.Status, "completed", "status")
	expect(t, *v.Result, "renovado", "result")
	expect(t, e.crm.packageCount(), 1, "packages created")
	pkg := e.crm.packageFor(key)
	expect(t, v.Commands[0].ResultRef, pkg, "command result_ref")
	expect(t, v.Commands[0].Status, "confirmed", "command status")
	d := historyDetail(t, v, "command_result")
	expect(t, d["result_ref"], pkg, "history result_ref")
	if !containsInOrder(kindsInOrder(v), []string{"started", "task_resolved", "command_requested", "command_result", "completed"}) {
		t.Fatalf("history order: %v", kindsInOrder(v))
	}
	for _, l := range historyLines(v) {
		t.Log(l)
	}
}

// R2: o CRM cria o pacote, a confirmação se perde, a KAS consulta o MESMO
// pedido (mesma command_key) e o CRM responde com o MESMO pacote. Resultado:
// exatamente um pacote e um histórico que explica a conclusão.
func TestR2ConfirmationLostKASQueriesSameRequestOnePackage(t *testing.T) {
	e := newEnv(t)
	e.setupRenewal()
	ref := "pacote-encerrando-202"
	e.crm.dropFirst[ref] = true
	id := e.startRenewal(ref)
	e.answerFamily(id, "renovou")
	key := e.inst(tenantClinica, id).Commands[0].CommandKey

	e.crm.drain() // efeito aplicado e recibo gravado; a confirmação se perde
	expect(t, e.crm.effectsFor(key), 1, "effects after lost confirmation")
	expect(t, e.crm.packageCount(), 1, "packages after lost confirmation")
	v := e.inst(tenantClinica, id)
	expect(t, v.Status, "running", "status without confirmation")
	expect(t, v.CurrentStep, "criar_pacote_novo", "step without confirmation")
	expect(t, v.Commands[0].Status, "requested", "command without confirmation")

	e.clock.Add(61 * time.Minute) // reply_timeout PT1H sem resposta
	expect(t, e.timers(), 1, "reply timers executed")
	expect(t, outboxCount(e, EventCommandStatusQuery, key), 1, "status queries with the same key")
	expect(t, outboxCount(e, EventCommandRequested, key), 1, "requests (never re-issued)")

	e.crm.drain() // o CRM acha o recibo da mesma chave e responde com o mesmo pacote
	v = e.inst(tenantClinica, id)
	expect(t, v.Status, "completed", "status")
	expect(t, *v.Result, "renovado", "result")
	expect(t, len(v.Commands), 1, "commands (same key, no new generation)")
	expect(t, v.Commands[0].CommandKey, key, "command key")
	expect(t, e.crm.effectsFor(key), 1, "effects in the CRM")
	expect(t, e.crm.packageCount(), 1, "packages in the CRM")
	pkg := e.crm.packageFor(key)
	expect(t, v.Commands[0].ResultRef, pkg, "command result_ref")

	want := []string{"started", "task_resolved", "command_requested", "timer_executed", "command_status_query", "command_result", "completed"}
	if !containsInOrder(kindsInOrder(v), want) {
		t.Fatalf("history order: %v, want subsequence %v", kindsInOrder(v), want)
	}
	d := historyDetail(t, v, "command_result")
	expect(t, d["result_ref"], pkg, "history result_ref")
	expect(t, d["command_key"], key, "history command_key")
	expect(t, d["status_queries"], 1, "status queries before the result")
	timer := historyDetail(t, v, "timer_executed")
	expect(t, timer["kind"], "command_reply", "timer kind (no reply)")
	t.Logf("R2: pacotes no CRM simulado = %d; pacote novo = %s; command_key = %s", e.crm.packageCount(), pkg, key)
	for _, l := range historyLines(v) {
		t.Log(l)
	}
}

// R3: reenvio idêntico é inofensivo (200 duplicate); outro result_ref para a
// mesma chave é conflito (409); confirmação sem result_ref é recusada porque a
// etapa exige o identificador; result_ref inválido é 400.
func TestR3ResultRefIdempotencyAndValidation(t *testing.T) {
	e := newEnv(t)
	e.setupRenewal()
	ref := "pacote-encerrando-303"
	e.crm.mute[ref] = true // o CRM aplica o efeito mas não responde; o teste responde por ele
	id := e.startRenewal(ref)
	e.answerFamily(id, "renovou")
	key := e.inst(tenantClinica, id).Commands[0].CommandKey
	e.crm.drain()
	pkg := e.crm.packageFor(key)
	ctx := context.Background()
	result := func(status, resultRef string) (bool, error) {
		return e.eng.RecordCommandResult(ctx, CommandResult{TenantID: tenantClinica, ServiceID: crmA, CommandKey: key,
			Status: status, ResultRef: resultRef, OccurredAt: e.clock.Now()})
	}

	_, err := result("confirmed", "")
	expectErr(t, err, ErrResultRefRequired)
	expect(t, e.inst(tenantClinica, id).Commands[0].Status, "requested", "command after confirmation without ref")
	for _, bad := range []string{"pacote novo", "fulano@exemplo.com", strings.Repeat("x", 129), "-pacote"} {
		_, err = result("confirmed", bad)
		expectErr(t, err, ErrInvalidInput)
	}
	_, err = result("rejected", pkg) // result_ref só acompanha "confirmed"
	expectErr(t, err, ErrInvalidInput)

	dup, err := result("confirmed", pkg)
	if err != nil || dup {
		t.Fatalf("first result: dup=%v err=%v", dup, err)
	}
	dup, err = result("confirmed", pkg)
	if err != nil || !dup {
		t.Fatalf("identical resend: dup=%v err=%v", dup, err)
	}
	_, err = result("confirmed", "pacote-novo-999")
	expectErr(t, err, ErrConflict)
	_, err = result("confirmed", "")
	expectErr(t, err, ErrConflict)
	v := e.inst(tenantClinica, id)
	expect(t, *v.Result, "renovado", "result")
	expect(t, v.Commands[0].ResultRef, pkg, "stored result_ref unchanged")
	expect(t, e.historyKinds(tenantClinica, id)["command_result"], 1, "command_result entries")
}

// R4: sem resposta da secretaria em 2 dias úteis (calendário do tenant, com
// corte às 18:00), a tarefa vai para revisão da supervisão.
func TestR4DeadlineGoesToSupervisaoReview(t *testing.T) {
	e := newEnv(t)
	e.setupRenewal()
	id := e.startRenewal("pacote-encerrando-404")
	task := e.openTask(tenantClinica, id)
	// Início sexta 13/11 10:00 → 2 dias úteis = terça 17/11 às 18:00 (São Paulo).
	expect(t, task.DueAt.In(loc).Format("2006-01-02 15:04"), "2026-11-17 18:00", "due_at")

	e.clock.Set(task.CutoffAt.Add(29 * time.Second))
	expect(t, e.timers(), 0, "timers before settle")
	e.clock.Set(task.CutoffAt.Add(31 * time.Second))
	expect(t, e.timers(), 1, "deadline timer")
	v := e.inst(tenantClinica, id)
	expect(t, v.Status, "running", "status")
	expect(t, v.CurrentStep, "revisar_supervisao", "step")
	review := e.openTask(tenantClinica, id)
	expect(t, strings.Join(review.Roles, ","), "supervisao", "review roles")
	_, err := e.submit(tenantClinica, review.ID, "vai_sair", "ator-secretaria-1", "secretaria", e.clock.Now())
	expectErr(t, err, ErrRoleNotAllowed)
	if _, err := e.submit(tenantClinica, review.ID, "pausa_combinada", "ator-supervisao-1", "supervisao", e.clock.Now()); err != nil {
		t.Fatalf("supervisao: %v", err)
	}
	v = e.inst(tenantClinica, id)
	expect(t, *v.Result, "pausado", "result after review")
	expect(t, len(v.Commands), 0, "commands")
}

// R5: "vai_sair" e "pausa_combinada" terminam sem comando ao CRM.
func TestR5EndsWithoutCommand(t *testing.T) {
	e := newEnv(t)
	e.setupRenewal()
	for outcome, result := range map[string]string{"vai_sair": "nao_renovou", "pausa_combinada": "pausado"} {
		id := e.startRenewal("pacote-encerrando-5-" + outcome)
		e.answerFamily(id, outcome)
		v := e.inst(tenantClinica, id)
		expect(t, v.Status, "completed", outcome+" status")
		expect(t, *v.Result, result, outcome+" result")
		expect(t, len(v.Commands), 0, outcome+" commands")
	}
	expect(t, e.count(`SELECT count(*) FROM outbox_events WHERE event_type = $1`, EventCommandRequested), 0, "command events")
	e.crm.drain()
	expect(t, e.crm.packageCount(), 0, "packages")
}

// R6: sem resposta alguma do CRM, depois das 3 consultas com a mesma chave, o
// comando fica "unknown" e vai para a supervisão, que não consegue encerrar
// enquanto o comando estiver incerto (409 command_unknown).
func TestR6NoReplyGoesToSupervisaoAndNeverClosesAsRenewed(t *testing.T) {
	e := newEnv(t)
	e.setupRenewal()
	ref := "pacote-encerrando-606"
	e.crm.mute[ref] = true
	id := e.startRenewal(ref)
	e.answerFamily(id, "renovou")
	key := e.inst(tenantClinica, id).Commands[0].CommandKey
	for i := 0; i < 4; i++ { // pedido + 3 consultas, sem resposta
		e.crm.drain()
		e.clock.Add(61 * time.Minute)
		e.timers()
	}
	v := e.inst(tenantClinica, id)
	expect(t, v.CurrentStep, "revisar_supervisao", "step after no reply")
	expect(t, v.Commands[0].Status, "unknown", "command status")
	expect(t, outboxCount(e, EventCommandStatusQuery, key), 3, "status queries")
	review := e.openTask(tenantClinica, id)
	_, err := e.submit(tenantClinica, review.ID, "vai_sair", "ator-supervisao-1", "supervisao", e.clock.Now())
	expectErr(t, err, ErrCommandUnknown)
	expect(t, e.inst(tenantClinica, id).Status, "running", "never closed while the command is uncertain")
	expect(t, e.crm.packageCount(), 1, "packages")
}

// The sample parses, keeps require_result_ref in its canonical JSON, and the
// flag is refused outside an external_command step. No database needed.
func TestRenewalSampleDefinition(t *testing.T) {
	raw, err := os.ReadFile(renewalSample)
	if err != nil {
		t.Fatal(err)
	}
	d, canonical, _, err := ParseDefinition(raw)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if !d.Steps["criar_pacote_novo"].RequireResultRef || !strings.Contains(string(canonical), `"require_result_ref":true`) {
		t.Fatalf("require_result_ref lost: %s", canonical)
	}
	bad := strings.Replace(string(raw), `"roles": ["supervisao"],`, `"roles": ["supervisao"], "require_result_ref": true,`, 1)
	if _, _, _, err := ParseDefinition([]byte(bad)); err == nil || !strings.Contains(err.Error(), "require_result_ref applies only to external_command") {
		t.Fatalf("require_result_ref on a human_task: %v", err)
	}
}
