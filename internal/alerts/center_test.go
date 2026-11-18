package alerts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func setup(cfg Config) (*Center, *SimulatedSink, *clock) {
	clk := &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	sink := &SimulatedSink{}
	return New(cfg, sink, clk.now), sink, clk
}

func cfg() Config { return Config{DedupWindow: time.Hour, Cooldown: 15 * time.Minute, MaxSubjects: 3} }

func exhausted(id string) Alert {
	return Alert{Source: "outbox-recovery", Kind: "entry_exhausted", Subject: id, Severity: Critical, Summary: "gave up " + id}
}

func must(t *testing.T, got Decision, err error, want Decision) {
	t.Helper()
	if want == DeliveryFailed {
		if got != want || err == nil {
			t.Fatalf("want %s with error, got %s %v", want, got, err)
		}
		return
	}
	if err != nil || got != want {
		t.Fatalf("want %s, got %s %v", want, got, err)
	}
}

func TestFirstAlertIsDeliveredRepeatIsFolded(t *testing.T) {
	c, sink, clk := setup(cfg())
	ctx := context.Background()
	d, err := c.Raise(ctx, exhausted("e1"))
	must(t, d, err, Delivered)
	clk.add(time.Minute)
	d, err = c.Raise(ctx, exhausted("e1"))
	must(t, d, err, Duplicate)
	if n := len(sink.Delivered()); n != 1 {
		t.Fatalf("delivered %d", n)
	}
}

func TestDistinctSubjectsDuringCooldownAreAggregatedNotLost(t *testing.T) {
	c, sink, clk := setup(cfg())
	ctx := context.Background()
	c.Raise(ctx, exhausted("e1"))
	for i := 2; i <= 6; i++ {
		clk.add(time.Minute)
		d, err := c.Raise(ctx, exhausted(fmt.Sprintf("e%d", i)))
		must(t, d, err, Deferred)
	}
	c.Raise(ctx, exhausted("e3")) // repeat inside the window
	if err := c.Flush(ctx); err != nil || len(sink.Delivered()) != 1 {
		t.Fatalf("flush before cooldown must not deliver: %v %d", err, len(sink.Delivered()))
	}
	clk.add(15 * time.Minute)
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got := sink.Delivered()
	if len(got) != 2 {
		t.Fatalf("want 2 notifications, got %d", len(got))
	}
	n := got[1]
	if n.Alerts != 5 || len(n.Subjects) != 3 || n.Omitted != 2 || n.Duplicates != 1 {
		t.Fatalf("aggregate wrong: %+v", n)
	}
	if c.Pending() != 0 {
		t.Fatal("pending after flush")
	}
}

func TestEscalationBypassesCooldown(t *testing.T) {
	c, sink, clk := setup(cfg())
	ctx := context.Background()
	warn := Alert{Source: "outbox", Kind: "pending_stale", Subject: "dispatcher", Severity: Warning, Summary: "10 pending"}
	c.Raise(ctx, warn)
	clk.add(time.Minute)
	crit := warn
	crit.Subject, crit.Severity, crit.Summary = "dispatcher-2", Critical, "5000 pending"
	d, err := c.Raise(ctx, crit)
	must(t, d, err, Delivered)
	got := sink.Delivered()
	if len(got) != 2 || !got[1].Escalation || got[1].Severity != "critical" {
		t.Fatalf("escalation not delivered: %+v", got)
	}
	clk.add(time.Minute)
	again := crit
	again.Subject = "dispatcher-3"
	d, err = c.Raise(ctx, again)
	must(t, d, err, Deferred) // same severity as last sent: cooldown applies
}

func TestFailedDeliveryKeepsAlertAndRetries(t *testing.T) {
	c, sink, clk := setup(cfg())
	ctx := context.Background()
	sink.FailNext = 2
	d, err := c.Raise(ctx, exhausted("e1"))
	must(t, d, err, DeliveryFailed)
	if !errors.Is(err, ErrSimulatedFailure) || c.Pending() != 1 {
		t.Fatalf("alert must stay pending: %v %d", err, c.Pending())
	}
	clk.add(time.Minute)
	d, err = c.Raise(ctx, exhausted("e2")) // no cooldown started: tries again, fails
	must(t, d, err, DeliveryFailed)
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got := sink.Delivered()
	if len(got) != 1 || got[0].Alerts != 2 || sink.Attempts != 3 {
		t.Fatalf("retry wrong: %+v attempts=%d", got, sink.Attempts)
	}
}

func TestDedupWindowExpires(t *testing.T) {
	c, sink, clk := setup(cfg())
	ctx := context.Background()
	c.Raise(ctx, exhausted("e1"))
	clk.add(61 * time.Minute)
	c.Flush(ctx)
	d, err := c.Raise(ctx, exhausted("e1"))
	must(t, d, err, Delivered)
	if len(sink.Delivered()) != 2 {
		t.Fatal("expected redelivery after window")
	}
}

func TestGroupsAreIndependent(t *testing.T) {
	c, sink, _ := setup(cfg())
	ctx := context.Background()
	c.Raise(ctx, exhausted("e1"))
	d, err := c.Raise(ctx, Alert{Source: "event-reaper", Kind: "cycle_error", Subject: "cycle", Severity: Warning, Summary: "x"})
	must(t, d, err, Delivered)
	if len(sink.Delivered()) != 2 {
		t.Fatal("other group must not be in cooldown")
	}
}

func TestInvalidAlert(t *testing.T) {
	c, _, _ := setup(cfg())
	if _, err := c.Raise(context.Background(), Alert{Kind: "k", Severity: Info}); !errors.Is(err, ErrInvalidAlert) {
		t.Fatal(err)
	}
	if _, err := c.Raise(context.Background(), Alert{Source: "s", Kind: "k"}); !errors.Is(err, ErrInvalidAlert) {
		t.Fatal(err)
	}
}

func TestConcurrentRaisesOneNotificationPerGroupWindow(t *testing.T) {
	c, sink, clk := setup(cfg())
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); c.Raise(ctx, exhausted(fmt.Sprintf("e%d", i%50))) }(i)
	}
	wg.Wait()
	if len(sink.Delivered()) != 1 {
		t.Fatalf("burst must produce one notification, got %d", len(sink.Delivered()))
	}
	clk.add(15 * time.Minute)
	c.Flush(ctx)
	got := sink.Delivered()
	total := got[0].Alerts
	if len(got) == 2 {
		total += got[1].Alerts
	}
	if total != 50 {
		t.Fatalf("50 distinct alerts must be covered, got %d", total)
	}
	s := c.Stats()
	if s[Duplicate] != 150 {
		t.Fatalf("150 duplicates expected, got %v", s)
	}
}
