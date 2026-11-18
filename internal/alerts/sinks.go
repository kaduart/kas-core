package alerts

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// SimulatedSink records notifications in memory. It never talks to a network.
// FailNext makes the next N deliveries fail, to exercise retry.
type SimulatedSink struct {
	mu        sync.Mutex
	delivered []Notification
	FailNext  int
	Attempts  int
}

// ErrSimulatedFailure is what a failing SimulatedSink returns.
var ErrSimulatedFailure = errors.New("simulated delivery failure")

func (s *SimulatedSink) Deliver(_ context.Context, n Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Attempts++
	if s.FailNext > 0 {
		s.FailNext--
		return ErrSimulatedFailure
	}
	s.delivered = append(s.delivered, n)
	return nil
}

// Delivered returns a copy of what was delivered.
func (s *SimulatedSink) Delivered() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Notification(nil), s.delivered...)
}

// LogSink writes notifications to a structured log. It is a delivery channel
// only if someone watches that log; it is not a monitored channel by itself.
type LogSink struct{ Logger *slog.Logger }

func (s LogSink) Deliver(_ context.Context, n Notification) error {
	if s.Logger == nil {
		return errors.New("alerts: LogSink without logger")
	}
	level := slog.LevelWarn
	if n.Severity == Critical.String() {
		level = slog.LevelError
	}
	s.Logger.Log(context.Background(), level, "alert", "source", n.Source, "kind", n.Kind, "severity", n.Severity,
		"summary", n.Summary, "alerts", n.Alerts, "duplicates", n.Duplicates, "subjects", n.Subjects,
		"omittedSubjects", n.Omitted, "escalation", n.Escalation)
	return nil
}
