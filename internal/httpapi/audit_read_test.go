package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
)

const auditReadFixture = `{"contract_version":"v1","config_version":"fixture-audit-read",
"tenant_registry":[{"id":"tenant-demo-a","status":"active"},{"id":"tenant-demo-b","status":"active"},{"id":"tenant-demo-c","status":"inactive"}],
"key_schema":{"k.v":{"type":"integer"}},"tenants":{"tenant-demo-a":{"k.v":1}},
"callers":{
 "node-a":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["audit:write","audit:read"]},
 "node-b":{"token_env":"TOKEN_B","tenants":["tenant-demo-b"],"capabilities":["audit:write","audit:read"]},
 "node-writer":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["audit:write"]},
 "node-reader":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["audit:read"]},
 "node-multi":{"token_env":"TOKEN_B","tenants":["tenant-demo-a","tenant-demo-b","tenant-demo-c"],"capabilities":["audit:write","audit:read"]},
 "node-none":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"]}}}`

func auditReadHandler(t *testing.T) (http.Handler, *audit.JSONLStore) {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(auditReadFixture), func(name string) (string, bool) {
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
	return NewHandlerWithAudit(store, audit.NewService(jsonl), 16, slog.New(slog.NewTextHandler(io.Discard, nil))), jsonl
}

func readCall(h http.Handler, service, token, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/audit"+query, nil)
	req.Header.Set("X-KAS-Service", service)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func writeAudit(t *testing.T, h http.Handler, service, token, tenant, id, entityType, entityID string) {
	t.Helper()
	body := fmt.Sprintf(`{"contract_version":"v1","event_id":%q,"tenant_id":%q,"service_id":%q,"actor":"user.demo-01","action":"settings.updated","resource":"setting.x","result":"success","correlation_id":"corr.r","occurred_at":"2026-11-18T12:00:00Z"`, id, tenant, service)
	if entityType != "" {
		body += fmt.Sprintf(`,"entity_type":%q,"entity_id":%q`, entityType, entityID)
	}
	body += `}`
	if rec := auditCall(h, service, token, body); rec.Code != http.StatusCreated {
		t.Fatalf("write %s = %d %s", id, rec.Code, rec.Body.String())
	}
}

type readPage struct {
	ContractVersion string        `json:"contract_version"`
	Items           []audit.Event `json:"items"`
	NextCursor      string        `json:"next_cursor"`
}

func decodePage(t *testing.T, rec *httptest.ResponseRecorder) readPage {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d %s", rec.Code, rec.Body.String())
	}
	var p readPage
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("bad body %s: %v", rec.Body.String(), err)
	}
	return p
}

func TestReadAuditRequiresTheAuditReadCapability(t *testing.T) {
	h, _ := auditReadHandler(t)
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "evt.cap-1", "", "")
	for _, service := range []string{"node-writer", "node-none"} {
		rec := readCall(h, service, handlerTokenA, "")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "capability_forbidden") {
			t.Fatalf("%s without audit:read = %d %s, want 403 capability_forbidden", service, rec.Code, rec.Body.String())
		}
	}
	// audit:write alone does not read; audit:read alone does not write.
	if rec := auditCall(h, "node-reader", handlerTokenA, strings.Replace(validAuditBody, `"node-a"`, `"node-reader"`, 1)); rec.Code != http.StatusForbidden {
		t.Fatalf("a reader must not write: %d", rec.Code)
	}
	if page := decodePage(t, readCall(h, "node-reader", handlerTokenA, "")); len(page.Items) != 1 {
		t.Fatalf("a reader must see the tenant's records, got %d", len(page.Items))
	}
}

func TestReadAuditAuthenticationFailures(t *testing.T) {
	h, _ := auditReadHandler(t)
	for name, tc := range map[string]struct{ service, token string }{
		"no credentials":  {"", ""},
		"wrong token":     {"node-a", handlerTokenB},
		"unknown service": {"ghost", handlerTokenA},
		"malformed token": {"node-a", "short"},
	} {
		rec := readCall(h, tc.service, tc.token, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s = %d %s, want 401", name, rec.Code, rec.Body.String())
		}
	}
}

