package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/process"
)

// Fictitious tenants and services only.
const processHandlerFixture = `{"contract_version":"v1","config_version":"fixture-process","tenant_registry":[{"id":"demo-empresa-a","status":"active"},{"id":"demo-empresa-b","status":"active"}],"key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},"tenants":{},
"event_types":["process.command.requested","process.command.status_query","process.wait.opened"],
"event_consumers":{"crm-simulado-a":{"service":"demo-crm-a","event_types":["process.command.requested","process.command.status_query","process.wait.opened"],"tenants":["demo-empresa-a"]}},
"callers":{
 "demo-crm-a":{"token_env":"TOKEN_A","tenants":["demo-empresa-a"],"capabilities":["events:consume","process:define","process:run","process:task","process:command","process:event"]},
 "demo-crm-b":{"token_env":"TOKEN_B","tenants":["demo-empresa-b"],"capabilities":["process:define","process:run","process:task","process:command","process:event"]},
 "demo-leitor":{"token_env":"TOKEN_A","tenants":["demo-empresa-a"],"capabilities":["process:run"]}}}`

const processHTTPDef = `{
  "schema": "kas.process/v1",
  "process_key": "retencao-cancelamento",
  "start": "negociar",
  "steps": {
    "negociar": {"type": "human_task", "roles": ["comercial"], "outcomes": ["cliente_permanece", "cliente_cancela"],
      "deadline": {"business_days": 2, "cutoff": "23:59"}, "grace": "PT5M", "on_deadline": "revisar", "late_response": "record_only", "next": "resultado"},
    "resultado": {"type": "decision", "routes": {"cliente_permanece": "fim_retido", "cliente_cancela": "fim_cancelado"}},
    "revisar": {"type": "human_task", "roles": ["supervisor"], "outcomes": ["cliente_permanece", "cliente_cancela"], "next": "resultado"},
    "fim_retido": {"type": "end", "result": "retido", "business_status": "220"},
    "fim_cancelado": {"type": "end", "result": "cancelado"}
  }
}`

type processHTTP struct {
	t       *testing.T
	handler http.Handler
}

