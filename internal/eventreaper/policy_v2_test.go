package eventreaper

import (
	"testing"
	"time"
)

func TestDecideV2Table(t *testing.T) {
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	cfg := V2Config()
	cfg.LegacyFence = now.Add(-30 * time.Minute) // deploy declared complete
	old := now.Add(-time.Hour)
	recent := now.Add(-time.Minute)
	lease := func(at time.Time) LeaseField { return LeaseField{Present: true, IsDate: true, At: at} }
	cases := []struct {
		name       string
		c          Candidate
		eligible   bool
		to         string
		msg        string
		recoveries float64
	}{
		{"lease expired, recent event", Candidate{CreatedAt: recent, Lease: lease(now.Add(-time.Second)), Attempts: NumberAttempts(0)}, true, StatusPending, LeaseMessage, 1},
		{"lease exactly now counts as expired ($lte)", Candidate{CreatedAt: recent, Lease: lease(now), Attempts: NumberAttempts(0)}, true, StatusPending, LeaseMessage, 1},
		{"lease alive, old event: owner keeps it", Candidate{CreatedAt: old, Lease: lease(now.Add(time.Millisecond))}, false, "", "", 0},
		{"missing attempts: JS undefined < 3 is false -> failed", Candidate{CreatedAt: old}, true, StatusFailed, cfg.RecoveryMessage(), 1},
		{"lease null: neither criterion matches", Candidate{CreatedAt: old, Lease: LeaseField{Present: true}}, false, "", "", 0},
		{"no lease, old: legacy criterion", Candidate{CreatedAt: old, Attempts: NumberAttempts(1)}, true, StatusPending, cfg.RecoveryMessage(), 1},
		{"no lease, recent: wait", Candidate{CreatedAt: recent}, false, "", "", 0},
		{"too many attempts -> failed", Candidate{CreatedAt: old, Attempts: NumberAttempts(3), RecoveryCount: NumberAttempts(1)}, true, StatusFailed, cfg.RecoveryMessage(), 2},
		{"third abandonment -> dead_letter", Candidate{CreatedAt: old, Lease: lease(old), RecoveryCount: NumberAttempts(2)}, true, StatusDeadLetter, cfg.ExhaustedMessage(), 3},
		{"recoveryCount text '2' coerces like JS Number()", Candidate{CreatedAt: old, RecoveryCount: Attempts{Kind: AttemptsString, Text: "2"}}, true, StatusDeadLetter, cfg.ExhaustedMessage(), 3},
		{"recoveryCount null -> 0", Candidate{CreatedAt: old, RecoveryCount: Attempts{Kind: AttemptsNull}, Attempts: NumberAttempts(0)}, true, StatusPending, cfg.RecoveryMessage(), 1},
		{"recoveryCount garbage -> NaN||0", Candidate{CreatedAt: old, RecoveryCount: Attempts{Kind: AttemptsString, Text: "abc"}, Attempts: NumberAttempts(0)}, true, StatusPending, cfg.RecoveryMessage(), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.c.Status = StatusProcessing
			tc.c.TokenNullOrMissing = tc.c.ProcessingToken == ""
			d := Decide(cfg, now, tc.c)
			if d.Eligible != tc.eligible || d.To != tc.to {
				t.Fatalf("got eligible=%v to=%q (%s), want %v %q", d.Eligible, d.To, d.Reason, tc.eligible, tc.to)
			}
			if !tc.eligible {
				return
			}
			if d.Message != tc.msg || d.Recoveries != tc.recoveries {
				t.Fatalf("got msg=%q recoveries=%v, want %q %v", d.Message, d.Recoveries, tc.msg, tc.recoveries)
			}
			if d.Exhausted != (tc.to == StatusDeadLetter) {
				t.Fatalf("exhausted=%v", d.Exhausted)
			}
			want := CodeRecovered
			if d.Exhausted {
				want = CodeExhausted
			}
			if d.Code != want {
				t.Fatalf("code %q", d.Code)
			}
		})
	}
}

func TestV2OwnerlessEventsWaitForTheLegacyFence(t *testing.T) {
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	old := Candidate{Status: StatusProcessing, CreatedAt: now.Add(-3 * time.Hour), Attempts: NumberAttempts(0), TokenNullOrMissing: true}
	nullLease := old
	nullLease.Lease = LeaseField{Present: true, Null: true}
	for name, fence := range map[string]time.Time{"no fence": {}, "fence in the future": now.Add(time.Minute)} {
		cfg := V2Config()
		cfg.LegacyFence = fence
		for _, c := range []Candidate{old, nullLease} {
			if d := Decide(cfg, now, c); d.Eligible || d.Reason != "legacy_awaiting_fence" {
				t.Fatalf("%s: ownerless event released: %+v", name, d)
			}
		}
	}
	cfg := V2Config()
	cfg.LegacyFence = now.Add(-time.Hour)
	for _, c := range []Candidate{old, nullLease} {
		if d := Decide(cfg, now, c); !d.Eligible || d.Message != cfg.RecoveryMessage() {
			t.Fatalf("fence passed: %+v", d)
		}
	}
	// Created after the fence: not released even though older than 15 min.
	recent := old
	recent.CreatedAt = now.Add(-30 * time.Minute)
	if d := Decide(cfg, now, recent); d.Eligible {
		t.Fatalf("event created after the fence released: %+v", d)
	}
	// A token without a lease is owned by someone: never ownerless.
	tok := old
	tok.TokenNullOrMissing, tok.ProcessingToken = false, "tok"
	if d := Decide(cfg, now, tok); d.Eligible {
		t.Fatalf("event with token released: %+v", d)
	}
}

func TestProtocolParsingAndV2Validation(t *testing.T) {
	for in, want := range map[string]Protocol{"v1": ProtocolV1, "v2": ProtocolV2} {
		if p, err := ParseProtocol(in); err != nil || p != want {
			t.Fatalf("%s: %v %v", in, p, err)
		}
	}
	if _, err := ParseProtocol("v3"); err == nil {
		t.Fatal("v3 must be rejected")
	}
	c := V2Config()
	c.MaxRecoveries = 0
	if c.Validate() == nil {
		t.Fatal("v2 with MaxRecoveries 0 must be invalid")
	}
}