func TestReadAuditNeverReturnsAnotherTenantsRecords(t *testing.T) {
	h, _ := auditReadHandler(t)
	for i := 0; i < 4; i++ {
		writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", fmt.Sprintf("a-%d", i), "appointment", "app-1")
		writeAudit(t, h, "node-b", handlerTokenB, "tenant-demo-b", fmt.Sprintf("b-%d", i), "appointment", "app-1")
	}
	for service, want := range map[string]struct {
		token, tenant string
	}{"node-a": {handlerTokenA, "tenant-demo-a"}, "node-b": {handlerTokenB, "tenant-demo-b"}} {
		page := decodePage(t, readCall(h, service, want.token, "?limit=200"))
		if len(page.Items) != 4 {
			t.Fatalf("%s read %d records, want 4", service, len(page.Items))
		}
		for _, e := range page.Items {
			if e.TenantID != want.tenant {
				t.Fatalf("%s received a record of %s", service, e.TenantID)
			}
		}
	}
	// The tenant cannot be chosen from the request by a service that is not allowed for it.
	for _, query := range []string{"?tenant_id=tenant-demo-b", "?tenant_id=tenant-demo-b&entity_type=appointment&entity_id=app-1"} {
		rec := readCall(h, "node-a", handlerTokenA, query)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenant_forbidden") {
			t.Fatalf("node-a asking for tenant B (%s) = %d %s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "b-0") {
			t.Fatal("a refused request must not leak records")
		}
	}
	// The cursor of one tenant does not widen another's view.
	first := decodePage(t, readCall(h, "node-a", handlerTokenA, "?limit=2"))
	other := decodePage(t, readCall(h, "node-b", handlerTokenB, "?limit=200&cursor="+url.QueryEscape(first.NextCursor)))
	for _, e := range other.Items {
		if e.TenantID != "tenant-demo-b" {
			t.Fatalf("cursor reuse leaked %s", e.TenantID)
		}
	}
	// An unknown tenant has nothing, and the response is not an error for the filters.
	if rec := readCall(h, "node-multi", handlerTokenB, "?tenant_id=tenant-demo-ghost"); rec.Code != http.StatusNotFound {
		t.Fatalf("unregistered tenant = %d", rec.Code)
	}
	if rec := readCall(h, "node-multi", handlerTokenB, "?tenant_id=tenant-demo-c"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenant_inactive") {
		t.Fatalf("inactive tenant = %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadAuditTenantComesFromAuthenticationForOneTenantServicesAndIsSelectedForMany(t *testing.T) {
	h, _ := auditReadHandler(t)
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "evt.t-a", "", "")
	writeAudit(t, h, "node-b", handlerTokenB, "tenant-demo-b", "evt.t-b", "", "")
	// A single-tenant service needs no tenant parameter and gets exactly its own tenant.
	if page := decodePage(t, readCall(h, "node-a", handlerTokenA, "")); len(page.Items) != 1 || page.Items[0].TenantID != "tenant-demo-a" {
		t.Fatalf("single-tenant service = %+v", page.Items)
	}
	// It may restate its own tenant, but not any other.
	if page := decodePage(t, readCall(h, "node-a", handlerTokenA, "?tenant_id=tenant-demo-a")); len(page.Items) != 1 {
		t.Fatalf("restating the own tenant = %+v", page.Items)
	}
	// A multi-tenant service must say which one it means.
	if rec := readCall(h, "node-multi", handlerTokenB, ""); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "tenant_required") {
		t.Fatalf("multi-tenant without selection = %d %s", rec.Code, rec.Body.String())
	}
	for tenant, want := range map[string]string{"tenant-demo-a": "evt.t-a", "tenant-demo-b": "evt.t-b"} {
		page := decodePage(t, readCall(h, "node-multi", handlerTokenB, "?tenant_id="+tenant))
		if len(page.Items) != 1 || page.Items[0].EventID != want {
			t.Fatalf("multi-tenant selecting %s = %+v", tenant, page.Items)
		}
	}
}

