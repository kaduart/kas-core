package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
)

const eventsHandlerFixture = `{"contract_version":"v1","config_version":"fixture-events","tenant_registry":[{"id":"tenant-demo-a","status":"active"},{"id":"tenant-demo-b","status":"active"}],"key_schema":{"logging.max_meta_keys":{"type":"integer","min":1,"max":10}},"tenants":{"tenant-demo-a":{"logging.max_meta_keys":10}},"event_types":["settings.updated"],"event_consumers":{"consumer-a":{"event_types":["settings.updated"],"tenants":["tenant-demo-a"]}},"callers":{"node-a":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["config:read","audit:write","events:publish"]},"node-audit":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["audit:write"]},"node-b":{"token_env":"TOKEN_B","tenants":["tenant-demo-b"],"capabilities":["events:publish"]}}}`

const validEventBody = `{"contract_version":"v1","event_id":"evt.http-1","tenant_id":"tenant-demo-a","service_id":"node-a","event_type":"settings.updated","correlation_id":"corr.http-1","occurred_at":"2026-11-17T12:00:00Z","payload":{"version":3,"setting_key":"logging.max_meta_keys"}}`

type scriptedEventsRepository struct {
	publishErr error
	readyErr   error
	published  []events.Event
	consumers  [][]string
}

func (r *scriptedEventsRepository) Publish(_ context.Context, e events.Event, consumers []string) error {
	if r.publishErr != nil {
		return r.publishErr
	}
	r.published = append(r.published, e)
	r.consumers = append(r.consumers, consumers)
	return nil
}
func (r *scriptedEventsRepository) Ready(context.Context) error { return r.readyErr }

func eventsTestHandler(t *testing.T, repo events.Repository) http.Handler {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(eventsHandlerFixture), func(name string) (string, bool) {
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
	if repo != nil {
		h.EnableEvents(events.NewService(repo, store))
	}
	return h
}

func eventCall(handler http.Handler, service, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestPublishEventCreatesWithCanonicalPayloadAndFansOutToDeclaredConsumers(t *testing.T) {
	repo := &scriptedEventsRepository{}
	got := eventCall(eventsTestHandler(t, repo), "node-a", handlerTokenA, validEventBody)
	if got.Code != http.StatusCreated {
		t.Fatalf("publish status = %d %s", got.Code, got.Body.String())
	}
	if !strings.Contains(got.Body.String(), `"payload":{"setting_key":"logging.max_meta_keys","version":3}`) ||
		!strings.Contains(got.Body.String(), `"service_id":"node-a"`) || !strings.Contains(got.Body.String(), `"received_at":`) {
		t.Fatalf("unexpected body: %s", got.Body.String())
	}
	if len(repo.consumers) != 1 || strings.Join(repo.consumers[0], ",") != "consumer-a" {
		t.Fatalf("consumers = %v", repo.consumers)
	}
}

func TestPublishEventAuthorizationRules(t *testing.T) {
	h := eventsTestHandler(t, &scriptedEventsRepository{})
	if got := eventCall(h, "node-a", handlerTokenB, validEventBody); got.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", got.Code)
	}
	if got := eventCall(h, "node-audit", handlerTokenA, strings.Replace(validEventBody, `"node-a"`, `"node-audit"`, 1)); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "capability_forbidden") {
		t.Fatalf("service without events:publish = %d %s", got.Code, got.Body.String())
	}
	cross := strings.Replace(validEventBody, `"tenant-demo-a"`, `"tenant-demo-b"`, 1)
	if got := eventCall(h, "node-a", handlerTokenA, cross); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "tenant_forbidden") {
		t.Fatalf("cross-tenant = %d %s", got.Code, got.Body.String())
	}
	spoof := strings.Replace(validEventBody, `"service_id":"node-a"`, `"service_id":"node-b"`, 1)
	if got := eventCall(h, "node-a", handlerTokenA, spoof); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "service_identity_mismatch") {
		t.Fatalf("spoofed issuer = %d %s", got.Code, got.Body.String())
	}
}

