package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kas-core-pilot/internal/attribution"
	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/pgtest"
)

// Fictional tenants. fono-demo and escola-demo contract attribution; loja-demo
// has it paused. Each service only sees its own tenant.
const attributionHandlerFixture = `{"contract_version":"v1","config_version":"fixture-attribution",
"tenant_registry":[{"id":"fono-demo","status":"active"},{"id":"escola-demo","status":"active"},{"id":"loja-demo","status":"active"}],
"key_schema":{"k.v":{"type":"integer"}},
"modules":{
 "fono-demo":{"attribution":{"status":"active"}},
 "escola-demo":{"attribution":{"status":"active"}},
 "loja-demo":{"attribution":{"status":"paused"}}},
"callers":{
 "fono-crm":{"token_env":"TOKEN_A","tenants":["fono-demo"],"capabilities":["attribution:import","attribution:read","audit:read"]},
 "escola-sis":{"token_env":"TOKEN_B","tenants":["escola-demo"],"capabilities":["attribution:import","attribution:read"]},
 "escola-bi":{"token_env":"TOKEN_A","tenants":["escola-demo"],"capabilities":["attribution:read"]},
 "loja-sis":{"token_env":"TOKEN_A","tenants":["loja-demo"],"capabilities":["attribution:import","attribution:read"]}}}`

func attributionHandler(t *testing.T) *Handler {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(attributionHandlerFixture), func(name string) (string, bool) {
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
	fp, err := attribution.NewFingerprinter([]byte("fictional-test-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	as, err := attribution.Open(context.Background(), pgtest.SchemaDSN(t), true, fp)
	if err != nil {
		t.Fatalf("attribution store: %v", err)
	}
	t.Cleanup(as.Close)
	h.EnableAttribution(as)
	return h
}

// batchBody builds a request body from testdata/attribution/<dir>/<kind>.csv.
func batchBody(t *testing.T, tenant, dir, kind, batchID string) string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "attribution", dir, kind+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	items, err := attribution.ParseCSV(kind, f)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"contract_version": "v1", "tenant_id": tenant, "batch_id": batchID,
		"actor": "user.fin-07", "items": items})
	return string(raw)
}

func importAll(t *testing.T, h http.Handler, service, token, tenant, dir, prefix string) {
	t.Helper()
	for _, kind := range []string{"customers", "contacts", "receipts", "refunds"} {
		rec := callAs(h, http.MethodPost, "/internal/v1/attribution/"+kind+":batch", service, token,
			batchBody(t, tenant, dir, kind, prefix+"-"+kind))
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("%s import: %d %s", kind, rec.Code, rec.Body.String())
		}
	}
}

type revenueBody struct {
	TenantID  string           `json:"tenant_id"`
	NetTotal  int64            `json:"total_liquido_centavos"`
	ByLevel   map[string]int64 `json:"por_nivel_centavos"`
	Invariant struct {
		Closes bool `json:"fecha"`
	} `json:"invariante"`
	Rules map[string]string `json:"regras"`
}

