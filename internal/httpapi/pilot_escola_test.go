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
	"regexp"
	"strings"
	"testing"

	"kas-core-pilot/internal/attribution"
	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/pgtest"
)

// Pilot of the second fictional company (spec "KAS por módulos", section 9).
// Each configuration below is one moment of the demo; the KAS is restarted
// with it over the same database, exactly like an administrator changing the
// configuration. Steps:
//
//  1. create the company (registered, no module contracted);
//  2. activate attribution + reports;
//  3. import its four CSV files: the report closes;
//  4. reimport the same files: same totals;
//  5. pause: 403 module_paused, data kept; reactivate: same numbers;
//  6. isolation from fono-demo, both ways;
//  7. no code of its own (scan of the module packages).
const pilotBase = `{"contract_version":"v1","config_version":"%VERSION%",
"tenant_registry":[{"id":"fono-demo","status":"active"}%ESCOLA_TENANT%],
"key_schema":{"branding.name":{"type":"string"},"branding.color":{"type":"string"}},
"tenants":{"fono-demo":{"branding.name":"Clínica Fictícia"}%ESCOLA_VALUES%},
"modules":{"fono-demo":{"attribution":{"status":"active"},"reports":{"status":"active"}}%ESCOLA_MODULES%},
"callers":{
 "fono-crm":{"token_env":"TOKEN_A","tenants":["fono-demo"],"capabilities":["attribution:import","attribution:read","reports:read"]}%ESCOLA_CALLERS%}}`

func pilotConfig(version, escolaModules string) string {
	r := strings.NewReplacer("%VERSION%", version, "%ESCOLA_TENANT%", "", "%ESCOLA_VALUES%", "", "%ESCOLA_MODULES%", "", "%ESCOLA_CALLERS%", "")
	if version != "v1-sem-escola" {
		callerCaps := `[]`
		if escolaModules != "" {
			callerCaps = `["attribution:import","attribution:read","reports:read"]`
		}
		mods := ""
		if escolaModules != "" {
			mods = `,"escola-demo":` + escolaModules
		}
		r = strings.NewReplacer("%VERSION%", version,
			"%ESCOLA_TENANT%", `,{"id":"escola-demo","status":"active"}`,
			"%ESCOLA_VALUES%", `,"escola-demo":{"branding.name":"Escola Fictícia Aurora","branding.color":"#3355cc"}`,
			"%ESCOLA_MODULES%", mods,
			"%ESCOLA_CALLERS%", `,"escola-sis":{"token_env":"TOKEN_B","tenants":["escola-demo"],"capabilities":`+callerCaps+`}`)
	}
	return r.Replace(pilotBase)
}

type pilot struct {
	t   *testing.T
	dsn string
}

// start boots a KAS with one configuration over the shared database.
func (p pilot) start(config string) *Handler {
	p.t.Helper()
	store, err := configstore.Load(strings.NewReader(config), func(name string) (string, bool) {
		switch name {
		case "TOKEN_A":
			return handlerTokenA, true
		case "TOKEN_B":
			return handlerTokenB, true
		}
		return "", false
	})
	if err != nil {
		p.t.Fatalf("config refused: %v", err)
	}
	jsonl, err := audit.OpenJSONL(p.t.TempDir())
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { _ = jsonl.Close() })
	h := NewHandlerWithAudit(store, audit.NewService(jsonl), 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fp, _ := attribution.NewFingerprinter([]byte("fictional-pilot-key-0123456789abcdef"))
	as, err := attribution.Open(context.Background(), p.dsn, true, fp)
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(as.Close)
	h.EnableAttribution(as)
	return h
}

