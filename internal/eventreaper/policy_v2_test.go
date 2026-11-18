package eventreaper

import (
	"testing"
	"time"
)

func TestDecideV2Table(t *testing.T) {
	cfg := V2Config()
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
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
