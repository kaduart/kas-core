package events

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"kas-core-pilot/internal/pgtest"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *PostgresStore {
	t.Helper()
	store, err := OpenPostgres(context.Background(), pgtest.SchemaDSN(t))
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustEvent(t *testing.T, id string) Event {
	t.Helper()
	payload, err := ValidateSubmission(testSubmission(id))
	if err != nil {
		t.Fatal(err)
	}
	s := testSubmission(id)
	return Event{
		ContractVersion: s.ContractVersion, EventID: s.EventID, TenantID: s.TenantID, ServiceID: s.ServiceID,
		EventType: s.EventType, CorrelationID: s.CorrelationID, OccurredAt: s.OccurredAt,
		ReceivedAt: t0.Format(time.RFC3339Nano), Payload: payload,
	}
}

func count(t *testing.T, store *PostgresStore, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPublishStoresEventAndOnePendingDeliveryPerConsumer(t *testing.T) {
	store := openStore(t)
	ev := mustEvent(t, "evt.pg-1")
	if err := store.Publish(context.Background(), ev, []string{"consumer-a", "consumer-b"}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_events"); n != 1 {
		t.Fatalf("events = %d", n)
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_deliveries WHERE status = 'pending' AND attempts = 0"); n != 2 {
		t.Fatalf("pending deliveries = %d, want 2", n)
	}
	var payload string
	if err := store.pool.QueryRow(context.Background(), "SELECT payload::text FROM outbox_events").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"version": 3`) {
		t.Fatalf("payload not stored as jsonb: %s", payload)
	}
	// An event with no consumers is stored with no deliveries.
	if err := store.Publish(context.Background(), mustEvent(t, "evt.pg-2"), nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_deliveries"); n != 2 {
		t.Fatalf("deliveries after event without consumers = %d, want 2", n)
	}
}

func TestPublishIsAtomicWhenDeliveryInsertFails(t *testing.T) {
	store := openStore(t)
	// A repeated consumer violates UNIQUE (event_pk, consumer_id) on the second insert.
	err := store.Publish(context.Background(), mustEvent(t, "evt.pg-atomic"), []string{"consumer-a", "consumer-a"})
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_events"); n != 0 {
		t.Fatalf("event must roll back with its deliveries, found %d", n)
	}
}

func TestPublishIdempotencyConflictAndIdentityScope(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	first := mustEvent(t, "evt.pg-idem")
	if err := store.Publish(ctx, first, []string{"consumer-a"}); err != nil {
		t.Fatal(err)
	}
	retry := first
	retry.ReceivedAt = t0.Add(time.Hour).Format(time.RFC3339Nano)
	var dup *DuplicateError
	if err := store.Publish(ctx, retry, []string{"consumer-a", "consumer-b"}); !errors.As(err, &dup) {
		t.Fatalf("identical resend = %v, want DuplicateError", err)
	}
	if dup.Stored.ReceivedAt != first.ReceivedAt || string(dup.Stored.Payload) != string(first.Payload) {
		t.Fatalf("duplicate must return the original event: %+v", dup.Stored)
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_deliveries"); n != 1 {
		t.Fatalf("a duplicate must not create deliveries; have %d", n)
	}
	changed := first
	changed.Payload = []byte(`{"version":4}`)
	if err := store.Publish(ctx, changed, nil); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("different content = %v, want ErrEventConflict", err)
	}
	otherTenant, otherService := first, first
	otherTenant.TenantID = "tenant-demo-b"
	otherService.ServiceID = "demo-node-b"
	for _, e := range []Event{otherTenant, otherService} {
		if err := store.Publish(ctx, e, nil); err != nil {
			t.Fatalf("same event_id under another tenant/service must be a new event: %v", err)
		}
	}
	if n := count(t, store, "SELECT count(*) FROM outbox_events"); n != 3 {
		t.Fatalf("events = %d, want 3", n)
	}
}

func TestConcurrentIdenticalPublishesCreateOneEventAndOneDeliverySet(t *testing.T) {
	store := openStore(t)
	const n = 20
	var created, duplicates atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := mustEvent(t, "evt.pg-race")
			e.ReceivedAt = t0.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
			var dup *DuplicateError
			switch err := store.Publish(context.Background(), e, []string{"consumer-a", "consumer-b"}); {
			case err == nil:
				created.Add(1)
			case errors.As(err, &dup):
				duplicates.Add(1)
			default:
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if created.Load() != 1 || duplicates.Load() != n-1 {
		t.Fatalf("created=%d duplicates=%d", created.Load(), duplicates.Load())
	}
	if e, d := count(t, store, "SELECT count(*) FROM outbox_events"), count(t, store, "SELECT count(*) FROM outbox_deliveries"); e != 1 || d != 2 {
		t.Fatalf("events=%d deliveries=%d, want 1 and 2", e, d)
	}
}

func TestMigrationsAreIdempotentVerifiableAndSeparateFromAudit(t *testing.T) {
	dsn := pgtest.SchemaDSN(t)
	ctx := context.Background()
	if _, err := OpenPostgresVerified(ctx, dsn); err == nil || !strings.Contains(err.Error(), "migration role") {
		t.Fatalf("verified open on empty schema = %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := MigratePostgres(ctx, dsn); err != nil {
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
	}
	store, err := OpenPostgresVerified(ctx, dsn)
	if err != nil {
		t.Fatalf("verified open after migration: %v", err)
	}
	defer store.Close()
	if n := count(t, store, "SELECT count(*) FROM events_schema_migrations"); n != 1 {
		t.Fatalf("tracked migrations = %d, want 1", n)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO outbox_events (contract_version, event_id, tenant_id, service_id, event_type,
		correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at, payload)
		VALUES ('v1','e','t','s','a.b','c','x','y',now(),now(),'[]'::jsonb)`); err == nil {
		t.Fatal("the database must reject a payload that is not a JSON object")
	}
}

func TestReadinessAndClosedStore(t *testing.T) {
	store := openStore(t)
	if err := store.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(context.Background()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("closed store ready = %v", err)
	}
	if err := store.Publish(context.Background(), mustEvent(t, "evt.closed"), nil); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("publish on closed store = %v", err)
	}
}