func TestEscolaDemoPilot(t *testing.T) {
	p := pilot{t: t, dsn: pgtest.SchemaDSN(t)}
	active := `{"attribution":{"status":"active"},"reports":{"status":"active"}}`
	paused := `{"attribution":{"status":"paused"},"reports":{"status":"paused"}}`

	// Before: only fono-demo exists; it has its data.
	h := p.start(pilotConfig("v1-sem-escola", ""))
	importAll(t, h, "fono-crm", handlerTokenA, "fono-demo", "amostra-fono", "fono")
	fonoBefore := readRevenue(t, h, "fono-crm", handlerTokenA, "")

	// 1. Company created, nothing contracted: the view shows every module absent.
	h = p.start(pilotConfig("v2-escola-criada", ""))
	rec := callAs(h, http.MethodGet, "/internal/v1/modules", "escola-sis", handlerTokenB, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"key":"attribution","contract":"absent"`) {
		t.Fatalf("step 1 view: %d %s", rec.Code, rec.Body.String())
	}

	// 2. Attribution + Reports activated.
	h = p.start(pilotConfig("v3-escola-ativa", active))
	rec = callAs(h, http.MethodGet, "/internal/v1/modules", "escola-sis", handlerTokenB, "")
	if !strings.Contains(rec.Body.String(), `"key":"attribution","contract":"active"`) ||
		!strings.Contains(rec.Body.String(), `"missing":["contacts","receipts"]`) {
		t.Fatalf("step 2 view: %s", rec.Body.String())
	}

	// 3. Import: the report closes with the expected numbers.
	importAll(t, h, "escola-sis", handlerTokenB, "escola-demo", "escola-demo", "carga-1")
	first := readRevenue(t, h, "escola-sis", handlerTokenB, "")
	if first.TenantID != "escola-demo" || first.NetTotal != 647000 || first.ByLevel["comprovada"] != 267000 ||
		first.ByLevel["provavel"] != 285000 || first.ByLevel["sem_vinculo"] != 95000 || !first.Invariant.Closes {
		t.Fatalf("step 3 report: %+v", first)
	}
	if rec = callAs(h, http.MethodGet, "/internal/v1/modules", "escola-sis", handlerTokenB, ""); !strings.Contains(rec.Body.String(), `"ready":true`) {
		t.Fatalf("step 3 readiness: %s", rec.Body.String())
	}

	// 4. Reimport (new batch ids, same files): same totals, nothing created.
	rec = callAs(h, http.MethodPost, "/internal/v1/attribution/receipts:batch", "escola-sis", handlerTokenB,
		batchBody(t, "escola-demo", "escola-demo", "receipts", "carga-2-receipts"))
	var again attributionBatchResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if rec.Code != http.StatusCreated || again.Created != 0 || again.Updated != 0 || again.Unchanged != 9 {
		t.Fatalf("step 4 reimport: %s", rec.Body.String())
	}
	if r := readRevenue(t, h, "escola-sis", handlerTokenB, ""); r.NetTotal != first.NetTotal || r.ByLevel["provavel"] != first.ByLevel["provavel"] {
		t.Fatalf("step 4 totals changed: %+v", r)
	}

	// 5. Pause: 403 module_paused on import, report and HTML; then reactivate.
	h = p.start(pilotConfig("v4-escola-pausada", paused))
	for _, path := range []string{"/internal/v1/attribution/revenue", "/internal/v1/reports/revenue"} {
		if rec = callAs(h, http.MethodGet, path, "escola-sis", handlerTokenB, ""); errorCode(t, rec.Body.Bytes()) != "module_paused" {
			t.Fatalf("step 5 %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec = callAs(h, http.MethodPost, "/internal/v1/attribution/receipts:batch", "escola-sis", handlerTokenB,
		batchBody(t, "escola-demo", "escola-demo", "receipts", "durante-pausa"))
	if errorCode(t, rec.Body.Bytes()) != "module_paused" {
		t.Fatalf("step 5 import: %s", rec.Body.String())
	}
	h = p.start(pilotConfig("v5-escola-reativada", active))
	if r := readRevenue(t, h, "escola-sis", handlerTokenB, ""); r.NetTotal != first.NetTotal || r.ByLevel["comprovada"] != first.ByLevel["comprovada"] {
		t.Fatalf("step 5 reactivated: %+v", r)
	}

	// 6. Isolation, both ways: fono-demo unchanged, no cross reads.
	if r := readRevenue(t, h, "fono-crm", handlerTokenA, ""); r.NetTotal != fonoBefore.NetTotal || r.NetTotal != 129000 {
		t.Fatalf("step 6 fono changed: %+v", r)
	}
	for _, c := range []struct{ service, token, tenant string }{
		{"escola-sis", handlerTokenB, "fono-demo"}, {"fono-crm", handlerTokenA, "escola-demo"},
	} {
		for _, path := range []string{"/internal/v1/attribution/revenue", "/internal/v1/reports/revenue"} {
			if rec = callAs(h, http.MethodGet, path+"?tenant_id="+c.tenant, c.service, c.token, ""); errorCode(t, rec.Body.Bytes()) != "tenant_forbidden" {
				t.Fatalf("step 6 %s reading %s: %d", c.service, c.tenant, rec.Code)
			}
		}
	}

	// The report a company receives: branded HTML and CSV, reconciled.
	rec = callAs(h, http.MethodGet, "/internal/v1/reports/revenue?from=2026-11-01&to=2026-11-30", "escola-sis", handlerTokenB, "")
	page := rec.Body.String()
	if out := os.Getenv("KAS_PILOT_HTML_OUT"); out != "" {
		_ = os.WriteFile(out, rec.Body.Bytes(), 0o644)
	}
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(page, "Escola Fictícia Aurora") || !strings.Contains(page, "#3355cc") ||
		!strings.Contains(page, "R$ 6.470,00") || !strings.Contains(page, "Conferido:") ||
		strings.Contains(page, "Clínica Fictícia") || strings.Contains(page, "<script") ||
		rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("html report: %d\n%s", rec.Code, page)
	}
	rec = callAs(h, http.MethodGet, "/internal/v1/reports/revenue?format=csv", "escola-sis", handlerTokenB, "")
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || rows[len(rows)-1][6] != "647000" {
		t.Fatalf("csv report: %v %v", rows, err)
	}
	// Unknown format.
	if rec = callAs(h, http.MethodGet, "/internal/v1/reports/revenue?format=pdf", "escola-sis", handlerTokenB, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad format: %d", rec.Code)
	}

	// 7. No code of its own: the module packages never name a company or a
	// business entity of one.
	assertNoTenantCode(t)
}

func assertNoTenantCode(t *testing.T) {
	t.Helper()
	forbidden := regexp.MustCompile(`(?i)escola|fono|pacient|aluno|matr[ií]cula|mensalidade`)
	files := []string{
		"attribution.go", "reports.go", "modules.go",
		"../configstore/modules.go",
		"../../cmd/kas-attribution-import/main.go",
	}
	for _, dir := range []string{"../attribution", "../modules"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		files = append(files, matches...)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := forbidden.Find(raw); m != nil {
			t.Fatalf("%s names %q: tenant-specific code in a generic module", f, m)
		}
	}
}

func TestBrlFormat(t *testing.T) {
	for cents, want := range map[int64]string{0: "R$ 0,00", 5: "R$ 0,05", 129000: "R$ 1.290,00", 123456789: "R$ 1.234.567,89", -4300: "-R$ 43,00"} {
		if got := brl(cents); got != want {
			t.Errorf("brl(%d) = %q, want %q", cents, got, want)
		}
	}
}

func TestReportsBrandingFallsBack(t *testing.T) {
	h := attributionHandler(t)
	// The fixture has no branding keys and no reports module.
	if b := h.brandingOf("escola-demo"); b.Name != "escola-demo" || b.Color != defaultColor {
		t.Fatalf("fallback: %+v", b)
	}
	rec := callAs(h, http.MethodGet, "/internal/v1/reports/revenue", "escola-sis", handlerTokenB, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reports without capability: %d %s", rec.Code, rec.Body.String())
	}
}

// The pilot configuration shipped in deploy/ must load.
func TestPilotConfigLoads(t *testing.T) {
	f, err := os.Open("../../deploy/piloto-escola/config.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tokens := map[string]string{"PILOTO_FONO_TOKEN": handlerTokenA, "PILOTO_ESCOLA_TOKEN": handlerTokenB}
	store, err := configstore.Load(f, func(name string) (string, bool) { v, ok := tokens[name]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if store.ModuleStatus("escola-demo", configstore.ModuleReports) != configstore.ModuleStatusActive {
		t.Fatal("reports not active for escola-demo")
	}
}
