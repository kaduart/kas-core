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
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
)

const pullHandlerFixture = `{"contract_version":"v1","config_version":"fixture-pull","tenant_registry":[{"id":"tenant-demo-a","status":"active"},{"id":"tenant-demo-b","status":"active"},{"id":"tenant-demo-c","status":"inactive"}],
"key_schema":{"k.v":{"type":"integer"}},"tenants":{"tenant-demo-a":{"k.v":1}},
"event_types":["settings.updated","settings.checked"],
"event_consumers":{
 "pull-a":{"event_types":["settings.updated"],"tenants":["tenant-demo-a","tenant-demo-b","tenant-demo-c"],"service":"node-a"},
 "pull-b":{"event_types":["settings.checked"],"tenants":["tenant-demo-b"],"service":"node-b"},
 "in-process":{"event_types":["settings.updated"],"tenants":["tenant-demo-a"]}},
"callers":{
 "node-a":{"token_env":"TOKEN_A","tenants":["tenant-demo-a","tenant-demo-c"],"capabilities":["events:consume"]},
 "node-b":{"token_env":"TOKEN_B","tenants":["tenant-demo-b"],"capabilities":["events:consume"]},
 "node-pub":{"token_env":"TOKEN_A","tenants":["tenant-demo-a"],"capabilities":["events:publish"]}}}`

type fakePullStore struct {
	claims   []claimCall
	delivers []events.Delivery
	ackOut   events.SettleOutcome
	nackOut  events.SettleOutcome
	err      error
	acks     []settleCall
}

type claimCall struct {
	token, consumer string
	tenants, types  []string
	lease           time.Duration
	limit           int
}

type settleCall struct {
	consumer string
	id       int64
	attempt  int
	token    string
	dead     bool
	code     string
}

func (f *fakePullStore) ClaimFor(_ context.Context, token, consumer string, tenants, types []string, _ time.Time, lease time.Duration, _ int, limit int) ([]events.Delivery, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.claims = append(f.claims, claimCall{token, consumer, tenants, types, lease, limit})
	return f.delivers, nil
}
func (f *fakePullStore) Ack(_ context.Context, consumer string, id int64, attempt int, token string, _ time.Time) (events.SettleOutcome, error) {
	f.acks = append(f.acks, settleCall{consumer: consumer, id: id, attempt: attempt, token: token})
	return f.ackOut, f.err
}
func (f *fakePullStore) Nack(_ context.Context, consumer string, id int64, attempt int, token string, _, _ time.Time, dead bool, code string) (events.SettleOutcome, error) {
	f.acks = append(f.acks, settleCall{consumer, id, attempt, token, dead, code})
	return f.nackOut, f.err
}