func newProcessHTTP(t *testing.T, enable bool) *processHTTP {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(processHandlerFixture), func(name string) (string, bool) {
		switch name {
		case "TOKEN_A":
			return handlerTokenA, true
		case "TOKEN_B":
			return handlerTokenB, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if enable {
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
		h.EnableProcess(eng)
	}
	return &processHTTP{t: t, handler: h}
}

func (p *processHTTP) do(method, path, service, token, body string) (int, map[string]any) {
	p.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func jsonString(v string) string { b, _ := json.Marshal(v); return string(b) }

func TestProcessRoutesUnavailableWhenNotEnabled(t *testing.T) {
	p := newProcessHTTP(t, false)
	code, body := p.do(http.MethodGet, "/internal/v1/process/tasks", "demo-crm-a", handlerTokenA, "")
	if code != http.StatusServiceUnavailable || errCode(body) != "process_unavailable" {
		t.Fatalf("got %d %v", code, body)
	}
}

// T17 over HTTP: the tenant comes from the authenticated service, never from
// the body alone; another tenant's data is invisible; capabilities gate routes.
func TestProcessHTTPFlowAndTenantIsolation(t *testing.T) {
	p := newProcessHTTP(t, true)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	code, body := p.do(http.MethodPost, "/internal/v1/process/definitions:validate", "demo-crm-a", handlerTokenA,
		`{"contract_version":"v1","publish_id":"val-1","definition":{"schema":"kas.process/v1","process_key":"x","start":"nada","steps":{}}}`)
	if code != http.StatusOK || body["valid"] != false {
		t.Fatalf("validate invalid: %d %v", code, body)
	}

	pub := `{"contract_version":"v1","publish_id":"pub-1","definition":` + processHTTPDef + `}`
	if code, body = p.do(http.MethodPost, "/internal/v1/process/definitions", "demo-crm-a", handlerTokenA, pub); code != http.StatusCreated {
		t.Fatalf("publish: %d %v", code, body)
	}
	if code, body = p.do(http.MethodPost, "/internal/v1/process/definitions", "demo-crm-a", handlerTokenA, pub); code != http.StatusOK || body["duplicate"] != true {
		t.Fatalf("publish resend: %d %v", code, body)
	}
	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	for role, actor := range map[string]string{"comercial": "ator-comercial-1", "supervisor": "ator-supervisor-1"} {
		roles := `{"contract_version":"v1","change_id":"chg-` + role + `","members":["` + actor + `"],"valid_from":"` + from + `"}`
		if code, body = p.do(http.MethodPut, "/internal/v1/process/roles/"+role+"/members", "demo-crm-a", handlerTokenA, roles); code != http.StatusCreated {
			t.Fatalf("roles %s: %d %v", role, code, body)
		}
	}

	start := `{"contract_version":"v1","process_key":"retencao-cancelamento","business_ref":"contrato-demo-1","start_key":"start-1","actor_id":"ator-comercial-1","occurred_at":"` + now + `"}`
	code, body = p.do(http.MethodPost, "/internal/v1/process/instances", "demo-crm-a", handlerTokenA, start)
	if code != http.StatusCreated {
		t.Fatalf("start: %d %v", code, body)
	}
	instanceID := int64(body["instance_id"].(float64))
	second := strings.Replace(strings.Replace(start, "start-1", "start-2", 1), now, now, 1)
	if code, body = p.do(http.MethodPost, "/internal/v1/process/instances", "demo-crm-a", handlerTokenA, second); code != http.StatusConflict || errCode(body) != "instance_active" {
		t.Fatalf("second running instance: %d %v", code, body)
	}

	code, body = p.do(http.MethodGet, "/internal/v1/process/tasks?role=comercial&status=open", "demo-crm-a", handlerTokenA, "")
	tasks, _ := body["tasks"].([]any)
	if code != http.StatusOK || len(tasks) != 1 {
		t.Fatalf("tasks: %d %v", code, body)
	}
	taskID := int64(tasks[0].(map[string]any)["id"].(float64))
	taskPath := "/internal/v1/process/tasks/" + jsonNumber(taskID) + "/resolve"
	instancePath := "/internal/v1/process/instances/" + jsonNumber(instanceID)

	// Tenant B cannot name tenant A, cannot see A's instance and cannot resolve A's task.
	if code, body = p.do(http.MethodPost, "/internal/v1/process/instances", "demo-crm-b", handlerTokenB,
		strings.Replace(start, `"contract_version":"v1"`, `"contract_version":"v1","tenant_id":"demo-empresa-a"`, 1)); code != http.StatusForbidden || errCode(body) != "tenant_forbidden" {
		t.Fatalf("cross-tenant start: %d %v", code, body)
	}
	if code, body = p.do(http.MethodGet, instancePath, "demo-crm-b", handlerTokenB, ""); code != http.StatusNotFound {
		t.Fatalf("cross-tenant read: %d %v", code, body)
	}
	resolve := `{"contract_version":"v1","submission_key":"sub-1","outcome":"cliente_permanece","actor_id":"ator-comercial-1","role":"comercial","occurred_at":` + jsonString(time.Now().UTC().Format(time.RFC3339Nano)) + `}`
	if code, body = p.do(http.MethodPost, taskPath, "demo-crm-b", handlerTokenB, resolve); code != http.StatusNotFound {
		t.Fatalf("cross-tenant resolve: %d %v", code, body)
	}
	// A read-only caller cannot resolve tasks.
	if code, body = p.do(http.MethodPost, taskPath, "demo-leitor", handlerTokenA, resolve); code != http.StatusForbidden || errCode(body) != "capability_forbidden" {
		t.Fatalf("capability: %d %v", code, body)
	}
	// The wrong role is refused; the right one closes the instance.
	wrongRole := strings.Replace(strings.Replace(resolve, `"role":"comercial"`, `"role":"supervisor"`, 1), "sub-1", "sub-0", 1)
	if code, body = p.do(http.MethodPost, taskPath, "demo-crm-a", handlerTokenA, wrongRole); code != http.StatusForbidden || errCode(body) != "role_not_allowed" {
		t.Fatalf("wrong role: %d %v", code, body)
	}
	if code, body = p.do(http.MethodPost, taskPath, "demo-crm-a", handlerTokenA, resolve); code != http.StatusOK || body["status"] != "applied" {
		t.Fatalf("resolve: %d %v", code, body)
	}
	if code, body = p.do(http.MethodPost, taskPath, "demo-crm-a", handlerTokenA, resolve); code != http.StatusOK || body["duplicate"] != true {
		t.Fatalf("resolve resend: %d %v", code, body)
	}
	code, body = p.do(http.MethodGet, instancePath, "demo-crm-a", handlerTokenA, "")
	inst, _ := body["instance"].(map[string]any)
	if code != http.StatusOK || inst["status"] != "completed" || inst["result"] != "retido" {
		t.Fatalf("instance: %d %v", code, body)
	}

	// Skewed occurred_at is refused.
	skewed := strings.Replace(start, now, time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339Nano), 1)
	skewed = strings.Replace(strings.Replace(skewed, "start-1", "start-3", 1), "contrato-demo-1", "contrato-demo-2", 1)
	if code, body = p.do(http.MethodPost, "/internal/v1/process/instances", "demo-crm-a", handlerTokenA, skewed); code != http.StatusBadRequest || errCode(body) != "clock_skew" {
		t.Fatalf("skew: %d %v", code, body)
	}
}

func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }
