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
	StatusDeadLetter = "dead_letter"
)

// Protocol selects which version of the CRM reaper is reproduced.
type Protocol string

const (
	// ProtocolV1 is the reaper running in production today
	// (fono-inova-crm-back@10d7e75): age by createdAt, no ownership fields.
	ProtocolV1 Protocol = "v1"
	// ProtocolV2 is the reaper of the CRM branch feat/eventstore-posse:
	// execution leases, conditional recovery, recoveryCount and dead_letter.
	ProtocolV2 Protocol = "v2"
)

// ParseProtocol validates a protocol name.
func ParseProtocol(s string) (Protocol, error) {
	switch Protocol(s) {
	case ProtocolV1, ProtocolV2:
		return Protocol(s), nil
	}
	return "", fmt.Errorf("unknown protocol %q (use v1 or v2)", s)
}

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
	// Protocol: v1 (default, production today) or v2 (execution ownership).
	Protocol Protocol
	// MaxRecoveries (v2 only): the recovery that reaches this count moves the
	// event to dead_letter (CRM: MAX_RECOVERIES = 3).
	MaxRecoveries int
	// LegacyFence (v2 only): events in processing without ownership (claimed
	// by pre-ownership CRM code) are eligible only once this instant has
	// passed (CRM: EVENT_LEGACY_FENCE_AT). Zero: never.
	LegacyFence time.Time
}

// LegacyCutoff (v2) is the createdAt bound for ownerless events, or the zero
// time when the fence is not set or not yet reached (ownerless events are
// then never eligible).
func (c Config) LegacyCutoff(now time.Time) time.Time {
	if c.LegacyFence.IsZero() || now.Before(c.LegacyFence) {
		return time.Time{}
	}
	cut := c.Cutoff(now)
	if c.LegacyFence.Before(cut) {
		return c.LegacyFence
	}
	return cut
}

func (c Config) protocol() Protocol {
	if c.Protocol == "" {
		return ProtocolV1
	}
	return c.Protocol
}

// LegacyConfig returns the values hard-coded in the legacy reaper.
func LegacyConfig() Config {
	return Config{StuckThreshold: 15 * time.Minute, MaxAttemptsForRetry: 3, BatchSize: 100, Protocol: ProtocolV1, MaxRecoveries: 3}
}

// V2Config returns the values of the CRM reaper with execution ownership.
func V2Config() Config {
	c := LegacyConfig()
	c.Protocol = ProtocolV2
	return c
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
	case c.protocol() != ProtocolV1 && c.protocol() != ProtocolV2:
		return fmt.Errorf("unknown protocol %q", c.Protocol)
	case c.protocol() == ProtocolV2 && c.MaxRecoveries < 1:
		return fmt.Errorf("max recoveries must be at least 1")
	}
	return nil
}

// Cutoff is the instant before which createdAt makes an event eligible.
func (c Config) Cutoff(now time.Time) time.Time { return now.Add(-c.StuckThreshold) }

// Messages and codes of the v2 reaper (crons/eventReaper.cron.js on
// feat/eventstore-posse).
const (
	LeaseMessage  = "Auto-recovered: processing lease expired"
	CodeRecovered = "RECOVERED_STUCK_PROCESSING"
	CodeExhausted = "RECOVERY_LIMIT_EXCEEDED"
)

// ExhaustedMessage is the v2 message for the recovery that hits the limit.
func (c Config) ExhaustedMessage() string {
	return "Recovery limit reached (" + strconv.Itoa(c.MaxRecoveries) + "); moved to dead_letter"
}

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
	// TokenNullOrMissing: processingToken absent or null (no owner).
	TokenNullOrMissing bool
	// Lease is the raw leaseExpiresAt field (v2).
	Lease LeaseField
	// RecoveryCount is the raw recoveryCount field (v2), read like Attempts.
	RecoveryCount Attempts
}

// LeaseField is the raw leaseExpiresAt value.
type LeaseField struct {
	Present bool      // field exists (any type)
	Null    bool      // field holds BSON null
	IsDate  bool      // field holds a BSON date
	At      time.Time // when IsDate
}

// jsNumberOrZero reproduces `Number(x) || 0`.
func (a Attempts) jsNumberOrZero() float64 {
	n := a.jsNumber()
	if a.Kind == AttemptsMissing || math.IsNaN(n) || n == 0 {
		return 0
	}
	return n
}

// Decision is what the policy wants to happen to one event.
type Decision struct {
	EventID  string
	From     string
	To       string
	Eligible bool
	Reason   string
	// v2 only.
	Recoveries float64
	Message    string
	Code       string
	Exhausted  bool
}

// Decide applies the legacy rule to one candidate observed at now.
func Decide(cfg Config, now time.Time, c Candidate) Decision {
	d := Decision{EventID: c.EventID, From: c.Status}
	if c.Status != StatusProcessing {
		d.Reason = "not_processing"
		return d
	}
	if cfg.protocol() == ProtocolV2 {
		return decideV2(cfg, now, c, d)
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

// decideV2 reproduces stuckFilter + decide of the v2 CRM reaper.
func decideV2(cfg Config, now time.Time, c Candidate, d Decision) Decision {
	legacyCut := cfg.LegacyCutoff(now)
	ownerless := c.TokenNullOrMissing && (!c.Lease.Present || c.Lease.Null)
	switch {
	case c.Lease.Present && c.Lease.IsDate && !c.Lease.At.After(now):
		// {leaseExpiresAt: {$lte: now}}
	case ownerless && !legacyCut.IsZero() && c.CreatedAt.Before(legacyCut):
		// fence passed: {processingToken: null, leaseExpiresAt: null, createdAt: {$lt: min(cutoff, fence)}}
	default:
		switch {
		case ownerless && legacyCut.IsZero():
			d.Reason = "legacy_awaiting_fence"
		case c.Lease.Present && !c.Lease.Null:
			d.Reason = "lease_alive"
		default:
			d.Reason = "not_old_enough"
		}
		return d
	}
	d.Eligible = true
	d.Recoveries = c.RecoveryCount.jsNumberOrZero() + 1
	if d.Recoveries >= float64(cfg.MaxRecoveries) {
		d.To, d.Reason, d.Message, d.Code, d.Exhausted = StatusDeadLetter, "recovery_limit", cfg.ExhaustedMessage(), CodeExhausted, true
		return d
	}
	if c.Attempts.jsNumber() < float64(cfg.MaxAttemptsForRetry) {
		d.To, d.Reason = StatusPending, "retry"
	} else {
		d.To, d.Reason = StatusFailed, "too_many_attempts"
	}
	d.Code = CodeRecovered
	// JS: event.leaseExpiresAt ? LEASE_MESSAGE : LEGACY_MESSAGE
	if c.Lease.Present && c.Lease.IsDate {
		d.Message = LeaseMessage
	} else {
		d.Message = cfg.RecoveryMessage()
	}
	return d
}

func formatJSNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e21 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
