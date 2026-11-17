package events

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"kas-core-pilot/internal/metrics"
)

type fakeStats struct {
	mu    sync.Mutex
	calls int
	stats DeliveryStats
	err   error
}

func (f *fakeStats) DeliveryStats(context.Context, time.Time) (DeliveryStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.stats, f.err
}

func collect(c metrics.Collector) string {
	r := metrics.NewRegistry()
	r.Register(c)
	var b strings.Builder
	_, _ = r.WriteTo(&b)
	return b.String()
}

func TestStatsCollectorExposesAllStatesAndOnlyFixedLabels(t *testing.T) {
	src := &fakeStats{stats: DeliveryStats{ByState: map[string]int64{"pending": 4, "dead": 1}, OldestDuePendingAge: 90 * time.Second}}
	out := collect(NewStatsCollector(src, time.Minute, nil))
	for _, want := range []string{
		"kas_outbox_stats_up 1",
		`kas_outbox_deliveries{state="pending"} 4`,
		`kas_outbox_deliveries{state="in_flight"} 0`,
		`kas_outbox_deliveries{state="delivered"} 0`,
		`kas_outbox_deliveries{state="dead"} 1`,
		"kas_outbox_oldest_due_pending_age_seconds 90",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tenant") || strings.Contains(out, "consumer") {
		t.Fatalf("outbox metrics must not carry tenants or consumers:\n%s", out)
	}
}

func TestStatsCollectorQueriesTheDatabaseAtMostOncePerTTL(t *testing.T) {
	src := &fakeStats{stats: DeliveryStats{ByState: map[string]int64{}}}
	clock := &fakeClock{now: t0}
	c := NewStatsCollector(src, 10*time.Second, clock.Now)
	for i := 0; i < 5; i++ {
		collect(c)
	}
	if src.calls != 1 {
		t.Fatalf("calls within the ttl = %d, want 1", src.calls)
	}
	clock.Advance(11 * time.Second)
	collect(c)
	if src.calls != 2 {
		t.Fatalf("calls after the ttl = %d, want 2", src.calls)
	}
}

func TestStatsCollectorReportsFailureWithoutStaleOrLeakedValues(t *testing.T) {
	src := &fakeStats{err: errors.New("connect db.internal: password=secret")}
	out := collect(NewStatsCollector(src, time.Second, nil))
	if !strings.Contains(out, "kas_outbox_stats_up 0") || strings.Contains(out, "kas_outbox_deliveries") {
		t.Fatalf("failure output:\n%s", out)
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "db.internal") {
		t.Fatalf("failure output leaks details:\n%s", out)
	}
}

func TestDeliveryStatsFromPostgres(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	publishOne(t, store, "evt.m-1", "consumer-a")
	publishOne(t, store, "evt.m-2", "consumer-a")
	publishOne(t, store, "evt.m-3", "consumer-a")
	publishOne(t, store, "evt.m-4", "consumer-a")
	clock := &fakeClock{now: t0}
	// delivered, dead, in flight (claimed) and one still pending.
	worker := testWorker(t, store, clock, map[string]Handler{"consumer-a": HandlerFunc(func(_ context.Context, d Delivery) error {
		switch d.Event.EventID {
		case "evt.m-1":
			return nil
		case "evt.m-2":
			return ErrPermanent
		}
		return errors.New("retry later")
	})}, func(c *WorkerConfig) { c.BatchSize = 3 })
	if n, err := worker.RunOnce(ctx); err != nil || n != 3 {
		t.Fatalf("run = %d, %v", n, err)
	}
	// evt.m-3 was retried (pending again, due t0+2s); evt.m-4 was never claimed (pending, due t0).
	stats, err := store.DeliveryStats(ctx, t0.Add(90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ByState["delivered"] != 1 || stats.ByState["dead"] != 1 || stats.ByState["pending"] != 2 || stats.ByState["in_flight"] != 0 {
		t.Fatalf("by state = %v", stats.ByState)
	}
	if stats.OldestDuePendingAge != 90*time.Second {
		t.Fatalf("oldest due pending age = %v, want 90s (evt.m-4 has waited since t0)", stats.OldestDuePendingAge)
	}
	// Nothing due yet: the age is zero.
	if early, _ := store.DeliveryStats(ctx, t0.Add(-time.Hour)); early.OldestDuePendingAge != 0 {
		t.Fatalf("age before anything is due = %v", early.OldestDuePendingAge)
	}
	if _, err := store.Claim(ctx, "w", []string{"consumer-a"}, t0.Add(time.Hour), time.Minute, 5, 10); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.DeliveryStats(ctx, t0.Add(time.Hour)); again.ByState["in_flight"] != 2 {
		t.Fatalf("in_flight = %d, want 2", again.ByState["in_flight"])
	}
}
