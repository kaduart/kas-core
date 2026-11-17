package events

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"kas-core-pilot/internal/pgdb"
	"kas-core-pilot/internal/pgtest"
)

func testPuller(store PullStore, clock *fakeClock) *Puller {
	return NewPuller(store, PullConfig{MaxAttempts: 3, BaseBackoff: 2 * time.Second, MaxBackoff: 5 * time.Minute,
		Now: clock.Now, Jitter: func() float64 { return 0.5 }})
}

func publishFor(t *testing.T, store *PostgresStore, id, tenant, eventType string, consumers ...string) {
	t.Helper()
	e := mustEvent(t, id)
	e.TenantID, e.EventType = tenant, eventType
	if err := store.Publish(context.Background(), e, consumers); err != nil {
		t.Fatal(err)
	}
}

func TestClaimIsRestrictedToTheScopeTenantsAndTypes(t *testing.T) {
	store := openStore(t)
	publishFor(t, store, "evt.p-a-upd", "tenant-demo-a", "settings.updated", "pull-a")
	publishFor(t, store, "evt.p-b-upd", "tenant-demo-b", "settings.updated", "pull-a")
	publishFor(t, store, "evt.p-a-chk", "tenant-demo-a", "settings.checked", "pull-a")
	publishFor(t, store, "evt.p-other", "tenant-demo-a", "settings.updated", "pull-other")
	puller := testPuller(store, &fakeClock{now: t0})
	ctx := context.Background()

	got, err := puller.Claim(ctx, "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 0, 0)
	if err != nil || len(got) != 1 || got[0].Event.EventID != "evt.p-a-upd" || got[0].Attempt != 1 {
		t.Fatalf("claim = %+v, %v; want only tenant A settings.updated", got, err)
	}
	if got[0].Event.TenantID != "tenant-demo-a" || string(got[0].Event.Payload) == "" || len(got[0].LeaseToken) != 32 {
		t.Fatalf("delivery = %+v", got[0])
	}
	// Empty scope means nothing, never "unrestricted".
	if got, _ := puller.Claim(ctx, "pull-a", nil, []string{"settings.updated"}, 0, 0); len(got) != 0 {
		t.Fatalf("empty tenants returned %d deliveries", len(got))
	}
	if got, _ := puller.Claim(ctx, "pull-a", []string{"tenant-demo-a"}, nil, 0, 0); len(got) != 0 {
		t.Fatalf("empty types returned %d deliveries", len(got))
	}
	// Out-of-scope deliveries stay untouched.
	if n := count(t, store, `SELECT count(*) FROM outbox_deliveries WHERE status = 'pending' AND attempts = 0`); n != 3 {
		t.Fatalf("untouched pending deliveries = %d, want 3", n)
	}
	// Another consumer's delivery is never returned for pull-a.
	all, _ := puller.Claim(ctx, "pull-a", []string{"tenant-demo-a", "tenant-demo-b"}, []string{"settings.updated", "settings.checked"}, 50, 0)
	for _, d := range all {
		if d.Event.EventID == "evt.p-other" {
			t.Fatal("a consumer received another consumer's delivery")
		}
	}
	if len(all) != 2 {
		t.Fatalf("remaining in scope = %d, want 2", len(all))
	}
}

func TestClaimBatchAndLeaseLimits(t *testing.T) {
	store := openStore(t)
	puller := testPuller(store, &fakeClock{now: t0})
	for _, tc := range []struct {
		limit int
		lease time.Duration
	}{{-1, 0}, {51, 0}, {0, time.Second}, {0, 6 * time.Minute}, {0, -time.Second}} {
		if _, err := puller.Claim(context.Background(), "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, tc.limit, tc.lease); !errors.Is(err, ErrInvalidPullInput) {
			t.Fatalf("limit %d lease %v: error = %v, want ErrInvalidPullInput", tc.limit, tc.lease, err)
		}
	}
	for i := 0; i < 60; i++ {
		publishFor(t, store, fmt.Sprintf("evt.p-batch-%03d", i), "tenant-demo-a", "settings.updated", "pull-a")
	}
	got, err := puller.Claim(context.Background(), "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 50, 0)
	if err != nil || len(got) != 50 {
		t.Fatalf("batch of 50 = %d, %v", len(got), err)
	}
	if rest, _ := puller.Claim(context.Background(), "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 0, 0); len(rest) != 10 {
		t.Fatalf("second claim = %d, want the remaining 10", len(rest))
	}
}

func TestAckRequiresTheLeaseTokenConsumerAndAttempt(t *testing.T) {
	store := openStore(t)
	publishFor(t, store, "evt.p-ack", "tenant-demo-a", "settings.updated", "pull-a")
	puller := testPuller(store, &fakeClock{now: t0})
	ctx := context.Background()
	got, _ := puller.Claim(ctx, "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 0, 0)
	d := got[0]
	badToken := "00000000000000000000000000000000"
	if err := puller.Ack(ctx, "pull-a", d.DeliveryID, d.Attempt, badToken); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("ack with a foreign token = %v", err)
	}
	if err := puller.Ack(ctx, "pull-other", d.DeliveryID, d.Attempt, d.LeaseToken); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("ack by another consumer = %v", err)
	}
	if err := puller.Ack(ctx, "pull-a", d.DeliveryID, d.Attempt+1, d.LeaseToken); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("ack with the wrong attempt = %v", err)
	}
	if err := puller.Ack(ctx, "pull-a", 0, 1, d.LeaseToken); !errors.Is(err, ErrInvalidPullInput) {
		t.Fatalf("invalid input = %v", err)
	}
	if err := puller.Ack(ctx, "pull-a", d.DeliveryID, d.Attempt, "short"); !errors.Is(err, ErrInvalidPullInput) {
		t.Fatalf("malformed token = %v", err)
	}
	if r := row(t, store, "pull-a"); r.Status != "in_flight" {
		t.Fatalf("failed acks must not change the delivery: %+v", r)
	}
	if err := puller.Ack(ctx, "pull-a", d.DeliveryID, d.Attempt, d.LeaseToken); err != nil {
		t.Fatalf("ack = %v", err)
	}
	if err := puller.Ack(ctx, "pull-a", d.DeliveryID, d.Attempt, d.LeaseToken); err != nil {
		t.Fatalf("a retried ack must succeed: %v", err)
	}
	if r := row(t, store, "pull-a"); r.Status != "delivered" || r.DeliveredAt == nil || r.Locked {
		t.Fatalf("row = %+v", r)
	}
	if more, _ := puller.Claim(ctx, "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 0, 0); len(more) != 0 {
		t.Fatal("an acknowledged delivery must not be returned again")
	}
}

