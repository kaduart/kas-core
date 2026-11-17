package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testSubmission(id string) Submission {
	return Submission{
		ContractVersion: ContractVersion, EventID: id, TenantID: "tenant-demo-a", ServiceID: "fono-node-local",
		EventType: "settings.updated", CorrelationID: "corr.demo-01", OccurredAt: "2026-11-17T12:00:00.123456789Z",
		Payload: json.RawMessage(`{"setting_key":"logging.max_meta_keys","version":3,"enabled":true}`),
	}
}

func TestCanonicalPayloadAcceptsIdentifiersIntegersAndBooleans(t *testing.T) {
	got, err := CanonicalPayload(json.RawMessage(` { "version" : 3 , "enabled":true,  "setting_key":"setting.x" } `))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"enabled":true,"setting_key":"setting.x","version":3}`; string(got) != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
	if _, err := CanonicalPayload(json.RawMessage(`{}`)); err != nil {
		t.Fatalf("empty object must be accepted: %v", err)
	}
}

func TestCanonicalPayloadRejectsShapesThatCouldCarryPersonalData(t *testing.T) {
	long := strings.Repeat("a", 129)
	manyKeys := "{"
	for i := 0; i < 33; i++ {
		if i > 0 {
			manyKeys += ","
		}
		manyKeys += fmt.Sprintf(`"k%d":1`, i)
	}
	manyKeys += "}"
	cases := map[string]string{
		"not an object":         `["a"]`,
		"null":                  `null`,
		"string":                `"abc"`,
		"empty":                 ``,
		"nested object":         `{"a":{"b":1}}`,
		"array value":           `{"a":["x"]}`,
		"null value":            `{"a":null}`,
		"float":                 `{"a":1.5}`,
		"exponent":              `{"a":1e3}`,
		"integer overflow":      `{"a":9223372036854775808}`,
		"free text with spaces": `{"a":"Maria da Silva"}`,
		"email":                 `{"a":"maria@example.com"}`,
		"empty string":          `{"a":""}`,
		"string too long":       `{"a":"` + long + `"}`,
		"uppercase key":         `{"Name":"x"}`,
		"key with dash":         `{"a-b":"x"}`,
		"duplicate keys":        `{"a":1,"a":2}`,
		"trailing value":        `{"a":1} {"b":2}`,
		"too many keys":         manyKeys,
		"unterminated":          `{"a":1`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalPayload(json.RawMessage(raw)); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("payload %q error = %v, want ErrInvalidEvent", raw, err)
			}
		})
	}
	big := `{"a":"` + strings.Repeat("b", MaxPayloadBytes) + `"}`
	if _, err := CanonicalPayload(json.RawMessage(big)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("payload above 16 KiB error = %v, want ErrPayloadTooLarge", err)
	}
}

func TestContentHashIgnoresKeyOrderAndReceivedAtButNotContent(t *testing.T) {
	sa, sb := testSubmission("e1"), testSubmission("e1")
	sb.Payload = json.RawMessage(`{ "enabled":true, "version":3, "setting_key":"logging.max_meta_keys" }`)
	pa, _ := ValidateSubmission(sa)
	pb, _ := ValidateSubmission(sb)
	a := Event{ContractVersion: "v1", EventType: "settings.updated", CorrelationID: "c", OccurredAt: "2026-11-17T12:00:00Z", ReceivedAt: "x", Payload: pa}
	b := a
	b.ReceivedAt = "y"
	b.Payload = pb
	if a.ContentHash() != b.ContentHash() {
		t.Fatal("hash must ignore received_at and payload key order")
	}
	for name, mutate := range map[string]func(*Event){
		"type":        func(e *Event) { e.EventType = "settings.checked" },
		"correlation": func(e *Event) { e.CorrelationID = "other" },
		"occurred_at": func(e *Event) { e.OccurredAt = "2026-11-17T12:00:00+00:00" },
		"payload":     func(e *Event) { e.Payload = json.RawMessage(`{"version":4}`) },
	} {
		c := a
		mutate(&c)
		if c.ContentHash() == a.ContentHash() {
			t.Fatalf("hash must change when %s changes", name)
		}
	}
}

func TestValidateSubmissionRejectsBadFields(t *testing.T) {
	if _, err := ValidateSubmission(testSubmission("evt.ok-1")); err != nil {
		t.Fatalf("valid submission rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Submission){
		"contract":        func(s *Submission) { s.ContractVersion = "v2" },
		"event id":        func(s *Submission) { s.EventID = "bad id" },
		"tenant":          func(s *Submission) { s.TenantID = "" },
		"service":         func(s *Submission) { s.ServiceID = "a b" },
		"event type":      func(s *Submission) { s.EventType = "Settings Updated" },
		"correlation":     func(s *Submission) { s.CorrelationID = "" },
		"occurred_at":     func(s *Submission) { s.OccurredAt = "yesterday" },
		"missing payload": func(s *Submission) { s.Payload = nil },
	} {
		s := testSubmission("evt.bad-1")
		mutate(&s)
		if _, err := ValidateSubmission(s); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s: error = %v, want ErrInvalidEvent", name, err)
		}
	}
}

// --- service with a fake repository ---

type fakeRegistry struct {
	types     map[string]bool
	consumers []string
	calls     [][2]string
}

func (r *fakeRegistry) EventTypeDeclared(t string) bool { return r.types[t] }
func (r *fakeRegistry) ConsumersFor(tenant, eventType string) []string {
	r.calls = append(r.calls, [2]string{tenant, eventType})
	return r.consumers
}

type fakeRepository struct {
	err       error
	readyErr  error
	published []Event
	consumers [][]string
}

func (r *fakeRepository) Publish(_ context.Context, e Event, consumers []string) error {
	if r.err != nil {
		return r.err
	}
	r.published = append(r.published, e)
	r.consumers = append(r.consumers, consumers)
	return nil
}
func (r *fakeRepository) Ready(context.Context) error { return r.readyErr }

func TestServicePublishFlow(t *testing.T) {
	registry := &fakeRegistry{types: map[string]bool{"settings.updated": true}, consumers: []string{"consumer-a", "consumer-b"}}
	repo := &fakeRepository{}
	service := NewService(repo, registry)
	service.now = func() time.Time { return time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC) }

	result, err := service.Publish(context.Background(), "fono-node-local", testSubmission("evt.svc-1"))
	if err != nil || result.Duplicate {
		t.Fatalf("publish = %+v, %v", result, err)
	}
	if result.Event.ReceivedAt != "2026-11-17T15:00:00Z" || result.Event.ServiceID != "fono-node-local" {
		t.Fatalf("server must set received_at and authenticated service: %+v", result.Event)
	}
	if string(result.Event.Payload) != `{"enabled":true,"setting_key":"logging.max_meta_keys","version":3}` {
		t.Fatalf("payload must be stored canonically: %s", result.Event.Payload)
	}
	if len(repo.consumers) != 1 || strings.Join(repo.consumers[0], ",") != "consumer-a,consumer-b" {
		t.Fatalf("consumers passed to repository = %v", repo.consumers)
	}
	if registry.calls[0] != [2]string{"tenant-demo-a", "settings.updated"} {
		t.Fatalf("registry asked for %v", registry.calls[0])
	}

	if _, err := service.Publish(context.Background(), "other-service", testSubmission("evt.svc-2")); !errors.Is(err, ErrServiceMismatch) {
		t.Fatalf("service mismatch error = %v", err)
	}
	unknown := testSubmission("evt.svc-3")
	unknown.EventType = "settings.deleted"
	if _, err := service.Publish(context.Background(), "fono-node-local", unknown); !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("unknown type error = %v", err)
	}
	bad := testSubmission("evt.svc-4")
	bad.Payload = json.RawMessage(`{"name":"Maria da Silva"}`)
	if _, err := service.Publish(context.Background(), "fono-node-local", bad); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid payload error = %v", err)
	}
	if len(repo.published) != 1 {
		t.Fatalf("rejected events must not reach the repository: %d published", len(repo.published))
	}
}

func TestServiceMapsRepositoryOutcomes(t *testing.T) {
	registry := &fakeRegistry{types: map[string]bool{"settings.updated": true}}
	stored := Event{EventID: "evt.dup-1", ReceivedAt: "2026-11-17T10:00:00Z"}
	cases := []struct {
		name          string
		repoErr       error
		wantErr       error
		wantDuplicate bool
	}{
		{"duplicate", &DuplicateError{Stored: stored}, nil, true},
		{"conflict", ErrEventConflict, ErrEventConflict, false},
		{"unavailable", errors.New("connection refused to db.internal"), ErrStoreUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := NewService(&fakeRepository{err: tc.repoErr}, registry)
			result, err := service.Publish(context.Background(), "fono-node-local", testSubmission("evt.dup-1"))
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if result.Duplicate != tc.wantDuplicate || (tc.wantDuplicate && result.Event.ReceivedAt != stored.ReceivedAt) {
				t.Fatalf("result = %+v", result)
			}
			if err != nil && strings.Contains(err.Error(), "db.internal") {
				t.Fatalf("error leaks storage detail: %v", err)
			}
		})
	}
}

func TestServiceReadiness(t *testing.T) {
	registry := &fakeRegistry{}
	if err := NewService(&fakeRepository{}, registry).Ready(context.Background()); err != nil {
		t.Fatalf("ready repository: %v", err)
	}
	if err := NewService(&fakeRepository{readyErr: errors.New("down")}, registry).Ready(context.Background()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("failing repository must not be ready: %v", err)
	}
	var nilService *Service
	if err := nilService.Ready(context.Background()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("nil service must not be ready: %v", err)
	}
}