func TestReadAuditPaginatesWithoutRepeatingOrSkipping(t *testing.T) {
	h, _ := auditReadHandler(t)
	const total = 33
	for i := 0; i < total; i++ {
		writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", fmt.Sprintf("p-%03d", i), "", "")
	}
	for _, limit := range []int{1, 5, 7, 32, 33, 34, 200} {
		seen := map[string]bool{}
		var order []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > total+2 {
				t.Fatalf("limit %d did not terminate", limit)
			}
			q := fmt.Sprintf("?limit=%d", limit)
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			page := decodePage(t, readCall(h, "node-a", handlerTokenA, q))
			if len(page.Items) > limit {
				t.Fatalf("limit %d returned %d items", limit, len(page.Items))
			}
			for _, e := range page.Items {
				if seen[e.EventID] {
					t.Fatalf("limit %d repeated %s", limit, e.EventID)
				}
				seen[e.EventID] = true
				order = append(order, e.EventID)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if len(order) != total {
			t.Fatalf("limit %d read %d records, want %d (skipped some)", limit, len(order), total)
		}
		for i, id := range order {
			if id != fmt.Sprintf("p-%03d", i) {
				t.Fatalf("limit %d out of order at %d: %s", limit, i, id)
			}
		}
	}
}

func TestReadAuditDefaultAndMaximumLimits(t *testing.T) {
	h, jsonl := auditReadHandler(t)
	service := audit.NewService(jsonl)
	for i := 0; i < 230; i++ {
		sub := audit.Submission{ContractVersion: "v1", EventID: fmt.Sprintf("l-%03d", i), TenantID: "tenant-demo-a", ServiceID: "node-a",
			Actor: "user.demo-01", Action: "settings.updated", Resource: "setting.x", Result: "success", CorrelationID: "corr.l", OccurredAt: "2026-11-18T12:00:00Z"}
		if _, err := service.Submit(t.Context(), "node-a", sub); err != nil {
			t.Fatal(err)
		}
	}
	if page := decodePage(t, readCall(h, "node-a", handlerTokenA, "")); len(page.Items) != 50 || page.NextCursor == "" {
		t.Fatalf("default limit returned %d items (cursor %q), want 50", len(page.Items), page.NextCursor)
	}
	if page := decodePage(t, readCall(h, "node-a", handlerTokenA, "?limit=200")); len(page.Items) != 200 || page.NextCursor == "" {
		t.Fatalf("maximum limit returned %d items, want 200", len(page.Items))
	}
	if page := decodePage(t, readCall(h, "node-a", handlerTokenA, "?limit=1")); len(page.Items) != 1 {
		t.Fatalf("limit 1 returned %d items", len(page.Items))
	}
}

func TestReadAuditFilters(t *testing.T) {
	h, _ := auditReadHandler(t)
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "f-1", "appointment", "app-1")
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "f-2", "appointment", "app-2")
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "f-3", "appointment", "app-1")
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "f-4", "package", "app-1")
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "f-5", "", "")
	idsOf := func(q string) string {
		page := decodePage(t, readCall(h, "node-a", handlerTokenA, q))
		out := make([]string, len(page.Items))
		for i, e := range page.Items {
			out[i] = e.EventID
		}
		return fmt.Sprint(out)
	}
	for q, want := range map[string]string{
		"":                         "[f-1 f-2 f-3 f-4 f-5]",
		"?entity_type=appointment": "[f-1 f-2 f-3]",
		"?entity_type=appointment&entity_id=app-1": "[f-1 f-3]",
		"?entity_type=package&entity_id=app-1":     "[f-4]",
		"?entity_type=appointment&entity_id=nope":  "[]",
		"?since=2999-01-01T00:00:00Z":              "[]",
		"?since=2000-01-01T00:00:00Z":              "[f-1 f-2 f-3 f-4 f-5]",
	} {
		if got := idsOf(q); got != want {
			t.Fatalf("query %q = %s, want %s", q, got, want)
		}
	}
	page := decodePage(t, readCall(h, "node-a", handlerTokenA, "?entity_type=appointment&entity_id=app-2"))
	if len(page.Items) != 1 || page.Items[0].EntityType != "appointment" || page.Items[0].EntityID != "app-2" || page.Items[0].ReceivedAt == "" {
		t.Fatalf("returned record = %+v", page.Items)
	}
	empty := readCall(h, "node-a", handlerTokenA, "?entity_type=appointment&entity_id=nope")
	if !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("an empty page must have an empty list, not null: %s", empty.Body.String())
	}
}

