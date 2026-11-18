package audit

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const ContractVersion = "v1"

var (
	ErrInvalidEvent     = errors.New("invalid audit event")
	ErrServiceMismatch  = errors.New("audit service identity mismatch")
	ErrStoreUnavailable = errors.New("audit store unavailable")
	// ErrEventConflict means the same (tenant_id, service_id, event_id) was
	// already stored with different content.
	ErrEventConflict  = errors.New("audit event conflict")
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	actorPattern      = regexp.MustCompile(`^(user|service|system)\.[a-z0-9][a-z0-9._-]{0,95}$`)
	actionPattern     = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)*$`)
)

// Submission is the v1 request declaration from an authenticated service.
// Actor is an issuer-supplied reference, not a KAS-verified user identity.
type Submission struct {
	ContractVersion string `json:"contract_version"`
	EventID         string `json:"event_id"`
	TenantID        string `json:"tenant_id"`
	ServiceID       string `json:"service_id"`
	Actor           string `json:"actor"`
	Action          string `json:"action"`
	Resource        string `json:"resource"`
	Result          string `json:"result"`
	CorrelationID   string `json:"correlation_id"`
	OccurredAt      string `json:"occurred_at"`
	// EntityType and EntityID optionally identify the entity the action was
	// about, so records can be read back by entity. Both or neither; identifiers
	// only, never names or other personal data.
	EntityType string `json:"entity_type,omitempty"`
	EntityID   string `json:"entity_id,omitempty"`
}

// Event is the persisted representation. ReceivedAt is set by the KAS server.
type Event struct {
	ContractVersion string `json:"contract_version"`
	EventID         string `json:"event_id"`
	TenantID        string `json:"tenant_id"`
	ServiceID       string `json:"service_id"`
	Actor           string `json:"actor"`
	Action          string `json:"action"`
	Resource        string `json:"resource"`
	Result          string `json:"result"`
	CorrelationID   string `json:"correlation_id"`
	OccurredAt      string `json:"occurred_at"`
	ReceivedAt      string `json:"received_at"`
	EntityType      string `json:"entity_type,omitempty"`
	EntityID        string `json:"entity_id,omitempty"`
}

func ValidateSubmission(s Submission) error {
	if s.ContractVersion != ContractVersion || !identifierPattern.MatchString(s.EventID) ||
		!identifierPattern.MatchString(s.TenantID) || !identifierPattern.MatchString(s.ServiceID) ||
		!actorPattern.MatchString(s.Actor) || !actionPattern.MatchString(s.Action) ||
		!identifierPattern.MatchString(s.Resource) || !identifierPattern.MatchString(s.CorrelationID) {
		return ErrInvalidEvent
	}
	switch s.Result {
	case "success", "failure", "denied":
	default:
		return ErrInvalidEvent
	}
	if len(s.OccurredAt) > 40 {
		return ErrInvalidEvent
	}
	if (s.EntityType == "") != (s.EntityID == "") {
		return ErrInvalidEvent
	}
	if s.EntityType != "" && (!identifierPattern.MatchString(s.EntityType) || !identifierPattern.MatchString(s.EntityID)) {
		return ErrInvalidEvent
	}
	if _, err := time.Parse(time.RFC3339Nano, s.OccurredAt); err != nil {
		return ErrInvalidEvent
	}
	return nil
}

func ValidateEvent(e Event) error {
	s := Submission{
		ContractVersion: e.ContractVersion, EventID: e.EventID, TenantID: e.TenantID,
		ServiceID: e.ServiceID, Actor: e.Actor, Action: e.Action, Resource: e.Resource,
		Result: e.Result, CorrelationID: e.CorrelationID, OccurredAt: e.OccurredAt,
		EntityType: e.EntityType, EntityID: e.EntityID,
	}
	if err := ValidateSubmission(s); err != nil || strings.TrimSpace(e.ReceivedAt) == "" || len(e.ReceivedAt) > 40 {
		return ErrInvalidEvent
	}
	if _, err := time.Parse(time.RFC3339Nano, e.ReceivedAt); err != nil {
		return ErrInvalidEvent
	}
	return nil
}

// DuplicateError reports that an identical event was already stored under the
// same (tenant_id, service_id, event_id). Nothing new was written; Stored is the
// event as originally recorded, including its original received_at.
type DuplicateError struct{ Stored Event }

func (*DuplicateError) Error() string { return "audit event already stored" }

// eventKey is the identity of an event: one event_id per tenant and service.
type eventKey struct{ tenantID, serviceID, eventID string }

func keyOf(e Event) eventKey { return eventKey{e.TenantID, e.ServiceID, e.EventID} }

// sameContent compares every emitter-supplied field. ReceivedAt is assigned by
// the server on each attempt and is intentionally excluded. Timestamps are
// compared as sent: the same instant written in a different format counts as
// different content.
func sameContent(a, b Event) bool {
	a.ReceivedAt, b.ReceivedAt = "", ""
	return a == b
}
