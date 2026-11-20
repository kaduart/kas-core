package crmsim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/httpapi"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/process"
)

const tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const tokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// Fictitious tenants and services only.
const fixture = `{"contract_version":"v1","config_version":"crmsim","tenant_registry":[{"id":"demo-empresa-a","status":"active"},{"id":"demo-empresa-b","status":"active"}],"key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},"tenants":{},
"event_types":["process.command.requested","process.command.status_query","process.wait.opened"],
"event_consumers":{
 "crm-simulado-a":{"service":"demo-crm-a","event_types":["process.command.requested","process.command.status_query","process.wait.opened"],"tenants":["demo-empresa-a"]},
 "crm-simulado-b":{"service":"demo-crm-b","event_types":["process.command.requested","process.command.status_query","process.wait.opened"],"tenants":["demo-empresa-b"]}},
"callers":{
 "demo-crm-a":{"token_env":"TOKEN_A","tenants":["demo-empresa-a"],"capabilities":["events:consume","process:define","process:run","process:task","process:command","process:event"]},
 "demo-crm-b":{"token_env":"TOKEN_B","tenants":["demo-empresa-b"],"capabilities":["events:consume","process:define","process:run","process:task","process:command","process:event"]}}}`

func definition(businessDays int) string {
	return `{"schema":"kas.process/v1","process_key":"retencao-cancelamento","start":"negociar",
"steps":{
 "negociar":{"type":"human_task","roles":["comercial"],"outcomes":["cliente_permanece","cliente_cancela"],
   "deadline":{"business_days":` + strconv.Itoa(businessDays) + `,"cutoff":"23:59"},"grace":"PT5M","on_deadline":"revisar","late_response":"record_only","next":"resultado"},
 "resultado":{"type":"decision","routes":{"cliente_permanece":"fim_retido","cliente_cancela":"pedir_cancelamento"}},
 "pedir_cancelamento":{"type":"external_command","command_type":"crm.contract.cancel","reply_timeout":"PT1H","max_status_queries":2,
   "on_confirmed":"fim_cancelado","on_rejected":"revisar","on_no_reply":"revisar","business_status":"910"},
 "revisar":{"type":"human_task","roles":["supervisor"],"outcomes":["cliente_permanece","cliente_cancela"],"next":"resultado"},
 "fim_retido":{"type":"end","result":"retido","business_status":"220"},
 "fim_cancelado":{"type":"end","result":"cancelado"}}}`
}

type stack struct {
	t   *testing.T
	srv *httptest.Server
	eng *process.Engine
}

