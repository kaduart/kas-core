package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
)

const handlerFixture = `{"contract_version":"v1","config_version":"fixture-1","tenant_registry":[{"id":"tenant-demo-a","status":"active"},{"id":"tenant-demo-b","status":"active"}],"key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},"tenants":{"tenant-demo-a":{"logging.max_meta_keys":10},"tenant-demo-b":{"logging.max_meta_keys":4}},"callers":{"node-a":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["config","audit"]},"node-b":{"token_env":"TOKEN_B","tenants":["tenant-demo-b"],"capabilities":["config","audit"]},"node-config":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["config"]},"node-audit":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["audit"]},"node-none":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"]}}}`
const handlerTokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const handlerTokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(handlerFixture), func(name string) (string, bool) {
		if name == "TOKEN_A" {
			return handlerTokenA, true
		}
		if name == "TOKEN_B" {
			return handlerTokenB, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(store, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func call(handler http.Handler, service, token, tenant, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/config/evaluate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	if tenant != "" {
		req.Body = io.NopCloser(strings.NewReader(body))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestEvaluateAndTenantBoundary(t *testing.T) {
	h := testHandler(t)
	body := `{"contract_version":"v1","tenant_id":"tenant-demo-a","key":"logging.max_meta_keys","expected_type":"integer"}`
	got := call(h, "node-a", handlerTokenA, "tenant-demo-a", body)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"value":10`) {
		t.Fatalf("A response: %d %s", got.Code, got.Body.String())
	}
	bodyB := strings.Replace(body, "tenant-demo-a", "tenant-demo-b", 1)
	gotB := call(h, "node-b", handlerTokenB, "tenant-demo-b", bodyB)
	if gotB.Code != http.StatusOK || !strings.Contains(gotB.Body.String(), `"value":4`) {
		t.Fatalf("B response: %d %s", gotB.Code, gotB.Body.String())
	}
	denied := call(h, "node-a", handlerTokenA, "tenant-demo-b", bodyB)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), `"tenant_forbidden"`) {
		t.Fatalf("cross-tenant response: %d %s", denied.Code, denied.Body.String())
	}
}

func TestConfigRejectsInactiveAndUnknownTenants(t *testing.T) {
	inactiveFixture := strings.Replace(handlerFixture, `{"id":"tenant-demo-a","status":"active"}`, `{"id":"tenant-demo-a","status":"inactive"}`, 1)
	store, err := configstore.Load(strings.NewReader(inactiveFixture), func(name string) (string, bool) {
		if name == "TOKEN_A" {
			return handlerTokenA, true
		}
		if name == "TOKEN_B" {
			return handlerTokenB, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := `{"contract_version":"v1","tenant_id":"tenant-demo-a","key":"logging.max_meta_keys","expected_type":"integer"}`
	if got := call(h, "node-a", handlerTokenA, "tenant-demo-a", body); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"tenant_inactive"`) {
		t.Fatalf("inactive tenant response = %d %s", got.Code, got.Body.String())
	}
	unknownBody := strings.Replace(body, "tenant-demo-a", "tenant-unknown", 1)
	if got := call(h, "node-a", handlerTokenA, "tenant-unknown", unknownBody); got.Code != http.StatusNotFound || !strings.Contains(got.Body.String(), `"tenant_not_found"`) {
		t.Fatalf("unknown tenant response = %d %s", got.Code, got.Body.String())
	}
}

func TestUnauthorizedAndInvalidRequest(t *testing.T) {
	h := testHandler(t)
	body := `{"contract_version":"v1","tenant_id":"tenant-demo-a","key":"logging.max_meta_keys","expected_type":"integer"}`
	if got := call(h, "node-a", handlerTokenB, "tenant-demo-a", body); got.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", got.Code)
	}
	if got := call(h, "node-a", "", "tenant-demo-a", body); got.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", got.Code)
	}
	wrongType := strings.Replace(body, `"integer"`, `"string"`, 1)
	if got := call(h, "node-a", handlerTokenA, "tenant-demo-a", wrongType); got.Code != http.StatusUnprocessableEntity || !strings.Contains(got.Body.String(), `"type_mismatch"`) {
		t.Fatalf("type response: %d %s", got.Code, got.Body.String())
	}
	unknownKey := strings.Replace(body, "logging.max_meta_keys", "unknown.key", 1)
	if got := call(h, "node-a", handlerTokenA, "tenant-demo-a", unknownKey); got.Code != http.StatusUnprocessableEntity || !strings.Contains(got.Body.String(), `"unknown_key"`) {
		t.Fatalf("key response: %d %s", got.Code, got.Body.String())
	}
	if got := call(h, "node-a", handlerTokenA, "tenant-demo-a", `{"contract_version":"v1","tenant_id":"tenant-demo-a","key":"logging.max_meta_keys","expected_type":"integer","extra":true}`); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", got.Code)
	}
}

func auditTestHandler(t *testing.T, repository audit.Repository) http.Handler {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(handlerFixture), func(name string) (string, bool) {
		if name == "TOKEN_A" {
			return handlerTokenA, true
		}
		if name == "TOKEN_B" {
			return handlerTokenB, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewHandlerWithAudit(store, audit.NewService(repository), 16, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type failingAuditRepository struct{}

func (failingAuditRepository) Append(context.Context, audit.Event) error {
	return errors.New("simulated disk failure")
}

type memoryAuditRepository struct {
	events []audit.Event
}

func (m *memoryAuditRepository) Append(_ context.Context, event audit.Event) error {
	m.events = append(m.events, event)
	return nil
}

const validAuditBody = `{"contract_version":"v1","event_id":"evt.demo-1","tenant_id":"tenant-demo-a","service_id":"node-a","actor":"user.demo-01","action":"settings.updated","resource":"setting.max_meta_keys","result":"success","correlation_id":"corr.demo-1","occurred_at":"2026-11-16T12:00:00Z"}`

func auditCall(handler http.Handler, service, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/audit/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestAuditAuthorizationIdentityAndSuccessTimestamps(t *testing.T) {
	repository := &memoryAuditRepository{}
	handler := auditTestHandler(t, repository)
	valid := auditCall(handler, "node-a", handlerTokenA, validAuditBody)
	if valid.Code != http.StatusCreated {
		t.Fatalf("valid event response = %d %s", valid.Code, valid.Body.String())
	}
	if !strings.Contains(valid.Body.String(), `"service_id":"node-a"`) || !strings.Contains(valid.Body.String(), `"received_at":`) {
		t.Fatalf("response should include authenticated service and receipt time: %s", valid.Body.String())
	}
	if len(repository.events) != 1 || repository.events[0].Actor != "user.demo-01" {
		t.Fatalf("event was not appended as declared: %+v", repository.events)
	}
	crossTenantBody := strings.Replace(validAuditBody, `"tenant-demo-a"`, `"tenant-demo-b"`, 1)
	crossTenant := auditCall(handler, "node-a", handlerTokenA, crossTenantBody)
	if crossTenant.Code != http.StatusForbidden || !strings.Contains(crossTenant.Body.String(), "tenant_forbidden") {
		t.Fatalf("cross-tenant event status/body = %d %s", crossTenant.Code, crossTenant.Body.String())
	}
	spoofedService := strings.Replace(validAuditBody, `"service_id":"node-a"`, `"service_id":"node-b"`, 1)
	spoofed := auditCall(handler, "node-a", handlerTokenA, spoofedService)
	if spoofed.Code != http.StatusForbidden || !strings.Contains(spoofed.Body.String(), "service_identity_mismatch") {
		t.Fatalf("spoofed issuer status/body = %d %s", spoofed.Code, spoofed.Body.String())
	}
}

func TestAuditRejectsInactiveTenantAndPreservesHistory(t *testing.T) {
	repository := &memoryAuditRepository{events: []audit.Event{{EventID: "evt.historical", TenantID: "tenant-demo-a"}}}
	inactiveFixture := strings.Replace(handlerFixture, `{"id":"tenant-demo-a","status":"active"}`, `{"id":"tenant-demo-a","status":"inactive"}`, 1)
	store, err := configstore.Load(strings.NewReader(inactiveFixture), func(name string) (string, bool) {
		if name == "TOKEN_A" {
			return handlerTokenA, true
		}
		if name == "TOKEN_B" {
			return handlerTokenB, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAudit(store, audit.NewService(repository), 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := auditCall(h, "node-a", handlerTokenA, validAuditBody)
	if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"tenant_inactive"`) {
		t.Fatalf("inactive audit tenant response = %d %s", got.Code, got.Body.String())
	}
	if len(repository.events) != 1 || repository.events[0].EventID != "evt.historical" {
		t.Fatalf("existing audit history changed after deactivation: %+v", repository.events)
	}
}

func TestAuditUnknownTenantIsNotFound(t *testing.T) {
	h := auditTestHandler(t, &memoryAuditRepository{})
	body := strings.Replace(validAuditBody, `"tenant-demo-a"`, `"tenant-unknown"`, 1)
	got := auditCall(h, "node-a", handlerTokenA, body)
	if got.Code != http.StatusNotFound || !strings.Contains(got.Body.String(), `"tenant_not_found"`) {
		t.Fatalf("unknown audit tenant response = %d %s", got.Code, got.Body.String())
	}
}

func TestAuditInvalidOversizedAndStorageFailure(t *testing.T) {
	handler := auditTestHandler(t, &memoryAuditRepository{})
	invalid := strings.Replace(validAuditBody, `"settings.updated"`, `"free form action"`, 1)
	if got := auditCall(handler, "node-a", handlerTokenA, invalid); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid event status = %d %s", got.Code, got.Body.String())
	}
	if got := auditCall(handler, "node-a", handlerTokenA, strings.Repeat(" ", maxAuditRequestBytes+1)); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized event status = %d", got.Code)
	}
	failing := auditTestHandler(t, failingAuditRepository{})
	if got := auditCall(failing, "node-a", handlerTokenA, validAuditBody); got.Code != http.StatusServiceUnavailable || !strings.Contains(got.Body.String(), "audit_unavailable") {
		t.Fatalf("storage failure status/body = %d %s", got.Code, got.Body.String())
	}
}

func TestAuditUnknownServiceIsUnauthorized(t *testing.T) {
	handler := auditTestHandler(t, &memoryAuditRepository{})
	got := auditCall(handler, "unknown-service", handlerTokenA, validAuditBody)
	if got.Code != http.StatusUnauthorized || !strings.Contains(got.Body.String(), "unauthorized") {
		t.Fatalf("unknown service status/body = %d %s", got.Code, got.Body.String())
	}
}

func TestServiceCapabilitiesGateConfigAndAuditEndpoints(t *testing.T) {
	handler := auditTestHandler(t, &memoryAuditRepository{})
	configBody := `{"contract_version":"v1","tenant_id":"tenant-demo-a","key":"logging.max_meta_keys","expected_type":"integer"}`

	if got := call(handler, "node-a", handlerTokenA, "tenant-demo-a", configBody); got.Code != http.StatusOK {
		t.Fatalf("service with both capabilities could not evaluate config: %d %s", got.Code, got.Body.String())
	}
	if got := auditCall(handler, "node-a", handlerTokenA, validAuditBody); got.Code != http.StatusCreated {
		t.Fatalf("service with both capabilities could not record audit: %d %s", got.Code, got.Body.String())
	}

	if got := call(handler, "node-config", handlerTokenA, "tenant-demo-a", configBody); got.Code != http.StatusOK {
		t.Fatalf("config-only service could not evaluate config: %d %s", got.Code, got.Body.String())
	}
	configOnlyAudit := strings.Replace(validAuditBody, `"service_id":"node-a"`, `"service_id":"node-config"`, 1)
	if got := auditCall(handler, "node-config", handlerTokenA, configOnlyAudit); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"capability_forbidden"`) {
		t.Fatalf("config-only service audit status/body = %d %s", got.Code, got.Body.String())
	}

	if got := call(handler, "node-audit", handlerTokenA, "tenant-demo-a", configBody); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"capability_forbidden"`) {
		t.Fatalf("audit-only service config status/body = %d %s", got.Code, got.Body.String())
	}
	auditOnlyBody := strings.Replace(validAuditBody, `"service_id":"node-a"`, `"service_id":"node-audit"`, 1)
	if got := auditCall(handler, "node-audit", handlerTokenA, auditOnlyBody); got.Code != http.StatusCreated {
		t.Fatalf("audit-only service could not record audit: %d %s", got.Code, got.Body.String())
	}

	noCapabilityAudit := strings.Replace(validAuditBody, `"service_id":"node-a"`, `"service_id":"node-none"`, 1)
	if got := call(handler, "node-none", handlerTokenA, "tenant-demo-a", configBody); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"capability_forbidden"`) {
		t.Fatalf("service without capabilities config status/body = %d %s", got.Code, got.Body.String())
	}
	if got := auditCall(handler, "node-none", handlerTokenA, noCapabilityAudit); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"capability_forbidden"`) {
		t.Fatalf("service without capabilities audit status/body = %d %s", got.Code, got.Body.String())
	}

	// Possessing the operation capability does not grant access to another tenant.
	configCrossTenant := strings.Replace(configBody, "tenant-demo-a", "tenant-demo-b", 1)
	if got := call(handler, "node-config", handlerTokenA, "tenant-demo-b", configCrossTenant); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"tenant_forbidden"`) {
		t.Fatalf("cross-tenant config access status/body = %d %s", got.Code, got.Body.String())
	}
	auditCrossTenant := strings.Replace(auditOnlyBody, `"tenant-demo-a"`, `"tenant-demo-b"`, 1)
	if got := auditCall(handler, "node-audit", handlerTokenA, auditCrossTenant); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), `"tenant_forbidden"`) {
		t.Fatalf("cross-tenant audit access status/body = %d %s", got.Code, got.Body.String())
	}
}

func TestInternalMethodsRejectDuplicatesAndOversizedBodies(t *testing.T) {
	configHandler := testHandler(t)
	getConfig := httptest.NewRequest(http.MethodGet, "/internal/v1/config/evaluate", nil)
	getResponse := httptest.NewRecorder()
	configHandler.ServeHTTP(getResponse, getConfig)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET config status = %d", getResponse.Code)
	}
	configBody := `{"contract_version":"v1","tenant_id":"tenant-demo-a","tenant_id":"tenant-demo-b","key":"logging.max_meta_keys","expected_type":"integer"}`
	if got := call(configHandler, "node-a", handlerTokenA, "", configBody); got.Code != http.StatusBadRequest {
		t.Fatalf("duplicate request field status = %d", got.Code)
	}
	if got := call(configHandler, "node-a", handlerTokenA, "", strings.Repeat(" ", maxRequestBytes+1)); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized config body status = %d", got.Code)
	}

	auditHandler := auditTestHandler(t, &memoryAuditRepository{})
	duplicateAudit := strings.TrimSuffix(validAuditBody, "}") + `,"tenant_id":"tenant-demo-b"}`
	if got := auditCall(auditHandler, "node-a", handlerTokenA, duplicateAudit); got.Code != http.StatusBadRequest {
		t.Fatalf("duplicate audit field status = %d", got.Code)
	}
	if got := auditCall(auditHandler, "node-a", handlerTokenA, `{"contract_version":`); got.Code != http.StatusBadRequest {
		t.Fatalf("malformed audit JSON status = %d", got.Code)
	}
	wrongMethod := httptest.NewRequest(http.MethodGet, "/internal/v1/audit/events", nil)
	wrongMethodResponse := httptest.NewRecorder()
	auditHandler.ServeHTTP(wrongMethodResponse, wrongMethod)
	if wrongMethodResponse.Code != http.StatusMethodNotAllowed || wrongMethodResponse.Header().Get("Allow") != http.MethodPost || !strings.Contains(wrongMethodResponse.Body.String(), `"invalid_request"`) {
		t.Fatalf("GET audit response = %d Allow=%q body=%s", wrongMethodResponse.Code, wrongMethodResponse.Header().Get("Allow"), wrongMethodResponse.Body.String())
	}

	wrongHealthMethod := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	wrongHealthResponse := httptest.NewRecorder()
	auditHandler.ServeHTTP(wrongHealthResponse, wrongHealthMethod)
	if wrongHealthResponse.Code != http.StatusMethodNotAllowed || wrongHealthResponse.Header().Get("Allow") != http.MethodGet || !strings.Contains(wrongHealthResponse.Body.String(), `"invalid_request"`) {
		t.Fatalf("POST health response = %d Allow=%q body=%s", wrongHealthResponse.Code, wrongHealthResponse.Header().Get("Allow"), wrongHealthResponse.Body.String())
	}
}

type blockingAuditRepository struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingAuditRepository) Append(context.Context, audit.Event) error {
	close(b.started)
	<-b.release
	return nil
}

func TestDrainWaitsForAuditWriteAndRejectsNewRequests(t *testing.T) {
	repository := &blockingAuditRepository{started: make(chan struct{}), release: make(chan struct{})}
	handler := auditTestHandler(t, repository)
	drainer := handler.(interface {
		BeginDrain()
		WaitForRequests()
	})
	firstResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResponse <- auditCall(handler, "node-a", handlerTokenA, validAuditBody)
	}()
	<-repository.started
	drainer.BeginDrain()
	newRequest := auditCall(handler, "node-a", handlerTokenA, validAuditBody)
	if newRequest.Code != http.StatusServiceUnavailable {
		t.Fatalf("request during drain status = %d", newRequest.Code)
	}
	waitDone := make(chan struct{})
	go func() {
		drainer.WaitForRequests()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("drain returned before the storage operation finished")
	case <-time.After(25 * time.Millisecond):
	}
	close(repository.release)
	select {
	case response := <-firstResponse:
		if response.Code != http.StatusCreated {
			t.Fatalf("in-flight audit response = %d %s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight audit request did not finish")
	}
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish after the write completed")
	}
}

func TestCorrelationIsGeneratedAndPreserved(t *testing.T) {
	handler := testHandler(t)
	body := `{"contract_version":"v1","tenant_id":"tenant-demo-a","key":"logging.max_meta_keys","expected_type":"integer"}`
	request := func(correlationID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/config/evaluate", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-KAS-Service", "node-a")
		req.Header.Set("Authorization", "Bearer "+handlerTokenA)
		if correlationID != "" {
			req.Header.Set(correlationHeader, correlationID)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	generated := request("")
	generatedID := generated.Header().Get(correlationHeader)
	if generated.Code != http.StatusOK || !validCorrelationID(generatedID) {
		t.Fatalf("missing correlation should generate an ID, status=%d id=%q", generated.Code, generatedID)
	}
	providedID := "corr-client-01"
	preserved := request(providedID)
	if preserved.Code != http.StatusOK || preserved.Header().Get(correlationHeader) != providedID {
		t.Fatalf("provided correlation ID was not preserved: status=%d header=%q", preserved.Code, preserved.Header().Get(correlationHeader))
	}
}

func TestInvalidCorrelationIsRejectedAndReturnsSafeGeneratedID(t *testing.T) {
	handler := testHandler(t)
	for name, values := range map[string][]string{
		"invalid characters": {"bad id"},
		"too long":           {strings.Repeat("a", maxCorrelationIDBytes+1)},
		"multiple headers":   {"corr-one", "corr-two"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			for _, value := range values {
				req.Header.Add(correlationHeader, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			generatedID := response.Header().Get(correlationHeader)
			if response.Code != http.StatusBadRequest || !validCorrelationID(generatedID) {
				t.Fatalf("invalid correlation response status=%d id=%q", response.Code, generatedID)
			}
			for _, value := range values {
				if generatedID == value {
					t.Fatalf("invalid caller value was returned as correlation ID: %q", value)
				}
			}
		})
	}
}

func TestCorrelationAppearsOnErrorsAndSafeLogs(t *testing.T) {
	var logs bytes.Buffer
	store, err := configstore.Load(strings.NewReader(handlerFixture), func(name string) (string, bool) {
		if name == "TOKEN_A" {
			return handlerTokenA, true
		}
		if name == "TOKEN_B" {
			return handlerTokenB, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, 8, slog.New(slog.NewJSONHandler(&logs, nil)))
	correlationID := "corr-error-01"
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/config/evaluate", strings.NewReader(`{"contract_version":"v1","tenant_id":"tenant-demo-a","key":"logging.max_meta_keys","expected_type":"integer"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KAS-Service", "node-a")
	req.Header.Set("Authorization", "Bearer token-that-must-not-be-logged")
	req.Header.Set(correlationHeader, correlationID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized || response.Header().Get(correlationHeader) != correlationID {
		t.Fatalf("error response omitted correlation: status=%d header=%q", response.Code, response.Header().Get(correlationHeader))
	}
	logText := logs.String()
	if !strings.Contains(logText, correlationID) {
		t.Fatalf("structured rejection log omitted correlation ID: %s", logText)
	}
	if strings.Contains(logText, "token-that-must-not-be-logged") || strings.Contains(logText, handlerTokenA) {
		t.Fatalf("structured log contains a credential: %s", logText)
	}
}

type correlationCaptureRepository struct {
	mu       sync.Mutex
	httpIDs  []string
	eventIDs []string
}

func (r *correlationCaptureRepository) Append(ctx context.Context, event audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.httpIDs = append(r.httpIDs, CorrelationID(ctx))
	r.eventIDs = append(r.eventIDs, event.CorrelationID)
	return nil
}

func TestCorrelationIsIsolatedAcrossConcurrentRequestsAndNotAuditCorrelation(t *testing.T) {
	repository := &correlationCaptureRepository{}
	handler := auditTestHandler(t, repository)
	ids := []string{"corr-concurrent-a", "corr-concurrent-b"}
	type result struct {
		id       string
		response *httptest.ResponseRecorder
	}
	results := make(chan result, len(ids))
	for i, id := range ids {
		go func(i int, id string) {
			body := strings.Replace(validAuditBody, "evt.demo-1", fmt.Sprintf("evt.concurrent-%d", i), 1)
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/audit/events", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-KAS-Service", "node-a")
			req.Header.Set("Authorization", "Bearer "+handlerTokenA)
			req.Header.Set(correlationHeader, id)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			results <- result{id: id, response: response}
		}(i, id)
	}
	for range ids {
		got := <-results
		if got.response.Code != http.StatusCreated || got.response.Header().Get(correlationHeader) != got.id {
			t.Fatalf("concurrent response mismatch: status=%d got=%q want=%q", got.response.Code, got.response.Header().Get(correlationHeader), got.id)
		}
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.httpIDs) != 2 || len(repository.eventIDs) != 2 {
		t.Fatalf("unexpected captured calls: HTTP=%v event=%v", repository.httpIDs, repository.eventIDs)
	}
	seen := map[string]bool{}
	for _, id := range repository.httpIDs {
		seen[id] = true
	}
	if !seen[ids[0]] || !seen[ids[1]] {
		t.Fatalf("request contexts mixed or lost correlation IDs: %v", repository.httpIDs)
	}
	for _, eventID := range repository.eventIDs {
		if eventID != "corr.demo-1" {
			t.Fatalf("HTTP correlation overwrote event correlation_id: %q", eventID)
		}
	}
}
