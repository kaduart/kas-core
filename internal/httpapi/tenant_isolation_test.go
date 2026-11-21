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

	"kas-core-pilot/internal/attribution"
	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/identity"
	"kas-core-pilot/internal/pgtest"
)

// Tenant isolation for the panel (a service that signs people in). The panel
// "kas-web" may serve both companies, but every data request must carry the
// person's session, and the company must be one of that person's memberships.
// Ana works only at escola-demo, Bruno only at fono-demo, Carla at both.
// "kas-web-2" is a second panel; its sessions are worthless to kas-web.
// "escola-sis" and "fono-crm" are ordinary services, not affected.
const isolationConfig = `{"contract_version":"v1","config_version":"isolamento-1",
"tenant_registry":[{"id":"escola-demo","status":"active"},{"id":"fono-demo","status":"active"}],
"key_schema":{"branding.name":{"type":"string"}},
"tenants":{"escola-demo":{"branding.name":"Escola Fictícia"},"fono-demo":{"branding.name":"Clínica Fictícia"}},
"modules":{
 "escola-demo":{"attribution":{"status":"active"},"reports":{"status":"active"}},
 "fono-demo":{"attribution":{"status":"active"},"reports":{"status":"active"}}},
"callers":{
 "kas-web":{"token_env":"TOKEN_A","tenants":["escola-demo","fono-demo"],"capabilities":["identity:sessions","attribution:read","reports:read"]},
 "kas-web-2":{"token_env":"TOKEN_C","tenants":["escola-demo","fono-demo"],"capabilities":["identity:sessions","attribution:read"]},
 "escola-sis":{"token_env":"TOKEN_B","tenants":["escola-demo"],"capabilities":["attribution:import","attribution:read"]},
 "fono-crm":{"token_env":"TOKEN_D","tenants":["fono-demo"],"capabilities":["attribution:import","attribution:read"]}}}`