func TestNackAppliesBackoffThenBuriesAtMaxAttempts(t *testing.T) {
	store := openStore(t)
	publishFor(t, store, "evt.p-nack", "tenant-demo-a", "settings.updated", "pull-a")
	clock := &fakeClock{now: t0}
	puller := testPuller(store, clock)
	ctx := context.Background()
	claim := func() ClaimedDelivery {
		t.Helper()
		got, err := puller.Claim(ctx, "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 0, 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("claim = %d, %v", len(got), err)
		}
		return got[0]
	}
	d := claim()
	res, err := puller.Nack(ctx, "pull-a", d.DeliveryID, d.Attempt, d.LeaseToken, false)
	if err != nil || res.Status != "pending" || !res.NextAttemptAt.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("nack 1 = %+v, %v", res, err)
	}
	if r := row(t, store, "pull-a"); r.Status != "pending" || r.ErrorCode != "consumer_nack" || r.Locked || !r.NextAttempt.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("row after nack 1 = %+v", r)
	}
	if again, _ := puller.Claim(ctx, "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 0, 0); len(again) != 0 {
		t.Fatal("a nacked delivery must wait for its backoff")
	}
	clock.Advance(2 * time.Second)
	d = claim()
	if d.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", d.Attempt)
	}
	res, _ = puller.Nack(ctx, "pull-a", d.DeliveryID, d.Attempt, d.LeaseToken, false)
	if !res.NextAttemptAt.Equal(t0.Add(2 * time.Second).Add(4 * time.Second)) {
		t.Fatalf("backoff must double: %+v", res)
	}
	clock.Advance(4 * time.Second)
	d = claim()
	res, err = puller.Nack(ctx, "pull-a", d.DeliveryID, d.Attempt, d.LeaseToken, false)
	if err != nil || res.Status != "dead" {
		t.Fatalf("nack at the last attempt = %+v, %v", res, err)
	}
	if r := row(t, store, "pull-a"); r.Status != "dead" || r.ErrorCode != "max_attempts" || r.DeadAt == nil || r.Attempts != 3 {
		t.Fatalf("row = %+v", r)
	}
	if got := count(t, store, "SELECT count(*) FROM outbox_deliveries"); got != 1 {
		t.Fatal("dead rows must be kept")
	}
}

