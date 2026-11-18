package crmalerts

import (
	"context"
	"testing"
	"time"

	"kas-core-pilot/internal/alerts"
	"kas-core-pilot/internal/eventreaper"
	"kas-core-pilot/internal/outboxrecovery"
)

func brt(y, mo, d, h, mi int) time.Time {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, FonoInovaHours().Location)
}

func TestSuspensionHours(t *testing.T) {
	h := FonoInovaHours()
	cases := map[time.Time]bool{
		brt(2026, 11, 18, 19, 9):  false, // quarta 19:09
		brt(2026, 11, 18, 19, 10): true,
		brt(2026, 11, 19, 7, 29):  true,
		brt(2026, 11, 19, 7, 30):  false,
		brt(2026, 11, 21, 12, 0):  true, // sábado
	}
	for at, want := range cases {
		if got := h.Suspended(at); got != want {
			t.Errorf("%s: want %v got %v", at, want, got)
		}
	}
}

func TestStalePendingSeverityFollowsHours(t *testing.T) {
	h := FonoInovaHours()
	cy := func(at time.Time, n int64) outboxrecovery.Cycle {
		return outboxrecovery.Cycle{At: at, Census: &outboxrecovery.Census{PendingStale: n}}
	}
	if a := OutboxCycle(cy(brt(2026, 11, 18, 22, 0), 900), h); a[0].Severity != alerts.Info {
		t.Fatalf("night: %+v", a)
	}
	if a := OutboxCycle(cy(brt(2026, 11, 18, 10, 0), 12), h); a[0].Severity != alerts.Warning {
		t.Fatalf("day small: %+v", a)
	}
	if a := OutboxCycle(cy(brt(2026, 11, 18, 10, 0), 900), h); a[0].Severity != alerts.Critical {
		t.Fatalf("day large: %+v", a)
	}
	if a := OutboxCycle(cy(brt(2026, 11, 18, 10, 0), 0), h); len(a) != 0 {
		t.Fatalf("nothing stale: %+v", a)
	}
}

func TestReaperCycle(t *testing.T) {
	rep := eventreaper.CycleReport{
		Err:      "candidates: timeout",
		Checks:   []eventreaper.ShadowCheck{{EventID: "ev1", Predicted: "failed", Observed: "pending", Verdict: "diverge"}, {EventID: "ev2", Verdict: "agree"}},
		Outcomes: []eventreaper.Outcome{{EventID: "ev3", Result: "error", Error: "x"}, {EventID: "ev4", Result: "applied"}},
	}
	got := ReaperCycle(rep)
	if len(got) != 3 || got[1].Kind != "shadow_diverge" || got[1].Severity != alerts.Critical {
		t.Fatalf("%+v", got)
	}
}

// Ponta a ponta com o centro e o destino simulado: 30 eventos abandonados no
// mesmo minuto viram 1 notificação imediata + 1 resumo depois do cooldown.
func TestEndToEndWithCenter(t *testing.T) {
	now := brt(2026, 11, 18, 10, 0)
	clk := func() time.Time { return now }
	sink := &alerts.SimulatedSink{}
	c := alerts.New(alerts.DefaultConfig(), sink, clk)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		c.Raise(ctx, OutboxExhausted(outboxrecovery.Alert{ID: "out" + string(rune('A'+i)), Reason: "max_attempts"}))
	}
	now = now.Add(16 * time.Minute)
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got := sink.Delivered()
	if len(got) != 2 || got[0].Alerts+got[1].Alerts != 30 {
		t.Fatalf("%+v", got)
	}
}
