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

	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/process"
)

// Fictional tenants: fono-demo has no modules block (process keeps working as
// today); escola-demo contracts attribution + reports; loja-demo has process paused.
const modulesHandlerFixture = `{"contract_version":"v1","config_version":"fixture-modules",
"tenant_registry":[{"id":"fono-demo","status":"active"},{"id":"escola-demo","status":"active"},{"id":"loja-demo","status":"active"}],
"key_schema":{"k.v":{"type":"integer"}},
"modules":{
 "escola-demo":{"attribution":{"status":"active"},"reports":{"status":"active"}},
 "loja-demo":{"process":{"status":"paused"}}},
"callers":{
 "fono-crm":{"token_env":"TOKEN_A","tenants":["fono-demo"],"capabilities":["process:define","audit:read"]},
 "escola-sis":{"token_env":"TOKEN_B","tenants":["escola-demo"],"capabilities":["attribution:import","attribution:read","audit:read"]},
 "loja-sis":{"token_env":"TOKEN_A","tenants":["loja-demo"],"capabilities":["process:define","audit:read"]},
 "multi":{"token_env":"TOKEN_B","tenants":["escola-demo","loja-demo"],"capabilities":["audit:read"]}}}`

func modulesHandler(t *testing.T, withProcess bool) *Handler {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(modulesHandlerFixture), func(name string) (string, bool) {
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
	jsonl, err := audit.OpenJSONL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jsonl.Close() })
	h := NewHandlerWithAudit(store, audit.NewService(jsonl), 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if withProcess {
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
	return h
}

func callAs(h http.Handler, method, path, service, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type modulesBody struct {
	TenantID string `json:"tenant_id"`
	Modules  []struct {
		Key                string             `json:"key"`
		Contract           string             `json:"contract"`
		DependsOn          []string           `json:"depends_on"`
		Integration        *moduleIntegration `json:"integration"`
		CallerCapabilities []string           `json:"caller_capabilities"`
	} `json:"modules"`
}

func TestModulesView(t *testing.T) {
	h := modulesHandler(t, false)
	h.EnableModuleReadiness(configstore.ModuleAttribution, func(_ context.Context, tenant string) ([]string, error) {
		if tenant != "escola-demo" {
			t.Errorf("readiness asked for %s", tenant)
		}
		return []string{"receipts", "contacts"}, nil
	})
	rec := callAs(h, http.MethodGet, "/internal/v1/modules", "escola-sis", handlerTokenB, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var body modulesBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for i, m := range body.Modules {
		got[m.Key] = i
	}
	attr := body.Modules[got["attribution"]]
	if body.TenantID != "escola-demo" || attr.Contract != "active" || attr.Integration == nil || attr.Integration.Ready ||
		strings.Join(attr.Integration.Missing, ",") != "contacts,receipts" ||
		strings.Join(attr.CallerCapabilities, ",") != "attribution:import,attribution:read" {
		t.Fatalf("attribution view: %+v", attr)
	}
	rep := body.Modules[got["reports"]]
	if rep.Contract != "active" || strings.Join(rep.DependsOn, ",") != "attribution" || len(rep.CallerCapabilities) != 0 || rep.Integration != nil {
		t.Fatalf("reports view: %+v", rep)
	}
	if body.Modules[got["crm"]].Contract != "planned" || body.Modules[got["process"]].Contract != "absent" {
		t.Fatalf("crm/process view: %s", rec.Body.String())
	}

	// Legacy tenant: process active, attribution absent and no readiness asked.
	rec = callAs(h, http.MethodGet, "/internal/v1/modules", "fono-crm", handlerTokenA, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `{"key":"process","contract":"active","caller_capabilities":["process:define"]}`) {
		t.Fatalf("legacy view: %d %s", rec.Code, rec.Body.String())
	}

	// Isolation and parameter rules.
	for name, tc := range map[string]struct {
		service, token, query, code string
		status                      int
	}{
		"other tenant":       {"escola-sis", handlerTokenB, "?tenant_id=fono-demo", "tenant_forbidden", 403},
		"multi without pick": {"multi", handlerTokenB, "", "tenant_required", 400},
		"bad token":          {"escola-sis", handlerTokenA, "", "unauthorized", 401},
		"unknown parameter":  {"escola-sis", handlerTokenB, "?x=1", "invalid_request", 400},
	} {
		rec := callAs(h, http.MethodGet, "/internal/v1/modules"+tc.query, tc.service, tc.token, "")
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec = callAs(h, http.MethodGet, "/internal/v1/modules?tenant_id=loja-demo", "multi", handlerTokenB, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `{"key":"process","contract":"paused"}`) {
		t.Fatalf("multi picks loja: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callAs(h, http.MethodPost, "/internal/v1/modules", "escola-sis", handlerTokenB, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST must be refused, got %d", rec.Code)
	}
}

func TestPausedModuleBlocksRoutesAndIsAudited(t *testing.T) {
	h := modulesHandler(t, true)
	validate := `{"contract_version":"v1","definition":` + processHTTPDef + `}`

	// Legacy tenant without a modules block: process works as before.
	if rec := callAs(h, http.MethodPost, "/internal/v1/process/definitions:validate", "fono-crm", handlerTokenA, validate); rec.Code != http.StatusOK {
		t.Fatalf("legacy tenant: %d %s", rec.Code, rec.Body.String())
	}
	// Tenant with process paused: 403 module_paused.
	rec := callAs(h, http.MethodPost, "/internal/v1/process/definitions:validate", "loja-sis", handlerTokenA, validate)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"module_paused"`) {
		t.Fatalf("paused tenant: %d %s", rec.Code, rec.Body.String())
	}
	// The refusal is in the tenant's audit trail, identifiers only.
	rec = callAs(h, http.MethodGet, "/internal/v1/audit", "loja-sis", handlerTokenA, "")
	var page struct{ Items []audit.Event }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("audit read: %d %s", rec.Code, rec.Body.String())
	}
	ev := page.Items[0]
	if ev.Action != "module.access_denied" || ev.Result != "denied" || ev.Resource != "module:process" ||
		ev.EntityID != "process:define" || ev.TenantID != "loja-demo" || ev.Actor != "system.kas-core" {
		t.Fatalf("audit record: %+v", ev)
	}
	// Other tenants see nothing of it.
	rec = callAs(h, http.MethodGet, "/internal/v1/audit", "fono-crm", handlerTokenA, "")
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("fono audit must be empty: %s", rec.Body.String())
	}
}