func TestPermanentNackBuriesImmediatelyAndLateNackIsRejected(t *testing.T) {
	store := openStore(t)
	publishFor(t, store, "evt.p-perm", "tenant-demo-a", "settings.updated", "pull-a")
	puller := testPuller(store, &fakeClock{now: t0})
	ctx := context.Background()
	d := mustClaimOne(t, puller)
	res, err := puller.Nack(ctx, "pull-a", d.DeliveryID, d.Attempt, d.LeaseToken, true)
	if err != nil || res.Status != "dead" {
		t.Fatalf("permanent nack = %+v, %v", res, err)
	}
	if r := row(t, store, "pull-a"); r.Status != "dead" || r.ErrorCode != "consumer_permanent" || r.Attempts != 1 {
		t.Fatalf("row = %+v", r)
	}
	if _, err := puller.Nack(ctx, "pull-a", d.DeliveryID, d.Attempt, d.LeaseToken, false); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("second nack = %v, want ErrLeaseLost", err)
	}
}

func mustClaimOne(t *testing.T, p *Puller) ClaimedDelivery {
	t.Helper()
	got, err := p.Claim(context.Background(), "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 0, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim = %d, %v", len(got), err)
	}
	return got[0]
}

func TestExpiredPullLeaseIsReclaimedAndTheLateConsumerIsRejected(t *testing.T) {
	store := openStore(t)
	publishFor(t, store, "evt.p-lease", "tenant-demo-a", "settings.updated", "pull-a")
	clock := &fakeClock{now: t0}
	puller := testPuller(store, clock)
	ctx := context.Background()
	first := mustClaimOne(t, puller)
	if first.LeaseExpiresAt != t0.Add(60*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("lease_expires_at = %s", first.LeaseExpiresAt)
	}
	clock.Advance(30 * time.Second)
	if again, _ := puller.Claim(ctx, "pull-a", []string{"tenant-demo-a"}, []string{"settings.updated"}, 0, 0); len(again) != 0 {
		t.Fatal("a delivery under a live lease must not be handed out twice")
	}
	clock.Advance(31 * time.Second)
	second := mustClaimOne(t, puller)
	if second.Attempt != 2 || second.LeaseToken == first.LeaseToken || second.DeliveryID != first.DeliveryID {
		t.Fatalf("reclaim = %+v after %+v", second, first)
	}
	if err := puller.Ack(ctx, "pull-a", first.DeliveryID, first.Attempt, first.LeaseToken); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("late consumer ack = %v, want ErrLeaseLost", err)
	}
	if err := puller.Ack(ctx, "pull-a", second.DeliveryID, second.Attempt, second.LeaseToken); err != nil {
		t.Fatalf("current holder ack = %v", err)
	}
}

func TestNewConsumersAreNotBackfilledWithEarlierEvents(t *testing.T) {
	store := openStore(t)
	registry := &fakeRegistry{types: map[string]bool{"settings.updated": true}, consumers: []string{"pull-a"}}
	service := NewService(store, registry)
	service.now = func() time.Time { return t0 }
	ctx := context.Background()
	if _, err := service.Publish(ctx, "fono-node-local", testSubmission("evt.p-old")); err != nil {
		t.Fatal(err)
	}
	// A consumer is added to the configuration (after a restart) and a new event is published.
	registry.consumers = []string{"pull-a", "pull-new"}
	if _, err := service.Publish(ctx, "fono-node-local", testSubmission("evt.p-new")); err != nil {
		t.Fatal(err)
	}
	puller := testPuller(store, &fakeClock{now: t0})
	got, err := puller.Claim(ctx, "pull-new", []string{"tenant-demo-a"}, []string{"settings.updated"}, 50, 0)
	if err != nil || len(got) != 1 || got[0].Event.EventID != "evt.p-new" {
		t.Fatalf("the new consumer must only see events published after it was configured: %+v, %v", got, err)
	}
}

// --- purge ---

