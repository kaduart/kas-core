package crmsim

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// Piloto de renovação (fictício): reply_timeout curto para a consulta de status
// acontecer em tempo real no teste.
const renewalDefinition = `{"schema":"kas.process/v1","process_key":"renovacao-pacote","start":"falar_com_familia",
"steps":{
 "falar_com_familia":{"type":"human_task","roles":["secretaria"],"outcomes":["renovou","vai_sair"],"next":"decidir"},
 "decidir":{"type":"decision","routes":{"renovou":"criar_pacote_novo","vai_sair":"fim_nao_renovou"}},
 "criar_pacote_novo":{"type":"external_command","command_type":"crm.package.create_renewal","reply_timeout":"PT1S","max_status_queries":3,
   "require_result_ref":true,"on_confirmed":"fim_renovado","on_rejected":"revisar_supervisao","on_no_reply":"revisar_supervisao"},
 "revisar_supervisao":{"type":"human_task","roles":["supervisao"],"outcomes":["renovou","vai_sair"],"next":"decidir"},
 "fim_renovado":{"type":"end","result":"renovado"},
 "fim_nao_renovou":{"type":"end","result":"nao_renovou"}}}`

// The simulated CRM creates the package, loses the first confirmation, KAS
// queries the same command_key and the CRM answers with the same package id:
// one package, instance "renovado", result_ref stored.
func TestSimulatedCRMDropsFirstConfirmationAndAnswersQueryWithSamePackage(t *testing.T) {
	s := newStack(t)
	svc, tok := "demo-crm-a", tokenA
	s.api("POST", "/internal/v1/process/definitions", svc, tok, map[string]any{
		"contract_version": "v1", "publish_id": "pub-renov", "definition": json.RawMessage(renewalDefinition)})
	s.api("PUT", "/internal/v1/process/roles/secretaria/members", svc, tok, map[string]any{
		"contract_version": "v1", "change_id": "chg-sec", "members": []string{"ator-secretaria-1"},
		"valid_from": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)})
	ref := "pacote-encerrando-1"
	out := s.api("POST", "/internal/v1/process/instances", svc, tok, map[string]any{
		"contract_version": "v1", "process_key": "renovacao-pacote", "business_ref": ref, "start_key": "renovacao-" + ref,
		"actor_id": "crm-rotina", "occurred_at": now()})
	id := int64(out["instance_id"].(float64))
	task := int64(s.instance(svc, tok, id)["tasks"].([]any)[0].(map[string]any)["id"].(float64))
	s.api("POST", "/internal/v1/process/tasks/"+strconv.FormatInt(task, 10)+"/resolve", svc, tok, map[string]any{
		"contract_version": "v1", "submission_key": "sub-renov", "outcome": "renovou",
		"actor_id": "ator-secretaria-1", "role": "secretaria", "occurred_at": now()})

	ctx := context.Background()
	state := t.TempDir() + "/sim.json"
	sim := s.sim(svc, tok, "crm-simulado-a", state, func(c *Config) { c.DropFirstResultRefs = map[string]bool{"pacote-encerrando-*": true} })
	if n, err := sim.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("first drain: %d %v", n, err)
	}
	snap := sim.Snapshot()
	if len(snap.Packages) != 1 || len(snap.Dropped) != 1 {
		t.Fatalf("after lost confirmation: packages=%v dropped=%v", snap.Packages, snap.Dropped)
	}
	if got := s.instance(svc, tok, id)["status"]; got != "running" {
		t.Fatalf("instance moved without confirmation: %v", got)
	}

	time.Sleep(1100 * time.Millisecond) // reply_timeout PT1S
	if n, err := s.eng.RunDueTimers(ctx, 10); err != nil || n != 1 {
		t.Fatalf("reply timer: %d %v", n, err)
	}
	// A restarted simulator (same state file) still answers from its receipt.
	restarted := s.sim(svc, tok, "crm-simulado-a", state, func(c *Config) { c.DropFirstResultRefs = map[string]bool{ref: true} })
	if n, err := restarted.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("status query drain: %d %v", n, err)
	}
	inst := s.instance(svc, tok, id)
	cmd := inst["commands"].([]any)[0].(map[string]any)
	snap = restarted.Snapshot()
	var pkg string
	for key, p := range snap.Packages {
		if key != cmd["command_key"] {
			t.Fatalf("package for another key: %s", key)
		}
		pkg = p
	}
	if inst["result"] != "renovado" || cmd["result_ref"] != pkg || pkg == "" || len(snap.Packages) != 1 || snap.Effects[cmd["command_key"].(string)] != 1 {
		t.Fatalf("instance=%v command=%v packages=%v effects=%v", inst["result"], cmd, snap.Packages, snap.Effects)
	}
}

func TestMatchRefPrefix(t *testing.T) {
	set := map[string]bool{"pacote-a": true, "pacote-demo-perde-*": true}
	for ref, want := range map[string]bool{"pacote-a": true, "pacote-ab": false, "pacote-demo-perde-1": true, "pacote-demo-perde-": true, "pacote-demo": false} {
		if got := matchRef(set, ref); got != want {
			t.Fatalf("matchRef(%q) = %v, want %v", ref, got, want)
		}
	}
}
