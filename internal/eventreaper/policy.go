// Package eventreaper decides what to do with events that look stuck in
// "processing" and runs that decision in shadow or executor mode.
//
// The policy in this file is pure: it knows nothing about MongoDB, the CRM or
// any clinical model. It reproduces the observable behaviour of the legacy
// Node reaper (fono-inova-crm-back@10d7e75, crons/eventReaper.cron.js); every
// known defect of that behaviour is preserved here on purpose and listed in
// docs/event-reaper/DIVERGENCIAS.md, never silently fixed.
package eventreaper

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Status values used by the legacy event store.
const (
	StatusProcessing = "processing"
	StatusPending    = "pending"
	StatusFailed     = "failed"
)

// Config mirrors the constants of the legacy reaper.
type Config struct {
	// StuckThreshold: an event whose createdAt is strictly older than now minus
	// this value is eligible (legacy: STUCK_THRESHOLD_MINUTES = 15).
	StuckThreshold time.Duration
	// MaxAttemptsForRetry: attempts < this value goes back to pending, anything
	// else goes to failed (legacy: MAX_ATTEMPTS_FOR_RETRY = 3).
	MaxAttemptsForRetry int
	// BatchSize: events handled per cycle (legacy: .limit(100)).
	BatchSize int
}

// LegacyConfig returns the values hard-coded in the legacy reaper.
func LegacyConfig() Config {
	return Config{StuckThreshold: 15 * time.Minute, MaxAttemptsForRetry: 3, BatchSize: 100}
}

// Validate rejects values that would make the policy meaningless.
func (c Config) Validate() error {
	switch {
	case c.StuckThreshold <= 0:
		return fmt.Errorf("stuck threshold must be positive")
	case c.MaxAttemptsForRetry < 1:
		return fmt.Errorf("max attempts for retry must be at least 1")
	case c.BatchSize < 1 || c.BatchSize > 1000:
		return fmt.Errorf("batch size must be between 1 and 1000")
	}
	return nil
}

// Cutoff is the instant before which createdAt makes an event eligible.
func (c Config) Cutoff(now time.Time) time.Time { return now.Add(-c.StuckThreshold) }

// RecoveryMessage is the exact error.message the legacy reaper stores.
func (c Config) RecoveryMessage() string {
	// JS template: `Auto-recovered from stuck processing after ${STUCK_THRESHOLD_MINUTES}min`
	return "Auto-recovered from stuck processing after " + formatJSNumber(c.StuckThreshold.Minutes()) + "min"
}

// AttemptsKind tells how the attempts field was stored. The legacy code reads
// documents with .lean(), so it sees the raw BSON value, not a cast Number.
type AttemptsKind int

const (
	AttemptsMissing AttemptsKind = iota // field absent (JS undefined)
	AttemptsNull                        // explicit null
	AttemptsNumber                      // int32, int64, double or decimal
	AttemptsString
	AttemptsBool
	AttemptsOther // object, array, date, ... (compares as NaN or as a date number)
)

// Attempts is the raw attempts value of a document.
type Attempts struct {
	Kind   AttemptsKind
	Number float64 // AttemptsNumber, or date milliseconds for a date in AttemptsOther
	Text   string  // AttemptsString
	Bool   bool    // AttemptsBool
	IsDate bool    // AttemptsOther holding a BSON date
}

// NumberAttempts is a convenience constructor for the common case.
func NumberAttempts(n float64) Attempts { return Attempts{Kind: AttemptsNumber, Number: n} }

// jsNumber reproduces JavaScript ToNumber for the value, as used by the
// relational comparison `event.attempts < MAX_ATTEMPTS_FOR_RETRY`.
func (a Attempts) jsNumber() float64 {
	switch a.Kind {
	case AttemptsNull:
		return 0
	case AttemptsNumber:
		return a.Number
	case AttemptsBool:
		if a.Bool {
			return 1
		}
		return 0
	case AttemptsString:
		return jsStringToNumber(a.Text)
	case AttemptsOther:
		if a.IsDate {
			return a.Number
		}
	}
	return math.NaN() // undefined, objects, arrays
}

// jsStringToNumber follows the StringToNumber rules for the forms that can
// plausibly appear in a database: decimal, exponent, Infinity, hex, binary,
// octal, surrounding white space and the empty string (which is 0).
func jsStringToNumber(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	for _, p := range []struct {
		prefix string
		base   int
	}{{"0x", 16}, {"0X", 16}, {"0b", 2}, {"0B", 2}, {"0o", 8}, {"0O", 8}} {
		if strings.HasPrefix(s, p.prefix) {
			v, err := strconv.ParseUint(s[2:], p.base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(v)
		}
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	// JS accepts only decimal literals here; reject Go-only forms.
	for _, r := range s {
		if !strings.ContainsRune("0123456789+-.eE", r) {
			return math.NaN()
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

// Candidate is the minimum the policy needs about an event. It deliberately
// has no payload, aggregate or metadata field.
type Candidate struct {
	EventID   string
	EventType string
	Status    string
	CreatedAt time.Time
	Attempts  Attempts
	// ProcessingToken is empty for every legacy document. It exists so that a
	// future fenced claim protocol can be honoured (see DIVERGENCIAS.md, F-1).
	ProcessingToken string
}

// Decision is what the policy wants to happen to one event.
type Decision struct {
	EventID  string
	From     string
	To       string
	Eligible bool
	Reason   string
}

// Decide applies the legacy rule to one candidate observed at now.
func Decide(cfg Config, now time.Time, c Candidate) Decision {
	d := Decision{EventID: c.EventID, From: c.Status}
	if c.Status != StatusProcessing {
		d.Reason = "not_processing"
		return d
	}
	// Mongo `createdAt: {$lt: threshold}` is strict.
	if !c.CreatedAt.Before(cfg.Cutoff(now)) {
		d.Reason = "not_old_enough"
		return d
	}
	d.Eligible = true
	// JS: undefined < 3 and NaN < 3 are false, so those go to failed.
	if c.Attempts.jsNumber() < float64(cfg.MaxAttemptsForRetry) {
		d.To, d.Reason = StatusPending, "retry"
	} else {
		d.To, d.Reason = StatusFailed, "too_many_attempts"
	}
	return d
}

func formatJSNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e21 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
