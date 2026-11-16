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
	identifierPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	actorPattern        = regexp.MustCompile(`^(user|service|system)\.[a-z0-9][a-z0-9._-]{0,95}$`)
	actionPattern       = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)*$`)
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
	}
	if err := ValidateSubmission(s); err != nil || strings.TrimSpace(e.ReceivedAt) == "" || len(e.ReceivedAt) > 40 {
		return ErrInvalidEvent
	}
	if _, err := time.Parse(time.RFC3339Nano, e.ReceivedAt); err != nil {
		return ErrInvalidEvent
	}
	return nil
}