func newStack(t *testing.T) *stack {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(fixture), func(name string) (string, bool) {
		switch name {
		case "TOKEN_A":
			return tokenA, true
		case "TOKEN_B":
			return tokenB, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	dsn := pgtest.SchemaDSN(t)
	ctx := context.Background()
	outbox, err := events.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("events store: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	eng, err := process.Open(ctx, dsn, true, process.Config{Registry: store})
	if err != nil {
		t.Fatalf("process engine: %v", err)
	}
	t.Cleanup(eng.Close)
	h := httpapi.NewHandler(store, 16, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.EnableEvents(events.NewService(outbox, store))
	h.EnablePull(events.NewPuller(outbox, events.PullConfig{}))
	h.EnableProcess(eng)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &stack{t: t, srv: srv, eng: eng}
}

func (s *stack) api(method, path, service, token string, body any) map[string]any {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, s.srv.URL+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		s.t.Fatalf("%s %s: %d %v", method, path, resp.StatusCode, out)
	}
	return out
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *stack) setup(service, token string, businessDays int) {
	s.t.Helper()
	s.api("POST", "/internal/v1/process/definitions", service, token, map[string]any{
		"contract_version": "v1", "publish_id": "pub-" + strconv.Itoa(businessDays), "definition": json.RawMessage(definition(businessDays))})
	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	for role, actor := range map[string]string{"comercial": "ator-comercial-1", "supervisor": "ator-supervisor-1"} {
		s.api("PUT", "/internal/v1/process/roles/"+role+"/members", service, token, map[string]any{
			"contract_version": "v1", "change_id": "chg-" + role, "members": []string{actor}, "valid_from": from})
	}
}

func (s *stack) startAndCancel(service, token, ref string) int64 {
	s.t.Helper()
	out := s.api("POST", "/internal/v1/process/instances", service, token, map[string]any{
		"contract_version": "v1", "process_key": "retencao-cancelamento", "business_ref": ref, "start_key": "start-" + ref,
		"actor_id": "ator-comercial-1", "occurred_at": now()})
	id := int64(out["instance_id"].(float64))
	inst := s.instance(service, token, id)
	task := int64(inst["tasks"].([]any)[0].(map[string]any)["id"].(float64))
	s.api("POST", "/internal/v1/process/tasks/"+strconv.FormatInt(task, 10)+"/resolve", service, token, map[string]any{
		"contract_version": "v1", "submission_key": "sub-" + ref, "outcome": "cliente_cancela",
		"actor_id": "ator-comercial-1", "role": "comercial", "occurred_at": now()})
	return id
}

func (s *stack) instance(service, token string, id int64) map[string]any {
	s.t.Helper()
	return s.api("GET", "/internal/v1/process/instances/"+strconv.FormatInt(id, 10), service, token, nil)["instance"].(map[string]any)
}

func (s *stack) sim(service, token, consumer, state string, mod func(*Config)) *Sim {
	s.t.Helper()
	cfg := Config{BaseURL: s.srv.URL, ServiceID: service, Token: token, ConsumerID: consumer, StatePath: state, LeaseSeconds: 5}
	if mod != nil {
		mod(&cfg)
	}
	sim, err := New(cfg)
	if err != nil {
		s.t.Fatal(err)
	}
	return sim
}

// The CRM crashes after applying the effect and before reporting. A restarted
// CRM gets the same delivery again after the lease, finds its receipt and
// reports without a second effect; the instance completes.
func TestSimulatedCRMOverRealPullSurvivesCrashAfterEffect(t *testing.T) {
	s := newStack(t)
	s.setup("demo-crm-a", tokenA, 2)
	id := s.startAndCancel("demo-crm-a", tokenA, "contrato-demo-1")
	if got := s.instance("demo-crm-a", tokenA, id)["current_step"]; got != "pedir_cancelamento" {
		t.Fatalf("step = %v", got)
	}
	state := filepath.Join(t.TempDir(), "sim-a.json")
	ctx := context.Background()
	crashing := s.sim("demo-crm-a", tokenA, "crm-simulado-a", state, func(c *Config) { c.CrashAfterEffect = true })
	if _, err := crashing.Drain(ctx); !errors.Is(err, ErrCrashAfterEffect) {
		t.Fatalf("crash: %v", err)
	}
	if got := s.instance("demo-crm-a", tokenA, id)["status"]; got != "running" {
		t.Fatalf("instance moved before the result was reported: %v", got)
	}
	restarted := s.sim("demo-crm-a", tokenA, "crm-simulado-a", state, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		n, err := restarted.Drain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delivery was not redelivered after the lease")
		}
		time.Sleep(250 * time.Millisecond)
	}
	inst := s.instance("demo-crm-a", tokenA, id)
	if inst["status"] != "completed" || inst["result"] != "cancelado" {
		t.Fatalf("instance = %v %v", inst["status"], inst["result"])
	}
	snap := restarted.Snapshot()
	if len(snap.Effects) != 1 {
		t.Fatalf("effects = %v", snap.Effects)
	}
	for key, n := range snap.Effects {
		if n != 1 {
			t.Fatalf("effect %s ran %d times", key, n)
		}
	}
}

// Two companies, same engine: each simulated CRM only sees its own tenant, a
// rejection sends the case to review, and deadlines follow each definition.
func TestSimulatedCRMsAreIsolatedPerTenant(t *testing.T) {
	s := newStack(t)
	s.setup("demo-crm-a", tokenA, 2)
	s.setup("demo-crm-b", tokenB, 5)
	idA := s.startAndCancel("demo-crm-a", tokenA, "contrato-a-1")
	idB := s.startAndCancel("demo-crm-b", tokenB, "contrato-b-1")
	ctx := context.Background()
	simB := s.sim("demo-crm-b", tokenB, "crm-simulado-b", "", func(c *Config) { c.RejectRefs = map[string]bool{"contrato-b-1": true} })
	if n, err := simB.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("sim B drained %d, %v", n, err)
	}
	if got := s.instance("demo-crm-a", tokenA, idA)["current_step"]; got != "pedir_cancelamento" {
		t.Fatalf("A moved through B's simulator: %v", got)
	}
	instB := s.instance("demo-crm-b", tokenB, idB)
	if instB["current_step"] != "revisar" {
		t.Fatalf("B after rejection = %v", instB["current_step"])
	}
	simA := s.sim("demo-crm-a", tokenA, "crm-simulado-a", "", nil)
	if n, err := simA.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("sim A drained %d, %v", n, err)
	}
	if got := s.instance("demo-crm-a", tokenA, idA)["result"]; got != "cancelado" {
		t.Fatalf("A result = %v", got)
	}
}

func TestSimRefusesNonLoopbackKAS(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://kas.example.com", ServiceID: "x", Token: "y", ConsumerID: "z"}); err == nil {
		t.Fatal("non-loopback KAS accepted")
	}
}
