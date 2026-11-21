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

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/identity"
	"kas-core-pilot/internal/pgtest"
)

// The panel "kas-web" may act only for escola-demo; "fono-crm" has no
// identity:sessions. Ana belongs to escola-demo (admin) and fono-demo.
const identityConfig = `{"contract_version":"v1","config_version":"id-1",
"tenant_registry":[{"id":"escola-demo","status":"active"},{"id":"fono-demo","status":"active"}],
"key_schema":{"branding.name":{"type":"string"}},"tenants":{},
"callers":{
 "kas-web":{"token_env":"TOKEN_A","tenants":["escola-demo"],"capabilities":["identity:sessions"]},
 "fono-crm":{"token_env":"TOKEN_B","tenants":["fono-demo"],"capabilities":["config:read"]}}}`

func identityHandler(t *testing.T) *Handler {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(identityConfig), func(name string) (string, bool) {
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
	ids, err := identity.Open(context.Background(), pgtest.SchemaDSN(t), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ids.Close)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, "ana@escola.exemplo", "Ana Souza", "senha-forte-123"); err != nil {
		t.Fatal(err)
	}
	_ = ids.SetMembership(ctx, "ana@escola.exemplo", "escola-demo", "admin")
	_ = ids.SetMembership(ctx, "ana@escola.exemplo", "fono-demo", "leitura")
	h := NewHandler(store, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.EnableIdentity(ids)
	return h
}

func identityCall(h *Handler, method, path, service, token, session, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		req.Header.Set("X-KAS-Session", session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIdentityRoutes(t *testing.T) {
	h := identityHandler(t)
	login := `{"contract_version":"v1","email":"ana@escola.exemplo","password":"senha-forte-123"}`

	// A service without identity:sessions cannot sign anyone in.
	if rec := identityCall(h, http.MethodPost, "/internal/v1/identity/sessions", "fono-crm", handlerTokenB, "", login); rec.Code != http.StatusForbidden {
		t.Fatalf("service without capability: %d %s", rec.Code, rec.Body)
	}
	if rec := identityCall(h, http.MethodPost, "/internal/v1/identity/sessions", "kas-web", "wrong", "", login); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad service token: %d", rec.Code)
	}
	wrong := strings.Replace(login, "senha-forte-123", "errada-000000", 1)
	if rec := identityCall(h, http.MethodPost, "/internal/v1/identity/sessions", "kas-web", handlerTokenA, "", wrong); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_credentials") {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body)
	}
	if rec := identityCall(h, http.MethodPost, "/internal/v1/identity/sessions", "kas-web", handlerTokenA, "", `{"email":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}

	rec := identityCall(h, http.MethodPost, "/internal/v1/identity/sessions", "kas-web", handlerTokenA, "", login)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatal("response echoes password")
	}
	var got sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// fono-demo is not visible through this panel.
	if got.SessionToken == "" || len(got.Memberships) != 1 || got.Memberships[0].TenantID != "escola-demo" || got.Memberships[0].Role != "admin" {
		t.Fatalf("login body %+v", got)
	}

	rec = identityCall(h, http.MethodGet, "/internal/v1/identity/session", "kas-web", handlerTokenA, got.SessionToken, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "session_token") {
		t.Fatalf("session: %d %s", rec.Code, rec.Body)
	}
	if rec := identityCall(h, http.MethodGet, "/internal/v1/identity/session", "kas-web", handlerTokenA, "kss_nope", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage session: %d", rec.Code)
	}
	if rec := identityCall(h, http.MethodDelete, "/internal/v1/identity/session", "kas-web", handlerTokenA, got.SessionToken, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := identityCall(h, http.MethodGet, "/internal/v1/identity/session", "kas-web", handlerTokenA, got.SessionToken, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", rec.Code)
	}
}

func TestIdentityDisabled(t *testing.T) {
	store, err := configstore.Load(strings.NewReader(identityConfig), func(string) (string, bool) { return handlerTokenA, true })
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := identityCall(h, http.MethodGet, "/internal/v1/identity/session", "kas-web", handlerTokenA, "", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled: %d", rec.Code)
	}
}
