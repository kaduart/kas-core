package events

import (
	"context"
	"errors"
	"time"
)

// Registry is the declared configuration the service needs.
type Registry interface {
	EventTypeDeclared(eventType string) bool
	// ConsumersFor returns the consumers subscribed to the type for the tenant.
	ConsumersFor(tenantID, eventType string) []string
}

// Repository stores an event and one delivery per consumer atomically. It
// returns *DuplicateError for an identical resend and ErrEventConflict when the
// identity exists with different content.
type Repository interface {
	Publish(ctx context.Context, event Event, consumers []string) error
}

// ReadinessChecker is implemented by repositories that can report readiness.
type ReadinessChecker interface {
	Ready(context.Context) error
}

// Result is the outcome of Publish. Duplicate is true when an identical event
// had already been published; Event is then the originally stored event.
type Result struct {
	Event     Event
	Duplicate bool
}

type Service struct {
	repository Repository
	registry   Registry
	now        func() time.Time
}

func NewService(repository Repository, registry Registry) *Service {
	return &Service{repository: repository, registry: registry, now: func() time.Time { return time.Now().UTC() }}
}

// Publish validates and stores the event once per (tenant_id, service_id,
// event_id), creating a delivery for every consumer subscribed to its type and
// tenant at this moment.
func (s *Service) Publish(ctx context.Context, authenticatedService string, submission Submission) (Result, error) {
	if s == nil || s.repository == nil || s.registry == nil {
		return Result{}, ErrStoreUnavailable
	}
	if submission.ServiceID != authenticatedService {
		return Result{}, ErrServiceMismatch
	}
	payload, err := ValidateSubmission(submission)
	if err != nil {
		return Result{}, err
	}
	if !s.registry.EventTypeDeclared(submission.EventType) {
		return Result{}, ErrUnknownEventType
	}
	event := Event{
		ContractVersion: submission.ContractVersion,
		EventID:         submission.EventID,
		TenantID:        submission.TenantID,
		ServiceID:       authenticatedService,
		EventType:       submission.EventType,
		CorrelationID:   submission.CorrelationID,
		OccurredAt:      submission.OccurredAt,
		ReceivedAt:      s.now().UTC().Format(time.RFC3339Nano),
		Payload:         payload,
	}
	consumers := s.registry.ConsumersFor(event.TenantID, event.EventType)
	if err := s.repository.Publish(ctx, event, consumers); err != nil {
		var duplicate *DuplicateError
		switch {
		case errors.As(err, &duplicate):
			return Result{Event: duplicate.Stored, Duplicate: true}, nil
		case errors.Is(err, ErrEventConflict):
			return Result{}, ErrEventConflict
		}
		return Result{}, ErrStoreUnavailable
	}
	return Result{Event: event}, nil
}

// Ready reports whether event storage can currently accept writes.
func (s *Service) Ready(ctx context.Context) error {
	if s == nil || s.repository == nil {
		return ErrStoreUnavailable
	}
	if checker, ok := s.repository.(ReadinessChecker); ok {
		if err := checker.Ready(ctx); err != nil {
			return ErrStoreUnavailable
		}
	}
	return nil
}
