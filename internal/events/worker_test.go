package events

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kas-core-pilot/internal/configstore"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testWorker(t *testing.T, store DeliveryStore, clock *fakeClock, handlers map[string]Handler, mutate func(*WorkerConfig)) *Worker {
	t.Helper()
	cfg := WorkerConfig{
		LeaseDuration: 60 * time.Second, HandlerTimeout: 10 * time.Second, BatchSize: 10, MaxAttempts: 3,
		BaseBackoff: 2 * time.Second, MaxBackoff: 5 * time.Minute,
		Now: clock.Now, Jitter: func() float64 { return 0.5 }, // factor 1.0: exact delays
		Logger: discardLogger(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	w, err := NewWorker(store, handlers, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

type deliveryRow struct {
	Status, ErrorCode   string
	Attempts            int
	NextAttempt         time.Time
	Locked              bool
	DeliveredAt, DeadAt *time.Time
}

func row(t *testing.T, store *PostgresStore, consumer string) deliveryRow {
	t.Helper()
	var r deliveryRow
	var code *string
	var lockedBy *string
	err := store.pool.QueryRow(context.Background(), `SELECT status, attempts, next_attempt_at, last_error_code, locked_by, delivered_at, dead_at
		FROM outbox_deliveries WHERE consumer_id = $1`, consumer).Scan(&r.Status, &r.Attempts, &r.NextAttempt, &code, &lockedBy, &r.DeliveredAt, &r.DeadAt)
	if err != nil {
		t.Fatal(err)
	}
	if code != nil {
		r.ErrorCode = *code
	}
	r.Locked = lockedBy != nil
	return r
}

func publishOne(t *testing.T, store *PostgresStore, id string, consumers ...string) {
	t.Helper()
	if err := store.Publish(context.Background(), mustEvent(t, id), consumers); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerDeliversAndDoesNotRedeliver(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-1", "consumer-a")
	var seen []Delivery
	handler := HandlerFunc(func(_ context.Context, d Delivery) error { seen = append(seen, d); return nil })
	clock := &fakeClock{now: t0}
	w := testWorker(t, store, clock, map[string]Handler{"consumer-a": handler}, nil)

	if n, err := w.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("first run = %d, %v", n, err)
	}
	if len(seen) != 1 || seen[0].Attempt != 1 || seen[0].Event.EventID != "evt.w-1" || seen[0].ConsumerID != "consumer-a" ||
		string(seen[0].Event.Payload) != `{"enabled":true,"setting_key":"logging.max_meta_keys","version":3}` {
		t.Fatalf("handler saw %+v", seen)
	}
	r := row(t, store, "consumer-a")
	if r.Status != "delivered" || r.Attempts != 1 || r.DeliveredAt == nil || r.Locked {
		t.Fatalf("row = %+v", r)
	}
	if n, _ := w.RunOnce(context.Background()); n != 0 || len(seen) != 1 {
		t.Fatalf("a delivered row must not be claimed again: %d claims, %d calls", n, len(seen))
	}
}

func TestWorkerRetriesWithExponentialBackoffThenSucceeds(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-retry", "consumer-a")
	var calls int
	handler := HandlerFunc(func(context.Context, Delivery) error {
		calls++
		if calls < 3 {
			return errors.New("temporary failure with details that must not be stored")
		}
		return nil
	})
	clock := &fakeClock{now: t0}
	w := testWorker(t, store, clock, map[string]Handler{"consumer-a": handler}, nil)
	ctx := context.Background()

	w.RunOnce(ctx)
	r := row(t, store, "consumer-a")
	if r.Status != "pending" || r.Attempts != 1 || !r.NextAttempt.Equal(t0.Add(2*time.Second)) || r.ErrorCode != "handler_error" || r.Locked {
		t.Fatalf("after attempt 1: %+v", r)
	}
	if n, _ := w.RunOnce(ctx); n != 0 {
		t.Fatal("a retry must not run before it is due")
	}
	clock.Advance(2 * time.Second)
	w.RunOnce(ctx)
	r = row(t, store, "consumer-a")
	if r.Attempts != 2 || !r.NextAttempt.Equal(t0.Add(2*time.Second+4*time.Second)) {
		t.Fatalf("after attempt 2: %+v (backoff must double)", r)
	}
	clock.Advance(4 * time.Second)
	w.RunOnce(ctx)
	r = row(t, store, "consumer-a")
	if r.Status != "delivered" || r.Attempts != 3 || calls != 3 {
		t.Fatalf("after attempt 3: %+v, calls %d", r, calls)
	}
}

func TestWorkerBuriesAfterMaxAttemptsKeepingTheRow(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-dead", "consumer-a")
	handler := HandlerFunc(func(context.Context, Delivery) error { return errors.New("always fails") })
	clock := &fakeClock{now: t0}
	w := testWorker(t, store, clock, map[string]Handler{"consumer-a": handler}, nil)
	for i := 0; i < 3; i++ {
		if n, err := w.RunOnce(context.Background()); err != nil || n != 1 {
			t.Fatalf("run %d = %d, %v", i+1, n, err)
		}
		clock.Advance(10 * time.Minute)
	}
	r := row(t, store, "consumer-a")
	if r.Status != "dead" || r.Attempts != 3 || r.ErrorCode != "max_attempts" || r.DeadAt == nil || r.Locked {
		t.Fatalf("row = %+v", r)
	}
	if n, _ := w.RunOnce(context.Background()); n != 0 {
		t.Fatal("a dead delivery must not be claimed again")
	}
	if got := count(t, store, "SELECT count(*) FROM outbox_deliveries"); got != 1 {
		t.Fatal("dead rows must be kept")
	}
}

func TestWorkerPermanentFailureGoesStraightToDead(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-perm", "consumer-a")
	handler := HandlerFunc(func(context.Context, Delivery) error { return fmt.Errorf("bad input: %w", ErrPermanent) })
	w := testWorker(t, store, &fakeClock{now: t0}, map[string]Handler{"consumer-a": handler}, nil)
	w.RunOnce(context.Background())
	if r := row(t, store, "consumer-a"); r.Status != "dead" || r.Attempts != 1 || r.ErrorCode != "handler_permanent" {
		t.Fatalf("row = %+v", r)
	}
}

func TestWorkerSurvivesHandlerPanicAndRetries(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-panic", "consumer-a")
	handler := HandlerFunc(func(context.Context, Delivery) error { panic("boom with details") })
	w := testWorker(t, store, &fakeClock{now: t0}, map[string]Handler{"consumer-a": handler}, nil)
	if n, err := w.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("run = %d, %v", n, err)
	}
	if r := row(t, store, "consumer-a"); r.Status != "pending" || r.ErrorCode != "handler_panic" || r.Locked {
		t.Fatalf("row = %+v", r)
	}
}

func TestWorkerHandlerTimeoutIsRecordedAndRetried(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-timeout", "consumer-a")
	handler := HandlerFunc(func(ctx context.Context, _ Delivery) error { <-ctx.Done(); return ctx.Err() })
	w := testWorker(t, store, &fakeClock{now: t0}, map[string]Handler{"consumer-a": handler}, func(c *WorkerConfig) { c.HandlerTimeout = 50 * time.Millisecond })
	w.RunOnce(context.Background())
	if r := row(t, store, "consumer-a"); r.Status != "pending" || r.ErrorCode != "handler_timeout" {
		t.Fatalf("row = %+v", r)
	}
}

func TestExpiredLeaseIsReclaimedAndTheLateWorkerCannotSettle(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-lease", "consumer-a")
	ctx := context.Background()
	clock := &fakeClock{now: t0}

	// Worker A claims and "crashes": it never settles.
	claimedByA, err := store.Claim(ctx, "worker-a", []string{"consumer-a"}, t0, 60*time.Second, 3, 10)
	if err != nil || len(claimedByA) != 1 || claimedByA[0].Attempt != 1 {
		t.Fatalf("A claim = %+v, %v", claimedByA, err)
	}
	var calls []int
	handler := HandlerFunc(func(_ context.Context, d Delivery) error { calls = append(calls, d.Attempt); return nil })
	b := testWorker(t, store, clock, map[string]Handler{"consumer-a": handler}, nil)

	clock.Advance(30 * time.Second)
	if n, _ := b.RunOnce(ctx); n != 0 {
		t.Fatal("a delivery under a live lease must not be claimed")
	}
	clock.Advance(31 * time.Second) // lease (60s) has expired
	if n, _ := b.RunOnce(ctx); n != 1 || len(calls) != 1 || calls[0] != 2 {
		t.Fatalf("B must reclaim as attempt 2; claimed %d, calls %v", n, calls)
	}
	if r := row(t, store, "consumer-a"); r.Status != "delivered" {
		t.Fatalf("row = %+v", r)
	}
	// A wakes up late: its lease is gone, so it must not overwrite the outcome.
	if applied, err := store.Complete(ctx, "worker-a", claimedByA[0], clock.Now()); err != nil || applied {
		t.Fatalf("late settle by A = %v, %v; want not applied", applied, err)
	}
	if applied, _ := store.Retry(ctx, "worker-a", claimedByA[0], clock.Now(), "x"); applied {
		t.Fatal("late retry by A must not apply")
	}
	if applied, _ := store.Bury(ctx, "worker-a", claimedByA[0], clock.Now(), "x"); applied {
		t.Fatal("late bury by A must not apply")
	}
	if r := row(t, store, "consumer-a"); r.Status != "delivered" {
		t.Fatalf("late worker changed the row: %+v", r)
	}
}

func TestExpiredLeaseWithNoAttemptsLeftBecomesDeadWithoutCallingTheHandler(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-expired-max", "consumer-a")
	ctx := context.Background()
	if _, err := store.Claim(ctx, "worker-a", []string{"consumer-a"}, t0, 60*time.Second, 1, 10); err != nil {
		t.Fatal(err)
	}
	called := false
	clock := &fakeClock{now: t0.Add(2 * time.Minute)}
	w := testWorker(t, store, clock, map[string]Handler{"consumer-a": HandlerFunc(func(context.Context, Delivery) error { called = true; return nil })},
		func(c *WorkerConfig) { c.MaxAttempts = 1 })
	if n, _ := w.RunOnce(ctx); n != 0 || called {
		t.Fatalf("claimed %d, called %v", n, called)
	}
	if r := row(t, store, "consumer-a"); r.Status != "dead" || r.ErrorCode != "lease_expired" || r.DeadAt == nil {
		t.Fatalf("row = %+v", r)
	}
}

func TestWorkerOnlyClaimsConsumersItHandles(t *testing.T) {
	store := openStore(t)
	publishOne(t, store, "evt.w-scope", "consumer-a", "consumer-b")
	w := testWorker(t, store, &fakeClock{now: t0}, map[string]Handler{"consumer-a": HandlerFunc(func(context.Context, Delivery) error { return nil })}, nil)
	w.RunOnce(context.Background())
	if r := row(t, store, "consumer-a"); r.Status != "delivered" {
		t.Fatalf("a = %+v", r)
	}
	if r := row(t, store, "consumer-b"); r.Status != "pending" || r.Attempts != 0 {
		t.Fatalf("b must be untouched: %+v", r)
	}
}

func TestConcurrentWorkersNeverHandleTheSameDeliveryTwice(t *testing.T) {
	store := openStore(t)
	const events = 60
	for i := 0; i < events; i++ {
		publishOne(t, store, fmt.Sprintf("evt.w-conc-%03d", i), "consumer-a")
	}
	var mu sync.Mutex
	handled := map[string]int{}
	handler := HandlerFunc(func(_ context.Context, d Delivery) error {
		mu.Lock()
		handled[d.Event.EventID]++
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		return nil
	})
	clock := &fakeClock{now: t0}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		w := testWorker(t, store, clock, map[string]Handler{"consumer-a": handler}, func(c *WorkerConfig) { c.BatchSize = 4 })
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, err := w.RunOnce(context.Background())
				if err != nil {
					t.Errorf("run: %v", err)
					return
				}
				if n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(handled) != events {
		t.Fatalf("handled %d distinct events, want %d", len(handled), events)
	}
	for id, n := range handled {
		if n != 1 {
			t.Fatalf("%s handled %d times; SKIP LOCKED must prevent double claims", id, n)
		}
	}
	if got := count(t, store, "SELECT count(*) FROM outbox_deliveries WHERE status = 'delivered' AND attempts = 1"); got != events {
		t.Fatalf("delivered = %d", got)
	}
}

func TestRunProcessesDeliveriesAndStopsAfterInFlightHandlersFinish(t *testing.T) {
	store := openStore(t)
	ev := mustEvent(t, "evt.w-run")
	ev.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.Publish(context.Background(), ev, []string{"consumer-a"}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var finished atomic.Bool
	handler := HandlerFunc(func(context.Context, Delivery) error {
		close(started)
		<-release
		finished.Store(true)
		return nil
	})
	w, err := NewWorker(store, map[string]Handler{"consumer-a": handler}, WorkerConfig{PollInterval: 10 * time.Millisecond, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	cancel() // shutdown while the handler is running
	select {
	case <-done:
		t.Fatal("Run returned while a handler was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("handler did not finish")
	}
	if r := row(t, store, "consumer-a"); r.Status != "delivered" {
		t.Fatalf("outcome must be recorded even after cancellation: %+v", r)
	}
}

func TestFanOutRespectsConsumerTenantsThroughTheConfiguration(t *testing.T) {
	const config = `{"contract_version":"v1","config_version":"c","tenant_registry":[{"id":"tenant-demo-a","status":"active"},{"id":"tenant-demo-b","status":"active"}],
	"key_schema":{"k.v":{"type":"integer"}},"tenants":{"tenant-demo-a":{"k.v":1}},
	"event_types":["settings.updated"],
	"event_consumers":{"consumer-a":{"event_types":["settings.updated"],"tenants":["tenant-demo-a"]},"consumer-ab":{"event_types":["settings.updated"],"tenants":["tenant-demo-a","tenant-demo-b"]}},
	"callers":{"fono-node-local":{"token_env":"T","tenants":["tenant-demo-a","tenant-demo-b"],"capabilities":["events:publish"]}}}`
	cfg, err := configstore.Load(strings.NewReader(config), func(string) (string, bool) { return strings.Repeat("a", 64), true })
	if err != nil {
		t.Fatal(err)
	}
	store := openStore(t)
	service := NewService(store, cfg)
	ctx := context.Background()
	if _, err := service.Publish(ctx, "fono-node-local", testSubmission("evt.fan-a")); err != nil {
		t.Fatal(err)
	}
	other := testSubmission("evt.fan-b")
	other.TenantID = "tenant-demo-b"
	if _, err := service.Publish(ctx, "fono-node-local", other); err != nil {
		t.Fatal(err)
	}
	rows, err := store.pool.Query(ctx, `SELECT e.tenant_id, d.consumer_id FROM outbox_deliveries d JOIN outbox_events e ON e.id = d.event_pk ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var tenant, consumer string
		if err := rows.Scan(&tenant, &consumer); err != nil {
			t.Fatal(err)
		}
		got = append(got, tenant+"/"+consumer)
	}
	if want := "tenant-demo-a/consumer-a,tenant-demo-a/consumer-ab,tenant-demo-b/consumer-ab"; strings.Join(got, ",") != want {
		t.Fatalf("deliveries = %v, want %s (a consumer must never receive another tenant's events)", got, want)
	}
}

// --- no database needed ---

func TestBackoffDoublesCapsAndJitters(t *testing.T) {
	base, max := 2*time.Second, 5*time.Minute
	for attempt, want := range map[int]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 3: 8 * time.Second, 4: 16 * time.Second} {
		if got := Backoff(attempt, base, max, 0.5); got != want {
			t.Fatalf("Backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
	if got := Backoff(30, base, max, 0.99); got != max {
		t.Fatalf("Backoff must be capped at max, got %v", got)
	}
	if low, high := Backoff(3, base, max, 0), Backoff(3, base, max, 0.999999); low != 6400*time.Millisecond || high < 9500*time.Millisecond || high > 9600*time.Millisecond {
		t.Fatalf("jitter range for attempt 3 = [%v, %v], want about 6.4s to 9.6s", low, high)
	}
	if Backoff(0, base, max, 0.5) != base {
		t.Fatal("attempts below 1 are treated as 1")
	}
}

type nopStore struct{ DeliveryStore }

func TestNewWorkerValidatesConfiguration(t *testing.T) {
	ok := map[string]Handler{"c": HandlerFunc(func(context.Context, Delivery) error { return nil })}
	cases := map[string]struct {
		handlers map[string]Handler
		mutate   func(*WorkerConfig)
	}{
		"no handlers":             {nil, nil},
		"nil handler":             {map[string]Handler{"c": nil}, nil},
		"timeout not below lease": {ok, func(c *WorkerConfig) { c.LeaseDuration = 5 * time.Second; c.HandlerTimeout = 5 * time.Second }},
		"batch too large":         {ok, func(c *WorkerConfig) { c.BatchSize = 101 }},
		"negative batch":          {ok, func(c *WorkerConfig) { c.BatchSize = -1 }},
		"negative attempts":       {ok, func(c *WorkerConfig) { c.MaxAttempts = -1 }},
		"base backoff above max":  {ok, func(c *WorkerConfig) { c.BaseBackoff = time.Hour; c.MaxBackoff = time.Minute }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := WorkerConfig{}
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			if _, err := NewWorker(nopStore{}, tc.handlers, cfg); err == nil {
				t.Fatal("invalid worker configuration was accepted")
			}
		})
	}
	if _, err := NewWorker(nil, ok, WorkerConfig{}); err == nil {
		t.Fatal("nil store accepted")
	}
	w, err := NewWorker(nopStore{}, ok, WorkerConfig{})
	if err != nil || w.ID() == "" {
		t.Fatalf("defaults must produce a worker with an ID: %v", err)
	}
}
