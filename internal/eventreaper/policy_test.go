package eventreaper

import (
	"context"
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func cand(age time.Duration, a Attempts) Candidate {
	return Candidate{EventID: "evt-ficticio", Status: StatusProcessing, CreatedAt: t0.Add(-age), Attempts: a}
}

// Characterisation of the legacy rule; each case was confirmed against the
// unmodified Node reaper by the oracle tests in internal/legacy/crmeventstore.
func TestDecideCharacterisesLegacyRule(t *testing.T) {
	cfg := LegacyConfig()
	cases := []struct {
		name     string
		c        Candidate
		eligible bool
		to       string
	}{
		{"exactly 15 min old is not eligible ($lt is strict)", cand(15*time.Minute, NumberAttempts(0)), false, ""},
		{"1 ms past the threshold is eligible", cand(15*time.Minute+time.Millisecond, NumberAttempts(0)), true, StatusPending},
		{"young processing event is left alone", cand(time.Minute, NumberAttempts(0)), false, ""},
		{"attempts 0 -> pending", cand(time.Hour, NumberAttempts(0)), true, StatusPending},
		{"attempts 2 -> pending", cand(time.Hour, NumberAttempts(2)), true, StatusPending},
		{"attempts 2.9 -> pending", cand(time.Hour, NumberAttempts(2.9)), true, StatusPending},
		{"attempts 3 -> failed", cand(time.Hour, NumberAttempts(3)), true, StatusFailed},
		{"attempts 7 -> failed", cand(time.Hour, NumberAttempts(7)), true, StatusFailed},
		{"negative attempts -> pending", cand(time.Hour, NumberAttempts(-1)), true, StatusPending},
		{"missing attempts -> failed (undefined < 3 is false)", cand(time.Hour, Attempts{Kind: AttemptsMissing}), true, StatusFailed},
		{"null attempts -> pending (null < 3 is true)", cand(time.Hour, Attempts{Kind: AttemptsNull}), true, StatusPending},
		{"string '2' -> pending", cand(time.Hour, Attempts{Kind: AttemptsString, Text: "2"}), true, StatusPending},
		{"string ' 5 ' -> failed", cand(time.Hour, Attempts{Kind: AttemptsString, Text: " 5 "}), true, StatusFailed},
		{"empty string -> pending (Number('') is 0)", cand(time.Hour, Attempts{Kind: AttemptsString, Text: ""}), true, StatusPending},
		{"string 'abc' -> failed (NaN)", cand(time.Hour, Attempts{Kind: AttemptsString, Text: "abc"}), true, StatusFailed},
		{"string '0x2' -> pending", cand(time.Hour, Attempts{Kind: AttemptsString, Text: "0x2"}), true, StatusPending},
		{"true -> pending", cand(time.Hour, Attempts{Kind: AttemptsBool, Bool: true}), true, StatusPending},
		{"object -> failed (NaN)", cand(time.Hour, Attempts{Kind: AttemptsOther}), true, StatusFailed},
		{"date -> failed (large number)", cand(time.Hour, Attempts{Kind: AttemptsOther, IsDate: true, Number: 1.7e12}), true, StatusFailed},
		{"NaN number -> failed", cand(time.Hour, NumberAttempts(math.NaN())), true, StatusFailed},
		{"other status is never touched", Candidate{EventID: "x", Status: "processed", CreatedAt: t0.Add(-time.Hour)}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(cfg, t0, tc.c)
			if d.Eligible != tc.eligible || d.To != tc.to {
				t.Fatalf("got eligible=%v to=%q (%s), want eligible=%v to=%q", d.Eligible, d.To, d.Reason, tc.eligible, tc.to)
			}
		})
	}
}

func TestRecoveryMessageIsTheLegacyText(t *testing.T) {
	if got, want := LegacyConfig().RecoveryMessage(), "Auto-recovered from stuck processing after 15min"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	c := LegacyConfig()
	c.StuckThreshold = 90 * time.Second
	if got := c.RecoveryMessage(); got != "Auto-recovered from stuck processing after 1.5min" {
		t.Fatalf("got %q", got)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := LegacyConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Config{{0, 3, 100}, {time.Minute, 0, 100}, {time.Minute, 3, 0}, {time.Minute, 3, 1001}} {
		if c.Validate() == nil {
			t.Fatalf("%+v should be invalid", c)
		}
	}
}

// Known legacy defects, preserved on purpose (docs/event-reaper/DIVERGENCIAS.md).
// These tests pin the behaviour so that fixing it becomes a visible decision.

// L-1: age is measured from createdAt, not from when processing started, so an
// old event that a worker picked up a second ago is already "stuck".
func TestKnownDefectL1AgeComesFromCreatedAtNotFromProcessingStart(t *testing.T) {
	justClaimed := Candidate{EventID: "retry", Status: StatusProcessing, CreatedAt: t0.Add(-16 * time.Minute), Attempts: NumberAttempts(1)}
	if d := Decide(LegacyConfig(), t0, justClaimed); !d.Eligible || d.To != StatusPending {
		t.Fatalf("legacy reaps an event created 16 min ago regardless of when it was claimed; got %+v", d)
	}
}

// L-2: the reaper never increments attempts, so an event whose worker dies
// every time bounces between processing and pending forever.
func TestKnownDefectL2ReaperNeverIncrementsAttempts(t *testing.T) {
	m := newMem(old("crash-loop", 0))
	r := &Runner{Config: LegacyConfig(), Mode: ModeExecutor, Store: m, Lease: okLease{true}, Now: fixed}
	for i := 0; i < 5; i++ {
		r.RunCycle(context.Background())
		if m.events["crash-loop"].Status != StatusPending {
			t.Fatalf("cycle %d: %s", i, m.events["crash-loop"].Status)
		}
		m.events["crash-loop"].Status = StatusProcessing // worker claims and dies again
	}
	if m.events["crash-loop"].Attempts != NumberAttempts(0) {
		t.Fatal("attempts changed")
	}
}