func pullTestHandler(t *testing.T, fake *fakePullStore) http.Handler {
	t.Helper()
	store, err := configstore.Load(strings.NewReader(pullHandlerFixture), func(name string) (string, bool) {
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
	if fake != nil {
		h.EnablePull(events.NewPuller(fake, events.PullConfig{MaxAttempts: 3, Jitter: func() float64 { return 0.5 }}))
	}
	return h
}

func pullCall(h http.Handler, path, service, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/events/deliveries/"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KAS-Service", service)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const claimBody = `{"contract_version":"v1","consumer_id":"pull-a"}`
const tokenHex = "0123456789abcdef0123456789abcdef"
const ackBody = `{"contract_version":"v1","consumer_id":"pull-a","delivery_id":7,"attempt":2,"lease_token":"` + tokenHex + `"}`

func TestClaimReturnsDeliveriesWithinTheConsumerScope(t *testing.T) {
	fake := &fakePullStore{delivers: []events.Delivery{{ID: 7, ConsumerID: "pull-a", Attempt: 2, Event: events.Event{
		ContractVersion: "v1", EventID: "evt.x", TenantID: "tenant-demo-a", ServiceID: "svc", EventType: "settings.updated",
		CorrelationID: "c", OccurredAt: "2026-11-17T12:00:00Z", ReceivedAt: "2026-11-17T12:00:01Z", Payload: []byte(`{"version":3}`)}}}}
	rec := pullCall(pullTestHandler(t, fake), "claim", "node-a", handlerTokenA, `{"contract_version":"v1","consumer_id":"pull-a","max_deliveries":20,"lease_seconds":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"delivery_id":7`) || !strings.Contains(body, `"attempt":2`) || !strings.Contains(body, `"lease_token":"`) ||
		!strings.Contains(body, `"lease_expires_at":"`) || !strings.Contains(body, `"payload":{"version":3}`) {
		t.Fatalf("body = %s", body)
	}
	c := fake.claims[0]
	// consumer lists a, b, c; service has a and c; c is inactive -> only tenant A.
	if strings.Join(c.tenants, ",") != "tenant-demo-a" || strings.Join(c.types, ",") != "settings.updated" ||
		c.consumer != "pull-a" || c.limit != 20 || c.lease != 30*time.Second || len(c.token) != 32 {
		t.Fatalf("store received %+v", c)
	}
}

func TestClaimWithNothingDueReturnsAnEmptyList(t *testing.T) {
	rec := pullCall(pullTestHandler(t, &fakePullStore{}), "claim", "node-a", handlerTokenA, claimBody)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deliveries":[]`) {
		t.Fatalf("empty claim = %d %s", rec.Code, rec.Body.String())
	}
}

func TestPullAuthorizationRules(t *testing.T) {
	fake := &fakePullStore{}
	h := pullTestHandler(t, fake)
	cases := []struct {
		name                 string
		service, token, body string
		code                 int
		text                 string
	}{
		{"wrong token", "node-a", handlerTokenB, claimBody, 401, "unauthorized"},
		{"unknown service", "ghost", handlerTokenA, claimBody, 401, "unauthorized"},
		{"service without consume capability", "node-pub", handlerTokenA, claimBody, 403, "capability_forbidden"},
		{"consumer bound to another service", "node-b", handlerTokenB, claimBody, 403, "consumer_forbidden"},
		{"unknown consumer", "node-a", handlerTokenA, `{"contract_version":"v1","consumer_id":"pull-ghost"}`, 403, "consumer_forbidden"},
		{"in-process consumer", "node-a", handlerTokenA, `{"contract_version":"v1","consumer_id":"in-process"}`, 403, "consumer_forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"claim", "ack", "nack"} {
				body := tc.body
				if path != "claim" {
					body = strings.Replace(tc.body, `}`, `,"delivery_id":7,"attempt":1,"lease_token":"`+tokenHex+`"}`, 1)
				}
				rec := pullCall(h, path, tc.service, tc.token, body)
				if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.text) {
					t.Fatalf("%s: %d %s, want %d %s", path, rec.Code, rec.Body.String(), tc.code, tc.text)
				}
			}
		})
	}
	if len(fake.claims) != 0 || len(fake.acks) != 0 {
		t.Fatal("an unauthorized request must never reach the store")
	}
	// Unknown and foreign consumers are indistinguishable.
	a := pullCall(h, "claim", "node-a", handlerTokenA, `{"contract_version":"v1","consumer_id":"pull-ghost"}`).Body.String()
	b := pullCall(h, "claim", "node-a", handlerTokenA, `{"contract_version":"v1","consumer_id":"pull-b"}`).Body.String()
	if a != b {
		t.Fatalf("responses differ: %s vs %s", a, b)
	}
}

func TestPullRequestValidation(t *testing.T) {
	h := pullTestHandler(t, &fakePullStore{})
	cases := map[string]struct {
		path, body string
		code       int
	}{
		"batch above 50":     {"claim", `{"contract_version":"v1","consumer_id":"pull-a","max_deliveries":51}`, 400},
		"negative batch":     {"claim", `{"contract_version":"v1","consumer_id":"pull-a","max_deliveries":-1}`, 400},
		"lease below 5s":     {"claim", `{"contract_version":"v1","consumer_id":"pull-a","lease_seconds":4}`, 400},
		"lease above 300s":   {"claim", `{"contract_version":"v1","consumer_id":"pull-a","lease_seconds":301}`, 400},
		"wrong contract":     {"claim", `{"contract_version":"v2","consumer_id":"pull-a"}`, 400},
		"missing consumer":   {"claim", `{"contract_version":"v1"}`, 400},
		"unknown field":      {"claim", `{"contract_version":"v1","consumer_id":"pull-a","tenant_id":"tenant-demo-b"}`, 400},
		"duplicate key":      {"claim", `{"contract_version":"v1","consumer_id":"pull-a","consumer_id":"pull-b"}`, 400},
		"ack bad token":      {"ack", `{"contract_version":"v1","consumer_id":"pull-a","delivery_id":7,"attempt":1,"lease_token":"nope"}`, 400},
		"ack zero id":        {"ack", `{"contract_version":"v1","consumer_id":"pull-a","delivery_id":0,"attempt":1,"lease_token":"` + tokenHex + `"}`, 400},
		"ack zero attempt":   {"ack", `{"contract_version":"v1","consumer_id":"pull-a","delivery_id":7,"attempt":0,"lease_token":"` + tokenHex + `"}`, 400},
		"nack unknown field": {"nack", `{"contract_version":"v1","consumer_id":"pull-a","delivery_id":7,"attempt":1,"lease_token":"` + tokenHex + `","reason":"free text"}`, 400},
		"body over 4 KiB":    {"claim", `{"contract_version":"v1","consumer_id":"pull-a","x":"` + strings.Repeat("a", 5000) + `"}`, 413},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := pullCall(h, tc.path, "node-a", handlerTokenA, tc.body); rec.Code != tc.code {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body.String(), tc.code)
			}
		})
	}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/internal/v1/events/deliveries/claim", nil))
	if get.Code != http.StatusMethodNotAllowed || get.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET = %d", get.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/events/deliveries/claim", strings.NewReader(claimBody))
	req.Header.Set("X-KAS-Service", "node-a")
	req.Header.Set("Authorization", "Bearer "+handlerTokenA)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type = %d", rec.Code)
	}
}

