// Package events implements the KAS event outbox: durable publication with
// identity-based idempotency, and delivery to consumers with leases, retries and
// a dead state. Delivery is at-least-once: consumers must be idempotent.
package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"time"

	"kas-core-pilot/internal/strictjson"
)

const (
	ContractVersion = "v1"
	// MaxPayloadBytes is the largest accepted payload (raw JSON bytes).
	MaxPayloadBytes = 16 << 10
	maxPayloadKeys  = 32
)

var (
	ErrInvalidEvent     = errors.New("invalid event")
	ErrUnknownEventType = errors.New("unknown event type")
	ErrPayloadTooLarge  = errors.New("event payload too large")
	ErrServiceMismatch  = errors.New("event service identity mismatch")
	ErrStoreUnavailable = errors.New("event store unavailable")
	// ErrEventConflict means the same (tenant_id, service_id, event_id) was
	// already published with different content.
	ErrEventConflict = errors.New("event conflict")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	eventTypePattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)*$`)
	payloadKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Submission is the v1 publish request from an authenticated service.
type Submission struct {
	ContractVersion string          `json:"contract_version"`
	EventID         string          `json:"event_id"`
	TenantID        string          `json:"tenant_id"`
	ServiceID       string          `json:"service_id"`
	EventType       string          `json:"event_type"`
	CorrelationID   string          `json:"correlation_id"`
	OccurredAt      string          `json:"occurred_at"`
	Payload         json.RawMessage `json:"payload"`
}

// Event is the stored representation. ReceivedAt is set by the KAS server and
// Payload is in canonical form (sorted keys, no whitespace).
type Event struct {
	ContractVersion string          `json:"contract_version"`
	EventID         string          `json:"event_id"`
	TenantID        string          `json:"tenant_id"`
	ServiceID       string          `json:"service_id"`
	EventType       string          `json:"event_type"`
	CorrelationID   string          `json:"correlation_id"`
	OccurredAt      string          `json:"occurred_at"`
	ReceivedAt      string          `json:"received_at"`
	Payload         json.RawMessage `json:"payload"`
}

// ValidateSubmission checks every field and returns the canonical payload.
// Event type declaration is checked separately against the configuration.
func ValidateSubmission(s Submission) (json.RawMessage, error) {
	if s.ContractVersion != ContractVersion || !identifierPattern.MatchString(s.EventID) ||
		!identifierPattern.MatchString(s.TenantID) || !identifierPattern.MatchString(s.ServiceID) ||
		!eventTypePattern.MatchString(s.EventType) || len(s.EventType) > 80 ||
		!identifierPattern.MatchString(s.CorrelationID) || len(s.OccurredAt) > 40 {
		return nil, ErrInvalidEvent
	}
	if _, err := time.Parse(time.RFC3339Nano, s.OccurredAt); err != nil {
		return nil, ErrInvalidEvent
	}
	return CanonicalPayload(s.Payload)
}

// CanonicalPayload validates the payload and returns its canonical form.
//
// The payload must be a JSON object of at most 16 KiB with at most 32 keys.
// Keys are lowercase snake_case. Values may only be identifier-shaped strings,
// 64-bit integers or booleans: no nesting, arrays, nulls or fractions. The
// contract forbids clinical and personal data; this shape check narrows what can
// be sent but cannot detect personal data written in an identifier-like form, so
// emitters remain responsible for sending identifiers only.
func CanonicalPayload(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || strictjson.RejectDuplicateKeys(trimmed) != nil {
		return nil, ErrInvalidEvent
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, ErrInvalidEvent
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrInvalidEvent
	}
	if len(object) > maxPayloadKeys {
		return nil, ErrInvalidEvent
	}
	canonical := make(map[string]any, len(object))
	for key, value := range object {
		if !payloadKeyPattern.MatchString(key) {
			return nil, ErrInvalidEvent
		}
		switch v := value.(type) {
		case string:
			if !identifierPattern.MatchString(v) {
				return nil, ErrInvalidEvent
			}
			canonical[key] = v
		case bool:
			canonical[key] = v
		case json.Number:
			n, err := strconv.ParseInt(v.String(), 10, 64)
			if err != nil {
				return nil, ErrInvalidEvent
			}
			canonical[key] = n
		default:
			return nil, ErrInvalidEvent
		}
	}
	out, err := json.Marshal(canonical) // map keys are marshalled in sorted order
	if err != nil {
		return nil, ErrInvalidEvent
	}
	return out, nil
}

// ContentHash identifies the emitter-supplied content: everything except the
// identity (tenant, service, event_id) and the server-assigned ReceivedAt.
func (e Event) ContentHash() string {
	payload := e.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	data, _ := json.Marshal(struct {
		ContractVersion string          `json:"v"`
		EventType       string          `json:"t"`
		CorrelationID   string          `json:"c"`
		OccurredAt      string          `json:"o"`
		Payload         json.RawMessage `json:"p"`
	}{e.ContractVersion, e.EventType, e.CorrelationID, e.OccurredAt, payload})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DuplicateError reports that an identical event was already published under the
// same identity. Nothing new was written; Stored is the event as first recorded.
type DuplicateError struct{ Stored Event }

func (*DuplicateError) Error() string { return "event already published" }