func readRevenue(t *testing.T, h http.Handler, service, token, query string) revenueBody {
	t.Helper()
	rec := callAs(h, http.MethodGet, "/internal/v1/attribution/revenue"+query, service, token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revenue: %d %s", rec.Code, rec.Body.String())
	}
	var body revenueBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e errorResponse
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

func TestAttributionImportAndRevenue(t *testing.T) {
	h := attributionHandler(t)
	importAll(t, h, "fono-crm", handlerTokenA, "fono-demo", "amostra-fono", "imp-1")

	// Conciliation: the generic inputs reproduce the CRM sample exactly.
	r := readRevenue(t, h, "fono-crm", handlerTokenA, "")
	if r.TenantID != "fono-demo" || r.NetTotal != 129000 || !r.Invariant.Closes ||
		r.ByLevel["comprovada"]+r.ByLevel["provavel"]+r.ByLevel["sem_vinculo"] != 129000 || r.Rules["modelo"] == "" {
		t.Fatalf("report: %+v", r)
	}

	// Replay of a batch: 200, the original answer, duplicate=true.
	rec := callAs(h, http.MethodPost, "/internal/v1/attribution/receipts:batch", "fono-crm", handlerTokenA,
		batchBody(t, "fono-demo", "amostra-fono", "receipts", "imp-1-receipts"))
	var replay attributionBatchResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &replay)
	if rec.Code != http.StatusOK || !replay.Duplicate || replay.Created != 10 {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	// Reimport with new batch ids: same totals (deduplication by external_id).
	importAll(t, h, "fono-crm", handlerTokenA, "fono-demo", "amostra-fono", "imp-2")
	if again := readRevenue(t, h, "fono-crm", handlerTokenA, ""); again.NetTotal != r.NetTotal || again.ByLevel["comprovada"] != r.ByLevel["comprovada"] {
		t.Fatalf("reimport changed the report: %+v", again)
	}
	// Same batch id, other content: 409.
	rec = callAs(h, http.MethodPost, "/internal/v1/attribution/receipts:batch", "fono-crm", handlerTokenA,
		`{"contract_version":"v1","batch_id":"imp-1-receipts","actor":"user.fin-07","items":[{"external_id":"x","customer_ref":"c","amount_cents":1,"received_at":"2026-11-10T15:00:00Z"}]}`)
	if rec.Code != http.StatusConflict || errorCode(t, rec.Body.Bytes()) != "batch_conflict" {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body.String())
	}
	// Invalid batch (no items), unknown field, wrong contract version.
	for body, code := range map[string]string{
		`{"contract_version":"v1","batch_id":"b","actor":"user.a","items":[]}`:                    "invalid_batch",
		`{"contract_version":"v1","batch_id":"b","actor":"user.a","items":[],"paciente":"x"}`:     "invalid_request",
		`{"contract_version":"v2","batch_id":"b","actor":"user.a","items":[{"external_id":"a"}]}`: "unsupported_contract_version",
	} {
		rec = callAs(h, http.MethodPost, "/internal/v1/attribution/receipts:batch", "fono-crm", handlerTokenA, body)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec.Body.Bytes()) != code {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// Unknown kind is not a route.
	if rec = callAs(h, http.MethodPost, "/internal/v1/attribution/pacientes:batch", "fono-crm", handlerTokenA, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown kind: %d", rec.Code)
	}
	// Unsupported model and bad parameters.
	if rec = callAs(h, http.MethodGet, "/internal/v1/attribution/revenue?model=ultimo-contato", "fono-crm", handlerTokenA, ""); errorCode(t, rec.Body.Bytes()) != "model_unsupported" {
		t.Fatalf("model: %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"?from=ontem", "?from=2026-11-10&to=2026-11-01", "?x=1", "?format=pdf", "?unit=%3Dcmd"} {
		if rec = callAs(h, http.MethodGet, "/internal/v1/attribution/revenue"+q, "fono-crm", handlerTokenA, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	// A period filter keeps the invariant.
	if p := readRevenue(t, h, "fono-crm", handlerTokenA, "?from=2026-11-01&to=2026-11-30"); !p.Invariant.Closes {
		t.Fatalf("period: %+v", p)
	}

	// The batch is audited with identifiers only (no amounts).
	rec = callAs(h, http.MethodGet, "/internal/v1/audit?entity_type=attribution_batch&entity_id=imp-1-receipts", "fono-crm", handlerTokenA, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"action":"attribution.batch_imported"`) ||
		strings.Contains(rec.Body.String(), "amount") || strings.Contains(rec.Body.String(), "48000") {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAttributionRevenueCSV(t *testing.T) {
	h := attributionHandler(t)
	importAll(t, h, "fono-crm", handlerTokenA, "fono-demo", "amostra-fono", "c")
	rec := callAs(h, http.MethodGet, "/internal/v1/attribution/revenue?format=csv", "fono-crm", handlerTokenA, "")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rows[0], ",") != strings.Join(revenueCSVHeader, ",") {
		t.Fatalf("header: %v", rows[0])
	}
	last := rows[len(rows)-1]
	if last[0] != "total" || last[6] != "129000" {
		t.Fatalf("total row: %v", last)
	}
	for _, row := range rows[1:] {
		for _, cell := range row {
			if cell != "" && strings.ContainsAny(cell[:1], "=+-@") {
				t.Fatalf("formula-like cell: %q", cell)
			}
		}
	}
}

func TestAttributionIsolationAndModuleGate(t *testing.T) {
	h := attributionHandler(t)
	importAll(t, h, "fono-crm", handlerTokenA, "fono-demo", "amostra-fono", "f")

	// escola-sis cannot write or read fono-demo.
	rec := callAs(h, http.MethodPost, "/internal/v1/attribution/receipts:batch", "escola-sis", handlerTokenB,
		batchBody(t, "fono-demo", "amostra-fono", "receipts", "intrusion"))
	if rec.Code != http.StatusForbidden || errorCode(t, rec.Body.Bytes()) != "tenant_forbidden" {
		t.Fatalf("cross-tenant write: %d %s", rec.Code, rec.Body.String())
	}
	if rec = callAs(h, http.MethodGet, "/internal/v1/attribution/revenue?tenant_id=fono-demo", "escola-sis", handlerTokenB, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant read: %d", rec.Code)
	}
	// escola-demo sees nothing of fono-demo.
	if r := readRevenue(t, h, "escola-sis", handlerTokenB, ""); r.TenantID != "escola-demo" || r.NetTotal != 0 || !r.Invariant.Closes {
		t.Fatalf("escola report: %+v", r)
	}
	// A read-only service cannot import.
	rec = callAs(h, http.MethodPost, "/internal/v1/attribution/receipts:batch", "escola-bi", handlerTokenA,
		batchBody(t, "escola-demo", "amostra-fono", "receipts", "bi"))
	if rec.Code != http.StatusForbidden || errorCode(t, rec.Body.Bytes()) != "capability_forbidden" {
		t.Fatalf("read-only import: %d %s", rec.Code, rec.Body.String())
	}
	// Paused module: 403 module_paused on both routes.
	rec = callAs(h, http.MethodPost, "/internal/v1/attribution/receipts:batch", "loja-sis", handlerTokenA,
		batchBody(t, "loja-demo", "amostra-fono", "receipts", "l"))
	if rec.Code != http.StatusForbidden || errorCode(t, rec.Body.Bytes()) != "module_paused" {
		t.Fatalf("paused import: %d %s", rec.Code, rec.Body.String())
	}
	if rec = callAs(h, http.MethodGet, "/internal/v1/attribution/revenue", "loja-sis", handlerTokenA, ""); errorCode(t, rec.Body.Bytes()) != "module_paused" {
		t.Fatalf("paused read: %d %s", rec.Code, rec.Body.String())
	}
	// The module view reports integration readiness from the store.
	rec = callAs(h, http.MethodGet, "/internal/v1/modules", "escola-sis", handlerTokenB, "")
	if !strings.Contains(rec.Body.String(), `"missing":["contacts","receipts"]`) {
		t.Fatalf("escola readiness: %s", rec.Body.String())
	}
	rec = callAs(h, http.MethodGet, "/internal/v1/modules", "fono-crm", handlerTokenA, "")
	if !strings.Contains(rec.Body.String(), `"ready":true`) {
		t.Fatalf("fono readiness: %s", rec.Body.String())
	}
}

func TestAttributionDisabled(t *testing.T) {
	h := modulesHandler(t, false)
	rec := callAs(h, http.MethodGet, "/internal/v1/attribution/revenue", "escola-sis", handlerTokenB, "")
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec.Body.Bytes()) != "attribution_unavailable" {
		t.Fatalf("disabled: %d %s", rec.Code, rec.Body.String())
	}
}