const (
	isolationTokenC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	isolationTokenD = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func isolationHandler(t *testing.T) (*Handler, *identity.Store) {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(isolationConfig), func(name string) (string, bool) {
		return map[string]string{"TOKEN_A": handlerTokenA, "TOKEN_B": handlerTokenB, "TOKEN_C": isolationTokenC, "TOKEN_D": isolationTokenD}[name], true
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
	dsn := pgtest.SchemaDSN(t)
	fp, err := attribution.NewFingerprinter([]byte("fictional-test-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	as, err := attribution.Open(context.Background(), dsn, true, fp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(as.Close)
	h.EnableAttribution(as)
	ids, err := identity.Open(context.Background(), dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ids.Close)
	h.EnableIdentity(ids)
	ctx := context.Background()
	for _, u := range []struct{ email, tenants string }{
		{"ana@escola.exemplo", "escola-demo"}, {"bruno@fono.exemplo", "fono-demo"}, {"carla@ambas.exemplo", "escola-demo,fono-demo"},
	} {
		if _, err := ids.CreateUser(ctx, u.email, u.email, "senha-forte-123"); err != nil {
			t.Fatal(err)
		}
		for _, tenant := range strings.Split(u.tenants, ",") {
			if err := ids.SetMembership(ctx, u.email, tenant, "leitura"); err != nil {
				t.Fatal(err)
			}
		}
	}
	importAll(t, h, "escola-sis", handlerTokenB, "escola-demo", "escola-demo", "iso-escola")
	importAll(t, h, "fono-crm", isolationTokenD, "fono-demo", "amostra-fono", "iso-fono")
	return h, ids
}

func signIn(t *testing.T, h *Handler, service, token, email string) string {
	t.Helper()
	rec := identityCall(h, http.MethodPost, "/internal/v1/identity/sessions", service, token, "",
		`{"contract_version":"v1","email":"`+email+`","password":"senha-forte-123"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sign-in %s: %d %s", email, rec.Code, rec.Body)
	}
	var body struct {
		Token string `json:"session_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Token
}

type isoResult struct {
	code  int
	error string
	total int64
	raw   string
}

func isoGet(h http.Handler, service, token, session, path string) isoResult {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		req.Header.Set("X-KAS-Session", session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Total int64 `json:"total_liquido_centavos"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return isoResult{rec.Code, body.Error.Code, body.Total, rec.Body.String()}
}

func TestPanelCannotReadAnotherCompany(t *testing.T) {
	h, ids := isolationHandler(t)
	ana := signIn(t, h, "kas-web", handlerTokenA, "ana@escola.exemplo")
	bruno := signIn(t, h, "kas-web", handlerTokenA, "bruno@fono.exemplo")
	carla := signIn(t, h, "kas-web", handlerTokenA, "carla@ambas.exemplo")

	// Each company's real total, read by its own ordinary service.
	escola := isoGet(h, "escola-sis", handlerTokenB, "", "/internal/v1/attribution/revenue")
	fono := isoGet(h, "fono-crm", isolationTokenD, "", "/internal/v1/attribution/revenue")
	if escola.code != 200 || fono.code != 200 || escola.total == fono.total || escola.total != 647000 {
		t.Fatalf("fixture: escola %+v fono %+v", escola, fono)
	}

	dataRoutes := []string{
		"/internal/v1/attribution/revenue?tenant_id=",
		"/internal/v1/attribution/revenue?format=csv&tenant_id=",
		"/internal/v1/reports/revenue?tenant_id=",
		"/internal/v1/reports/revenue?format=csv&tenant_id=",
		"/internal/v1/modules?tenant_id=",
	}

	t.Run("sem sessão a credencial do painel não abre empresa nenhuma", func(t *testing.T) {
		for _, route := range dataRoutes {
			for _, tenant := range []string{"escola-demo", "fono-demo"} {
				if r := isoGet(h, "kas-web", handlerTokenA, "", route+tenant); r.code != 401 || r.error != "session_required" {
					t.Errorf("%s%s sem sessão: %d %s", route, tenant, r.code, r.error)
				}
			}
		}
	})

	t.Run("cada pessoa lê só a própria empresa", func(t *testing.T) {
		for _, route := range dataRoutes {
			if r := isoGet(h, "kas-web", handlerTokenA, ana, route+"escola-demo"); r.code != 200 {
				t.Errorf("ana escola %s: %d %s", route, r.code, r.raw)
			}
			if r := isoGet(h, "kas-web", handlerTokenA, ana, route+"fono-demo"); r.code != 403 || r.error != "tenant_forbidden" || strings.Contains(r.raw, "liquido") {
				t.Errorf("ana leu fono em %s: %d %s", route, r.code, r.raw)
			}
			if r := isoGet(h, "kas-web", handlerTokenA, bruno, route+"escola-demo"); r.code != 403 || r.error != "tenant_forbidden" {
				t.Errorf("bruno leu escola em %s: %d %s", route, r.code, r.raw)
			}
		}
		if r := isoGet(h, "kas-web", handlerTokenA, ana, "/internal/v1/attribution/revenue?tenant_id=escola-demo"); r.total != escola.total {
			t.Errorf("ana vê %d, escola tem %d", r.total, escola.total)
		}
		if r := isoGet(h, "kas-web", handlerTokenA, bruno, "/internal/v1/attribution/revenue?tenant_id=fono-demo"); r.total != fono.total {
			t.Errorf("bruno vê %d, fono tem %d", r.total, fono.total)
		}
	})

	t.Run("quem tem as duas empresas lê cada uma com o próprio número", func(t *testing.T) {
		if r := isoGet(h, "kas-web", handlerTokenA, carla, "/internal/v1/attribution/revenue?tenant_id=escola-demo"); r.total != escola.total {
			t.Errorf("carla escola: %+v", r)
		}
		if r := isoGet(h, "kas-web", handlerTokenA, carla, "/internal/v1/attribution/revenue?tenant_id=fono-demo"); r.total != fono.total {
			t.Errorf("carla fono: %+v", r)
		}
		// Painel com várias empresas precisa dizer qual; não há empresa padrão.
		if r := isoGet(h, "kas-web", handlerTokenA, carla, "/internal/v1/attribution/revenue"); r.code != 400 {
			t.Errorf("sem tenant_id: %d %s", r.code, r.raw)
		}
	})

	t.Run("sessão inválida, de outro painel ou encerrada não vale", func(t *testing.T) {
		route := "/internal/v1/attribution/revenue?tenant_id=escola-demo"
		if r := isoGet(h, "kas-web", handlerTokenA, "kss_"+strings.Repeat("0", 64), route); r.code != 401 || r.error != "session_invalid" {
			t.Errorf("token inventado: %d %s", r.code, r.error)
		}
		other := signIn(t, h, "kas-web-2", isolationTokenC, "ana@escola.exemplo")
		if r := isoGet(h, "kas-web", handlerTokenA, other, route); r.code != 401 {
			t.Errorf("sessão de outro painel: %d %s", r.code, r.error)
		}
		if err := ids.Revoke(context.Background(), ana, "kas-web"); err != nil {
			t.Fatal(err)
		}
		if r := isoGet(h, "kas-web", handlerTokenA, ana, route); r.code != 401 || r.error != "session_invalid" {
			t.Errorf("sessão encerrada: %d %s", r.code, r.error)
		}
	})

	t.Run("membro removido perde o acesso na hora", func(t *testing.T) {
		dani := "dani@escola.exemplo"
		if _, err := ids.CreateUser(context.Background(), dani, "Dani", "senha-forte-123"); err != nil {
			t.Fatal(err)
		}
		_ = ids.SetMembership(context.Background(), dani, "escola-demo", "leitura")
		_ = ids.SetMembership(context.Background(), dani, "fono-demo", "leitura")
		s := signIn(t, h, "kas-web", handlerTokenA, dani)
		if err := ids.RemoveMembership(context.Background(), dani, "fono-demo"); err != nil {
			t.Fatal(err)
		}
		if r := isoGet(h, "kas-web", handlerTokenA, s, "/internal/v1/attribution/revenue?tenant_id=fono-demo"); r.code != 403 {
			t.Errorf("depois de remover: %d %s", r.code, r.raw)
		}
	})

	t.Run("serviços comuns continuam como antes", func(t *testing.T) {
		if r := isoGet(h, "escola-sis", handlerTokenB, "", "/internal/v1/attribution/revenue?tenant_id=fono-demo"); r.code != 403 || r.error != "tenant_forbidden" {
			t.Errorf("escola-sis leu fono: %d %s", r.code, r.raw)
		}
	})
}

func TestPanelServiceCannotHoldWriteCapabilities(t *testing.T) {
	cfg := strings.Replace(isolationConfig, `"capabilities":["identity:sessions","attribution:read","reports:read"]`,
		`"capabilities":["identity:sessions","attribution:read","attribution:import"]`, 1)
	_, err := configstore.Load(strings.NewReader(cfg), func(string) (string, bool) { return handlerTokenA, true })
	if err == nil || !strings.Contains(err.Error(), "identity:sessions") {
		t.Fatalf("panel with attribution:import accepted: %v", err)
	}
}