func insertAged(t *testing.T, store *PostgresStore, id string, receivedAgo time.Duration, status string, settledAgo time.Duration) {
	t.Helper()
	ctx := context.Background()
	received := time.Now().UTC().Add(-receivedAgo)
	var pk int64
	if err := store.pool.QueryRow(ctx, `INSERT INTO outbox_events (contract_version, event_id, tenant_id, service_id, event_type,
		correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at, payload)
		VALUES ('v1',$1,'tenant-demo-a','svc','settings.updated','c','x','y',$2,$2,'{}'::jsonb) RETURNING id`, id, received).Scan(&pk); err != nil {
		t.Fatal(err)
	}
	if status == "" {
		return // an event with no deliveries
	}
	settled := time.Now().UTC().Add(-settledAgo)
	if _, err := store.pool.Exec(ctx, `INSERT INTO outbox_deliveries (event_pk, consumer_id, status, attempts, next_attempt_at, created_at, delivered_at, dead_at)
		VALUES ($1,'c1',$2,1,$3,$3, CASE WHEN $2='delivered' THEN $4::timestamptz END, CASE WHEN $2='dead' THEN $4::timestamptz END)`,
		pk, status, received, settled); err != nil {
		t.Fatal(err)
	}
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestPurgeAppliesRetentionWindowsAndKeepsEverythingElse(t *testing.T) {
	dsn := pgtest.SchemaDSN(t)
	store, err := OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insertAged(t, store, "e.del-old", days(40), "delivered", days(31))    // purged, event follows
	insertAged(t, store, "e.del-new", days(40), "delivered", days(29))    // kept
	insertAged(t, store, "e.dead-old", days(120), "dead", days(91))       // purged, event follows
	insertAged(t, store, "e.dead-mid", days(120), "dead", days(89))       // kept: dead is kept 90 days
	insertAged(t, store, "e.pending-ancient", days(200), "pending", 0)    // never purged
	insertAged(t, store, "e.inflight-ancient", days(200), "in_flight", 0) // never purged
	insertAged(t, store, "e.nodeliv-old", days(31), "", 0)                // event without deliveries, past 30 days
	insertAged(t, store, "e.nodeliv-new", days(5), "", 0)                 // kept
	ctx := context.Background()

	dry, err := PurgePostgres(ctx, dsn, time.Now(), DeliveredRetention, DeadRetention, true)
	if err != nil || !dry.DryRun || dry.Delivered != 1 || dry.Dead != 1 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_events"); n != 8 {
		t.Fatalf("a dry run must delete nothing; events = %d", n)
	}

	res, err := PurgePostgres(ctx, dsn, time.Now(), DeliveredRetention, DeadRetention, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Delivered != 1 || res.Dead != 1 || res.Events != 3 {
		t.Fatalf("purge = %+v, want 1 delivered, 1 dead, 3 events (del-old, dead-old, nodeliv-old)", res)
	}
	rows, err := store.pool.Query(ctx, "SELECT event_id FROM outbox_events ORDER BY event_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var left []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	want := []string{"e.dead-mid", "e.del-new", "e.inflight-ancient", "e.nodeliv-new", "e.pending-ancient"}
	if fmt.Sprint(left) != fmt.Sprint(want) {
		t.Fatalf("events left = %v, want %v", left, want)
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_deliveries"); n != 4 {
		t.Fatalf("deliveries left = %d, want 4", n)
	}
	again, err := PurgePostgres(ctx, dsn, time.Now(), DeliveredRetention, DeadRetention, false)
	if err != nil || again.Delivered+again.Dead+again.Events != 0 {
		t.Fatalf("a second purge must find nothing: %+v, %v", again, err)
	}
}

func TestPurgeHandlesMoreRowsThanOneBatch(t *testing.T) {
	dsn := pgtest.SchemaDSN(t)
	store, err := OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `INSERT INTO outbox_events (contract_version, event_id, tenant_id, service_id, event_type,
		correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at, payload)
		SELECT 'v1','bulk-'||g,'tenant-demo-a','svc','settings.updated','c','x','y',now()-interval '60 days',now()-interval '60 days','{}'::jsonb
		FROM generate_series(1, 2500) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO outbox_deliveries (event_pk, consumer_id, status, attempts, next_attempt_at, created_at, delivered_at)
		SELECT id, 'c1', 'delivered', 1, received_at, received_at, now()-interval '40 days' FROM outbox_events`); err != nil {
		t.Fatal(err)
	}
	res, err := PurgePostgres(ctx, dsn, time.Now(), DeliveredRetention, DeadRetention, false)
	if err != nil || res.Delivered != 2500 || res.Events != 2500 {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_events"); n != 0 {
		t.Fatalf("events left = %d", n)
	}
}

func TestPurgeRejectsInvalidInput(t *testing.T) {
	if _, err := PurgePostgres(context.Background(), "postgres://u:p@127.0.0.1:1/x", time.Now(), 0, DeadRetention, true); err == nil {
		t.Fatal("zero retention accepted")
	}
	if _, err := PurgePostgres(context.Background(), "postgres://u:segredo-ficticio@db.example.com/x?sslmode=disable", time.Now(), DeliveredRetention, DeadRetention, true); !errors.Is(err, pgdb.ErrTLSRequired) {
		t.Fatalf("plaintext remote purge must be refused: %v", err)
	}
}
