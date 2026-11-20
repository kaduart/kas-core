package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Piloto de renovação: result_ref no POST /commands/{key}/result. Tenant e
// pacotes fictícios.
const processRenewalHTTPDef = `{
  "schema": "kas.process/v1",
  "process_key": "renovacao-pacote",
  "start": "falar_com_familia",
  "steps": {
    "falar_com_familia": {"type": "human_task", "roles": ["secretaria"], "outcomes": ["renovou", "vai_sair"], "next": "decidir"},
    "decidir": {"type": "decision", "routes": {"renovou": "criar_pacote_novo", "vai_sair": "fim_nao_renovou"}},
    "criar_pacote_novo": {"type": "external_command", "command_type": "crm.package.create_renewal", "reply_timeout": "PT1H",
      "max_status_queries": 1, "require_result_ref": true, "on_confirmed": "fim_renovado", "on_rejected": "fim_nao_renovou", "on_no_reply": "fim_nao_renovou"},
    "fim_renovado": {"type": "end", "result": "renovado"},
    "fim_nao_renovou": {"type": "end", "result": "nao_renovou"}
  }
}`

func TestProcessHTTPCommandResultRef(t *testing.T) {
	p := newProcessHTTP(t, true)
	stamp := func() string { return jsonString(time.Now().UTC().Format(time.RFC3339Nano)) }
	if code, body := p.do(http.MethodPost, "/internal/v1/process/definitions", "demo-crm-a", handlerTokenA,
		`{"contract_version":"v1","publish_id":"pub-renov","definition":`+processRenewalHTTPDef+`}`); code != http.StatusCreated {
		t.Fatalf("publish: %d %v", code, body)
	}
	from := jsonString(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	if code, body := p.do(http.MethodPut, "/internal/v1/process/roles/secretaria/members", "demo-crm-a", handlerTokenA,
		`{"contract_version":"v1","change_id":"chg-sec","members":["ator-secretaria-1"],"valid_from":`+from+`}`); code != http.StatusCreated {
		t.Fatalf("roles: %d %v", code, body)
	}
	code, body := p.do(http.MethodPost, "/internal/v1/process/instances", "demo-crm-a", handlerTokenA,
		`{"contract_version":"v1","process_key":"renovacao-pacote","business_ref":"pacote-encerrando-1","start_key":"renovacao-pacote-encerrando-1","actor_id":"crm-rotina","occurred_at":`+stamp()+`}`)
	if code != http.StatusCreated {
		t.Fatalf("start: %d %v", code, body)
	}
	instancePath := "/internal/v1/process/instances/" + jsonNumber(int64(body["instance_id"].(float64)))
	_, body = p.do(http.MethodGet, instancePath, "demo-crm-a", handlerTokenA, "")
	tasks := body["instance"].(map[string]any)["tasks"].([]any)
	taskPath := "/internal/v1/process/tasks/" + jsonNumber(int64(tasks[0].(map[string]any)["id"].(float64))) + "/resolve"
	if code, body = p.do(http.MethodPost, taskPath, "demo-crm-a", handlerTokenA,
		`{"contract_version":"v1","submission_key":"sub-renov","outcome":"renovou","actor_id":"ator-secretaria-1","role":"secretaria","occurred_at":`+stamp()+`}`); code != http.StatusOK {
		t.Fatalf("resolve: %d %v", code, body)
	}
	_, body = p.do(http.MethodGet, instancePath, "demo-crm-a", handlerTokenA, "")
	key := body["instance"].(map[string]any)["commands"].([]any)[0].(map[string]any)["command_key"].(string)
	resultPath := "/internal/v1/process/commands/" + key + "/result"
	result := func(extra string) (int, map[string]any) {
		return p.do(http.MethodPost, resultPath, "demo-crm-a", handlerTokenA, `{"contract_version":"v1","status":"confirmed"`+extra+`,"occurred_at":`+stamp()+`}`)
	}

	if code, body = result(""); code != http.StatusUnprocessableEntity || errCode(body) != "result_ref_required" {
		t.Fatalf("confirmed without result_ref: %d %v", code, body)
	}
	if code, body = result(`,"result_ref":"pacote novo"`); code != http.StatusBadRequest {
		t.Fatalf("invalid result_ref: %d %v", code, body)
	}
	if code, body = result(`,"result_ref":"` + strings.Repeat("p", 129) + `"`); code != http.StatusBadRequest {
		t.Fatalf("long result_ref: %d %v", code, body)
	}
	if code, body = result(`,"result_ref":"pacote-novo-1"`); code != http.StatusOK || body["duplicate"] != false || body["result_ref"] != "pacote-novo-1" {
		t.Fatalf("first result: %d %v", code, body)
	}
	if code, body = result(`,"result_ref":"pacote-novo-1"`); code != http.StatusOK || body["duplicate"] != true {
		t.Fatalf("identical resend: %d %v", code, body)
	}
	if code, body = result(`,"result_ref":"pacote-novo-2"`); code != http.StatusConflict || errCode(body) != "idempotency_conflict" {
		t.Fatalf("different result_ref: %d %v", code, body)
	}
	_, body = p.do(http.MethodGet, instancePath, "demo-crm-a", handlerTokenA, "")
	inst := body["instance"].(map[string]any)
	cmd := inst["commands"].([]any)[0].(map[string]any)
	if inst["result"] != "renovado" || cmd["result_ref"] != "pacote-novo-1" || cmd["status"] != "confirmed" {
		t.Fatalf("instance after result: %v", inst)
	}
}