func TestReadAuditInvalidParametersAreRejectedWith400(t *testing.T) {
	h, _ := auditReadHandler(t)
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "v-1", "", "")
	for name, query := range map[string]string{
		"limit zero":               "?limit=0",
		"limit above the maximum":  "?limit=201",
		"negative limit":           "?limit=-5",
		"non-numeric limit":        "?limit=abc",
		"fractional limit":         "?limit=1.5",
		"empty limit":              "?limit=",
		"limit with a plus sign":   "?limit=%2B5",
		"huge limit":               "?limit=99999999999999999999",
		"since is not a timestamp": "?since=yesterday",
		"since is a date only":     "?since=2026-11-18",
		"empty since":              "?since=",
		"entity id without a type": "?entity_id=app-1",
		"entity type with spaces":  "?entity_type=bad%20type",
		"entity id with free text": "?entity_type=appointment&entity_id=Maria%20da%20Silva",
		"garbage cursor":           "?cursor=!!!",
		"cursor for nothing":       "?cursor=AAAA",
		"unknown parameter":        "?order=desc",
		"repeated limit":           "?limit=5&limit=6",
		"repeated entity type":     "?entity_type=a&entity_type=b",
		"oversized query":          "?entity_type=" + strings.Repeat("a", 3000),
	} {
		t.Run(name, func(t *testing.T) {
			rec := readCall(h, "node-a", handlerTokenA, query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d %s, want 400", query, rec.Code, rec.Body.String())
			}
		})
	}
	if rec := readCall(h, "node-a", handlerTokenA, "?cursor=!!!"); !strings.Contains(rec.Body.String(), "invalid_cursor") {
		t.Fatalf("a bad cursor must say invalid_cursor: %s", rec.Body.String())
	}
	// Valid boundaries are accepted.
	for _, ok := range []string{"?limit=1", "?limit=200", "?since=2026-11-18T12:00:00.123456789Z", "?since=2026-11-18T09:00:00-03:00"} {
		if rec := readCall(h, "node-a", handlerTokenA, ok); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s, want 200", ok, rec.Code, rec.Body.String())
		}
	}
}

func TestReadAuditMethodAndAvailability(t *testing.T) {
	h, _ := auditReadHandler(t)
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/internal/v1/audit", strings.NewReader("{}")))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST /internal/v1/audit = %d allow %q", post.Code, post.Header().Get("Allow"))
	}
	// A handler without audit storage refuses to read.
	store, _ := configstore.Load(strings.NewReader(auditReadFixture), func(name string) (string, bool) {
		if name == "TOKEN_A" {
			return handlerTokenA, true
		}
		return handlerTokenB, true
	})
	bare := NewHandler(store, 4, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rec := readCall(bare, "node-a", handlerTokenA, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no audit storage = %d", rec.Code)
	}
}

func TestReadAuditResponseNeverLeaksSecretsOrOtherFields(t *testing.T) {
	h, _ := auditReadHandler(t)
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "s-1", "appointment", "app-1")
	rec := readCall(h, "node-a", handlerTokenA, "")
	body := rec.Body.String()
	for _, secret := range []string{handlerTokenA, handlerTokenB, "lease_token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaks %q", secret)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Correlation-ID") == "" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestReadAuditLivesUnderTheInternalPrefixOnly(t *testing.T) {
	h, _ := auditReadHandler(t)
	writeAudit(t, h, "node-a", handlerTokenA, "tenant-demo-a", "pre-1", "", "")
	// The route is /internal/v1/audit, next to POST /internal/v1/audit/events.
	if page := decodePage(t, readCall(h, "node-a", handlerTokenA, "")); len(page.Items) != 1 {
		t.Fatalf("read under the internal prefix = %+v", page.Items)
	}
	// The earlier path was never released as supported: it no longer exists.
	for _, path := range []string{"/v1/audit", "/v1/audit/events"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-KAS-Service", "node-a")
		req.Header.Set("Authorization", "Bearer "+handlerTokenA)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	// Reading and writing are separate routes: the events route still only accepts POST.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/audit/events", nil)
	req.Header.Set("X-KAS-Service", "node-a")
	req.Header.Set("Authorization", "Bearer "+handlerTokenA)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /internal/v1/audit/events = %d, want 405", rec.Code)
	}
}