func TestAckAndNackOutcomes(t *testing.T) {
	fake := &fakePullStore{}
	h := pullTestHandler(t, fake)
	if rec := pullCall(h, "ack", "node-a", handlerTokenA, ackBody); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"delivered"`) {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body.String())
	}
	if c := fake.acks[0]; c.consumer != "pull-a" || c.id != 7 || c.attempt != 2 || c.token != tokenHex {
		t.Fatalf("store received %+v", c)
	}
	fake.ackOut = events.LeaseLost
	if rec := pullCall(h, "ack", "node-a", handlerTokenA, ackBody); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "lease_lost") {
		t.Fatalf("lost lease = %d %s", rec.Code, rec.Body.String())
	}
	nack := strings.Replace(ackBody, `"attempt":2`, `"attempt":1`, 1)
	rec := pullCall(h, "nack", "node-a", handlerTokenA, nack)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"pending"`) || !strings.Contains(rec.Body.String(), `"next_attempt_at":"`) {
		t.Fatalf("nack = %d %s", rec.Code, rec.Body.String())
	}
	if last := fake.acks[len(fake.acks)-1]; last.dead || last.code != "consumer_nack" {
		t.Fatalf("store received %+v", last)
	}
	atMax := strings.Replace(ackBody, `"attempt":2`, `"attempt":3`, 1)
	rec = pullCall(h, "nack", "node-a", handlerTokenA, atMax)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"dead"`) || strings.Contains(rec.Body.String(), "next_attempt_at") {
		t.Fatalf("nack at max attempts = %d %s", rec.Code, rec.Body.String())
	}
	if last := fake.acks[len(fake.acks)-1]; !last.dead || last.code != "max_attempts" {
		t.Fatalf("store received %+v", last)
	}
	permanent := strings.Replace(nack, `"lease_token"`, `"permanent":true,"lease_token"`, 1)
	rec = pullCall(h, "nack", "node-a", handlerTokenA, permanent)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"dead"`) {
		t.Fatalf("permanent nack = %d %s", rec.Code, rec.Body.String())
	}
	if last := fake.acks[len(fake.acks)-1]; !last.dead || last.code != "consumer_permanent" {
		t.Fatalf("store received %+v", last)
	}
	fake.nackOut = events.LeaseLost
	if rec := pullCall(h, "nack", "node-a", handlerTokenA, nack); rec.Code != http.StatusConflict {
		t.Fatalf("nack with lost lease = %d", rec.Code)
	}
}

func TestPullStorageFailureAndDisabledStates(t *testing.T) {
	failing := pullTestHandler(t, &fakePullStore{err: errors.New("connect db.internal: refused")})
	for _, path := range []string{"claim", "ack"} {
		body := claimBody
		if path == "ack" {
			body = ackBody
		}
		rec := pullCall(failing, path, "node-a", handlerTokenA, body)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "events_unavailable") || strings.Contains(rec.Body.String(), "db.internal") {
			t.Fatalf("%s on failing store = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := pullCall(pullTestHandler(t, nil), "claim", "node-a", handlerTokenA, claimBody)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "events_unavailable") {
		t.Fatalf("disabled pull = %d %s", rec.Code, rec.Body.String())
	}
}
