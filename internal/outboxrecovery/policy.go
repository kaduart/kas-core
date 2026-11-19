// Package outboxrecovery decides what to do with transactional-outbox entries
// that a dispatcher marked as failed, and runs that decision in shadow (read
// only) or executor mode.
//
// The policy in this file is pure and generic: it knows nothing about
// MongoDB, BullMQ, the CRM or any clinical model. An outbox entry is reduced
// to its status, its attempt counter, the instant before which the dispatcher
// must not retry it, a coarse classification of its last error and the
// recovery bookkeeping this package keeps on the entry.
//
// Recovery is at-least-once by construction: putting an entry back in the
// dispatcher's queue can publish a message that a consumer already saw (for
// example when the first publish reached the broker but the status update was
// lost). Consumers must deduplicate; this package never promises exactly once.
package outboxrecovery

import (
	"errors"
	"strings"
	"time"
)

// Status values of a transactional outbox.
const (
	StatusPending   = "pending"
	StatusPublished = "published"
	StatusFailed    = "failed"
)

// Action is what the policy wants to happen to one entry.
type Action string

const (
	// ActionNone leaves the entry untouched.
	ActionNone Action = "none"
	// ActionRecover puts a failed entry back to pending so the dispatcher
	// publishes it again.
	ActionRecover Action = "recover"
	// ActionExhaust marks a failed entry as given up (it stays failed) and
	// raises an alert. It is a reversible annotation, never a deletion.
	ActionExhaust Action = "exhaust"
)

// Reasons reported for each decision. They are stable identifiers used in
// reports and metrics.
const (
	ReasonNotFailed         = "not_failed"
	ReasonAlreadyExhausted  = "already_exhausted"
	ReasonMalformed         = "malformed_entry"
	ReasonBackoff           = "backoff_not_elapsed"
	ReasonRetry             = "retry"
	ReasonPermanentError    = "permanent_error"
	ReasonAttemptsExhausted = "attempts_exhausted"
	ReasonRecoveryLimit     = "recovery_limit"
)

// Config is the generic recovery policy.
type Config struct {
	// MaxAttempts: a failed entry whose attempt counter reached this value is
	// exhausted instead of recovered.
	MaxAttempts int
	// MaxRecoveries: an upper bound on how many times this package recovers
	// the same entry, independent of the dispatcher's own counter. It protects
	// against a dispatcher that fails without incrementing attempts.
	MaxRecoveries int
	// PermanentErrorPrefixes: a last error starting with one of these is not
	// worth retrying (for the CRM: UNKNOWN_EVENT_TYPE, an event the dispatcher
	// has no queue for). Such entries are exhausted on sight.
	PermanentErrorPrefixes []string
	// BatchSize: entries handled per cycle.
	BatchSize int
}

// DefaultConfig: five attempts, matching the dispatcher's per-job BullMQ
// attempts, and the CRM's only known permanent error.
func DefaultConfig() Config {
	return Config{MaxAttempts: 5, MaxRecoveries: 5, PermanentErrorPrefixes: []string{"UNKNOWN_EVENT_TYPE"}, BatchSize: 100}
}

// Validate rejects values that would make the policy meaningless.
func (c Config) Validate() error {
	switch {
	case c.MaxAttempts < 1:
		return errors.New("max attempts must be at least 1")
	case c.MaxRecoveries < 1:
		return errors.New("max recoveries must be at least 1")
	case c.BatchSize < 1 || c.BatchSize > 1000:
		return errors.New("batch size must be between 1 and 1000")
	}
	for _, p := range c.PermanentErrorPrefixes {
		if strings.TrimSpace(p) == "" {
			return errors.New("permanent error prefixes must not be empty")
		}
	}
	return nil
}

// Entry is the minimum the policy needs about an outbox entry. It has no
// payload, aggregate or correlation field on purpose.
type Entry struct {
	// ID is opaque to the policy (the adapter's primary key, as text).
	ID     string
	Status string
	// Attempts is the dispatcher's failure counter. AttemptsValid is false
	// when the stored value is not a finite whole number (absent counts as 0).
	Attempts      int64
	AttemptsValid bool
	// RetryAt is the instant before which the dispatcher must not retry the
	// entry (its own backoff). RetryAtValid is false when it is absent or not
	// a date.
	RetryAt      time.Time
	RetryAtValid bool
	// PermanentError is the adapter's classification of the last error
	// against Config.PermanentErrorPrefixes. The text itself is never kept.
	PermanentError bool
	// Type and CreatedAt are reported by the shadow (what kind of entry, how
	// old) and never used by the policy: they do not change any decision.
	Type           string
	CreatedAt      time.Time
	CreatedAtValid bool
	// Recoveries and Exhausted are this package's own bookkeeping.
	Recoveries int64
	Exhausted  bool
	// Observed is adapter-private state captured by Scan (the raw stored
	// values) and handed back to Recover/Exhaust for the conditional write.
	// The policy never looks at it.
	Observed any
}

// IsPermanent classifies an error text against the configured prefixes.
func (c Config) IsPermanent(lastError string) bool {
	for _, p := range c.PermanentErrorPrefixes {
		if strings.HasPrefix(lastError, p) {
			return true
		}
	}
	return false
}

// Decision is the policy's verdict for one entry.
type Decision struct {
	ID     string
	Action Action
	Reason string
}

// Decide applies the policy to one entry observed at now. The order of the
// checks is part of the contract and is covered by tests.
func Decide(cfg Config, now time.Time, e Entry) Decision {
	d := Decision{ID: e.ID, Action: ActionNone}
	switch {
	case e.Status != StatusFailed:
		d.Reason = ReasonNotFailed
	case e.Exhausted:
		d.Reason = ReasonAlreadyExhausted
	case !e.AttemptsValid:
		// Never guess: a counter we cannot read is reported, not acted on.
		d.Reason = ReasonMalformed
	case e.PermanentError:
		d.Action, d.Reason = ActionExhaust, ReasonPermanentError
	case e.Attempts >= int64(cfg.MaxAttempts):
		d.Action, d.Reason = ActionExhaust, ReasonAttemptsExhausted
	case e.Recoveries >= int64(cfg.MaxRecoveries):
		d.Action, d.Reason = ActionExhaust, ReasonRecoveryLimit
	case e.RetryAtValid && e.RetryAt.After(now):
		// The dispatcher's own backoff has not elapsed yet.
		d.Reason = ReasonBackoff
	default:
		// An absent or unreadable retry instant does not block recovery: the
		// dispatcher treats the entry as due as soon as it is pending.
		d.Action, d.Reason = ActionRecover, ReasonRetry
	}
	return d
}
