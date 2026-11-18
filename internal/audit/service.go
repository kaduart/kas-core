package audit

import (
	"context"
	"errors"
	"time"
)

// Repository is the small persistence boundary for audit events.
type Repository interface {
	Append(context.Context, Event) error
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

// Result is the outcome of Submit. Duplicate is true when an identical event
// had already been stored; Event is then the originally stored event and
// nothing was written.
type Result struct {
	Event     Event
	Duplicate bool
}

// ReadinessChecker is implemented by repositories that can report whether they
// can currently accept writes.
type ReadinessChecker interface {
	Ready(context.Context) error
}

// Ready reports whether audit storage can currently accept writes. Repositories
// without a readiness check are assumed ready.
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

// Record is Submit without the duplicate indication.
func (s *Service) Record(ctx context.Context, authenticatedService string, submission Submission) (Event, error) {
	result, err := s.Submit(ctx, authenticatedService, submission)
	return result.Event, err
}

// Submit stores the event once per (tenant_id, service_id, event_id). An
// identical resend succeeds without a second write; the same identity with
// different content fails with ErrEventConflict.
func (s *Service) Submit(ctx context.Context, authenticatedService string, submission Submission) (Result, error) {
	if s == nil || s.repository == nil {
		return Result{}, ErrStoreUnavailable
	}
	if submission.ServiceID != authenticatedService {
		return Result{}, ErrServiceMismatch
	}
	if err := ValidateSubmission(submission); err != nil {
		return Result{}, err
	}
	event := Event{
		ContractVersion: submission.ContractVersion,
		EventID:         submission.EventID,
		TenantID:        submission.TenantID,
		ServiceID:       authenticatedService,
		Actor:           submission.Actor,
		Action:          submission.Action,
		Resource:        submission.Resource,
		Result:          submission.Result,
		CorrelationID:   submission.CorrelationID,
		OccurredAt:      submission.OccurredAt,
		EntityType:      submission.EntityType,
		EntityID:        submission.EntityID,
		ReceivedAt:      s.now().UTC().Format(time.RFC3339Nano),
	}
	if err := s.repository.Append(ctx, event); err != nil {
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