func TestPublishEventValidationAndLimits(t *testing.T) {
	h := eventsTestHandler(t, &scriptedEventsRepository{})
	cases := map[string]struct {
		body string
		code int
		text string
	}{
		"unknown type":    {strings.Replace(validEventBody, "settings.updated", "settings.deleted", 1), http.StatusUnprocessableEntity, "unknown_event_type"},
		"nested payload":  {strings.Replace(validEventBody, `{"version":3,"setting_key":"logging.max_meta_keys"}`, `{"a":{"b":1}}`, 1), http.StatusBadRequest, "invalid_event"},
		"free text":       {strings.Replace(validEventBody, `{"version":3,"setting_key":"logging.max_meta_keys"}`, `{"name":"Maria da Silva"}`, 1), http.StatusBadRequest, "invalid_event"},
		"missing payload": {strings.Replace(validEventBody, `,"payload":{"version":3,"setting_key":"logging.max_meta_keys"}`, ``, 1), http.StatusBadRequest, "invalid_event"},
		"unknown field":   {strings.Replace(validEventBody, `"payload"`, `"extra":1,"payload"`, 1), http.StatusBadRequest, "invalid_request"},
		"payload 17 KiB":  {strings.Replace(validEventBody, `{"version":3,"setting_key":"logging.max_meta_keys"}`, `{"a":"`+strings.Repeat("b", 17<<10)+`"}`, 1), http.StatusRequestEntityTooLarge, "payload_too_large"},
		"body over limit": {strings.Replace(validEventBody, `{"version":3,"setting_key":"logging.max_meta_keys"}`, `{"a":"`+strings.Repeat("b", 25<<10)+`"}`, 1), http.StatusRequestEntityTooLarge, "payload_too_large"},
		"duplicate key":   {strings.Replace(validEventBody, `{"version":3,"setting_key":"logging.max_meta_keys"}`, `{"version":3,"version":4}`, 1), http.StatusBadRequest, "invalid_request"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := eventCall(h, "node-a", handlerTokenA, tc.body)
			if got.Code != tc.code || !strings.Contains(got.Body.String(), tc.text) {
				t.Fatalf("response = %d %s, want %d %s", got.Code, got.Body.String(), tc.code, tc.text)
			}
		})
	}
	wrongMethod := httptest.NewRecorder()
	h.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodGet, "/internal/v1/events", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed || wrongMethod.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET = %d", wrongMethod.Code)
	}
}

func TestPublishEventIdempotencyAndConflict(t *testing.T) {
	stored := events.Event{
		ContractVersion: "v1", EventID: "evt.http-1", TenantID: "tenant-demo-a", ServiceID: "node-a", EventType: "settings.updated",
		CorrelationID: "corr.http-1", OccurredAt: "2026-11-17T12:00:00Z", ReceivedAt: "2026-11-17T10:00:00Z",
		Payload: []byte(`{"setting_key":"logging.max_meta_keys","version":3}`),
	}
	dup := eventCall(eventsTestHandler(t, &scriptedEventsRepository{publishErr: &events.DuplicateError{Stored: stored}}), "node-a", handlerTokenA, validEventBody)
	if dup.Code != http.StatusOK || !strings.Contains(dup.Body.String(), `"received_at":"2026-11-17T10:00:00Z"`) {
		t.Fatalf("identical resend = %d %s", dup.Code, dup.Body.String())
	}
	conflict := eventCall(eventsTestHandler(t, &scriptedEventsRepository{publishErr: events.ErrEventConflict}), "node-a", handlerTokenA, validEventBody)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "event_conflict") {
		t.Fatalf("conflict = %d %s", conflict.Code, conflict.Body.String())
	}
	down := eventCall(eventsTestHandler(t, &scriptedEventsRepository{publishErr: errors.New("connect db.internal: refused")}), "node-a", handlerTokenA, validEventBody)
	if down.Code != http.StatusServiceUnavailable || !strings.Contains(down.Body.String(), "events_unavailable") || strings.Contains(down.Body.String(), "db.internal") {
		t.Fatalf("storage failure = %d %s", down.Code, down.Body.String())
	}
}

func TestPublishEventUnavailableWhenNotEnabled(t *testing.T) {
	got := eventCall(eventsTestHandler(t, nil), "node-a", handlerTokenA, validEventBody)
	if got.Code != http.StatusServiceUnavailable || !strings.Contains(got.Body.String(), "events_unavailable") {
		t.Fatalf("disabled events = %d %s", got.Code, got.Body.String())
	}
}

func TestReadyzChecksEventStorage(t *testing.T) {
	get := func(h http.Handler) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return r
	}
	if got := get(eventsTestHandler(t, &scriptedEventsRepository{})); got.Code != http.StatusOK {
		t.Fatalf("ready = %d", got.Code)
	}
	failing := get(eventsTestHandler(t, &scriptedEventsRepository{readyErr: errors.New("password=secret")}))
	if failing.Code != http.StatusServiceUnavailable || !strings.Contains(failing.Body.String(), "events_unavailable") || strings.Contains(failing.Body.String(), "secret") {
		t.Fatalf("failing = %d %s", failing.Code, failing.Body.String())
	}
	if got := get(eventsTestHandler(t, nil)); got.Code != http.StatusOK {
		t.Fatalf("events disabled must not affect readiness: %d", got.Code)
	}
}
