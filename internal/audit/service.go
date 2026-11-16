package audit

import (
	"context"
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

func (s *Service) Record(ctx context.Context, authenticatedService string, submission Submission) (Event, error) {
	if s == nil || s.repository == nil {
		return Event{}, ErrStoreUnavailable
	}
	if submission.ServiceID != authenticatedService {
		return Event{}, ErrServiceMismatch
	}
	if err := ValidateSubmission(submission); err != nil {
		return Event{}, err
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
		ReceivedAt:      s.now().UTC().Format(time.RFC3339Nano),
	}
	if err := s.repository.Append(ctx, event); err != nil {
		return Event{}, ErrStoreUnavailable
	}
	return event, nil
}