// The runtime role: INSERT/SELECT on outbox_events, INSERT/SELECT/UPDATE on
// outbox_deliveries, never DELETE. Skipped when the test login cannot create roles.
func TestRuntimeRoleHasLeastPrivilege(t *testing.T) {
	base := pgtest.BaseURL(t)
	dsn := pgtest.SchemaDSN(t)
	ctx := context.Background()
	if err := MigratePostgres(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var schema, database string
	if err := admin.QueryRow(ctx, "SELECT current_schema(), current_database()").Scan(&schema, &database); err != nil {
		t.Fatal(err)
	}
	role := fmt.Sprintf("kas_ev_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'rt_senha_ficticia'", role)); err != nil {
		t.Skipf("test login cannot create roles: %v", err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{
			fmt.Sprintf("REVOKE ALL ON %s.outbox_events FROM %s", schema, role),
			fmt.Sprintf("REVOKE ALL ON %s.outbox_deliveries FROM %s", schema, role),
			fmt.Sprintf("REVOKE ALL ON %s.events_schema_migrations FROM %s", schema, role),
			fmt.Sprintf("REVOKE ALL ON SCHEMA %s FROM %s", schema, role),
			fmt.Sprintf("DROP ROLE IF EXISTS %s", role),
		} {
			if _, err := admin.Exec(ctx, stmt); err != nil {
				t.Errorf("cleanup %q: %v", stmt, err)
			}
		}
	})
	for _, grant := range []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, role),
		fmt.Sprintf("GRANT INSERT, SELECT ON %s.outbox_events TO %s", schema, role),
		fmt.Sprintf("GRANT INSERT, SELECT, UPDATE ON %s.outbox_deliveries TO %s", schema, role),
		fmt.Sprintf("GRANT SELECT ON %s.events_schema_migrations TO %s", schema, role),
	} {
		if _, err := admin.Exec(ctx, grant); err != nil {
			t.Fatalf("%s: %v", grant, err)
		}
	}
	u, _ := url.Parse(base)
	u.User = url.UserPassword(role, "rt_senha_ficticia")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	store, err := OpenPostgresVerified(ctx, u.String())
	if err != nil {
		t.Fatalf("runtime role must open in verified mode: %v", err)
	}
	defer store.Close()
	if err := store.Publish(ctx, mustEvent(t, "evt.rt-1"), []string{"consumer-a"}); err != nil {
		t.Fatalf("runtime role must publish: %v", err)
	}
	var dup *DuplicateError
	if err := store.Publish(ctx, mustEvent(t, "evt.rt-1"), []string{"consumer-a"}); !errors.As(err, &dup) {
		t.Fatalf("runtime role must detect duplicates: %v", err)
	}
	worker, err := NewWorker(store, map[string]Handler{"consumer-a": HandlerFunc(func(context.Context, Delivery) error { return nil })},
		WorkerConfig{Now: func() time.Time { return t0 }, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("runtime role must claim and settle deliveries: %d, %v", n, err)
	}
	if got := count(t, store, "SELECT count(*) FROM outbox_deliveries WHERE status = 'delivered'"); got != 1 {
		t.Fatalf("delivered = %d", got)
	}
	for _, stmt := range []string{
		"UPDATE outbox_events SET event_type = 'x.y'",
		"DELETE FROM outbox_events",
		"DELETE FROM outbox_deliveries",
		"TRUNCATE outbox_deliveries",
		"DROP TABLE outbox_deliveries",
		"CREATE TABLE intruder (id int)",
		"UPDATE events_schema_migrations SET name = 'x'",
	} {
		if _, err := store.pool.Exec(ctx, stmt); err == nil {
			t.Fatalf("runtime role must not be able to run: %s", stmt)
		}
	}
}
