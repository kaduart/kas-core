package outboxrecovery

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func failed(attempts int64) Entry {
	return Entry{ID: "x", Status: StatusFailed, Attempts: attempts, AttemptsValid: true, RetryAt: t0.Add(-time.Second), RetryAtValid: true}
}

func TestDecideOrder(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		name   string
		mut    func(e *Entry)
		action Action
		reason string
	}{
		{"pending is not touched", func(e *Entry) { e.Status = StatusPending }, ActionNone, ReasonNotFailed},
		{"published is not touched", func(e *Entry) { e.Status = StatusPublished }, ActionNone, ReasonNotFailed},
		{"already exhausted wins over everything", func(e *Entry) { e.Exhausted, e.PermanentError = true, true }, ActionNone, ReasonAlreadyExhausted},
		{"unreadable counter is reported, never acted on", func(e *Entry) { e.AttemptsValid, e.PermanentError = false, true }, ActionNone, ReasonMalformed},
		{"permanent error is exhausted even with backoff pending", func(e *Entry) { e.PermanentError, e.RetryAt = true, t0.Add(time.Hour) }, ActionExhaust, ReasonPermanentError},
		{"attempts at the limit", func(e *Entry) { e.Attempts = 5 }, ActionExhaust, ReasonAttemptsExhausted},
		{"attempts above the limit", func(e *Entry) { e.Attempts = 50 }, ActionExhaust, ReasonAttemptsExhausted},
		{"recovery limit protects against a dispatcher that does not count", func(e *Entry) { e.Recoveries = 5 }, ActionExhaust, ReasonRecoveryLimit},
		{"backoff not elapsed", func(e *Entry) { e.RetryAt = t0.Add(time.Millisecond) }, ActionNone, ReasonBackoff},
		{"backoff exactly now is due", func(e *Entry) { e.RetryAt = t0 }, ActionRecover, ReasonRetry},
		{"missing retry instant is due", func(e *Entry) { e.RetryAtValid = false }, ActionRecover, ReasonRetry},
		{"below the limit", func(e *Entry) { e.Attempts = 4; e.Recoveries = 4 }, ActionRecover, ReasonRetry},
		{"zero attempts", func(e *Entry) { e.Attempts = 0 }, ActionRecover, ReasonRetry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := failed(1)
			c.mut(&e)
			d := Decide(cfg, t0, e)
			if d.Action != c.action || d.Reason != c.reason {
				t.Fatalf("got %s/%s, want %s/%s", d.Action, d.Reason, c.action, c.reason)
			}
		})
	}
}

func TestPermanentPrefixes(t *testing.T) {
	cfg := DefaultConfig()
	for s, want := range map[string]bool{
		"UNKNOWN_EVENT_TYPE: X não está em eventToQueueMap": true,
		"Connection is closed.":                             false,
		"":                                                  false,
		" UNKNOWN_EVENT_TYPE":                               false,
	} {
		if got := cfg.IsPermanent(s); got != want {
			t.Errorf("IsPermanent(%q) = %v", s, got)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, mut := range []func(*Config){
		func(c *Config) { c.MaxAttempts = 0 },
		func(c *Config) { c.MaxRecoveries = 0 },
		func(c *Config) { c.BatchSize = 0 },
		func(c *Config) { c.BatchSize = 1001 },
		func(c *Config) { c.PermanentErrorPrefixes = []string{" "} },
	} {
		c := DefaultConfig()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("accepted invalid config %+v", c)
		}
	}
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}
