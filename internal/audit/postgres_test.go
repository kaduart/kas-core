package audit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests run only when KAS_TEST_DATABASE_URL points to a disposable
// local PostgreSQL database. Each test creates and drops its own schema.
func postgresTestDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv("KAS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("KAS_TEST_DATABASE_URL is not set; skipping PostgreSQL integration tests")
	}
	admin, err := pgxpool.New(context.Background(), base)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	schema := fmt.Sprintf("kas_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func openTestPostgres(t *testing.T, dsn string) *PostgresStore {
	t.Helper()
	store, err := OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testEvent(id string) Event {
	return Event{
		ContractVersion: ContractVersion, EventID: id, TenantID: "tenant-demo-a",
		ServiceID: "fono-node-local", Actor: "user.demo-01", Action: "settings.updated",
		Resource: "setting.max_meta_keys", Result: "success", CorrelationID: "corr.demo-01",
		OccurredAt: "2026-11-16T12:00:00.123456789Z", ReceivedAt: "2026-11-16T12:00:01.987654321Z",
	}
}

func TestPostgresAppendPreservesEventFields(t *testing.T) {
	store := openTestPostgres(t, postgresTestDSN(t))
	want := testEvent("evt.pg-01")
	if err := store.Append(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	var got Event
	err := store.pool.QueryRow(context.Background(), `SELECT contract_version, event_id, tenant_id,
		service_id, actor, action, resource, result, correlation_id, occurred_at_raw, received_at_raw
		FROM audit_events WHERE event_id = $1`, want.EventID).Scan(&got.ContractVersion, &got.EventID,
		&got.TenantID, &got.ServiceID, &got.Actor, &got.Action, &got.Resource, &got.Result,
		&got.CorrelationID, &got.OccurredAt, &got.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("stored event differs:\n got %+v\nwant %+v", got, want)
	}
}

func TestPostgresMigrationIsIdempotentAndConcurrentSafe(t *testing.T) {
	dsn := postgresTestDSN(t)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := OpenPostgres(context.Background(), dsn)
			if err != nil {
				errs <- err
				return
			}
			errs <- s.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent open/migrate: %v", err)
		}
	}
	store := openTestPostgres(t, dsn)
	var applied int
	if err := store.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if want := 2; applied != want {
		t.Fatalf("migrations recorded = %d, want %d", applied, want)
	}
}

func TestPostgresConcurrentAppendsAreAllStored(t *testing.T) {
	store := openTestPostgres(t, postgresTestDSN(t))
	const n = 60
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- store.Append(context.Background(), testEvent(fmt.Sprintf("evt.pg-c%03d", i)))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := store.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("stored = %d, want %d", count, n)
	}
}

func TestPostgresRejectsInvalidEventsAndClosedStore(t *testing.T) {
	store, err := OpenPostgres(context.Background(), postgresTestDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	bad := testEvent("evt.pg-bad")
	bad.Result = "maybe"
	if err := store.Append(context.Background(), bad); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid event error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Append(cancelled, testEvent("evt.pg-ctx")); err == nil {
		t.Fatal("cancelled context must not append")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := store.Append(context.Background(), testEvent("evt.pg-closed")); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("closed store error = %v", err)
	}
}

func TestPostgresServiceMapsDatabaseFailureWithoutLeakingDetails(t *testing.T) {
	store, err := OpenPostgres(context.Background(), postgresTestDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	if _, err := service.Record(context.Background(), "fono-node-local", testSubmission("evt.pg-svc")); err != nil {
		t.Fatal(err)
	}
	// Simulate a database fault: remove the table underneath the running store.
	if _, err := store.pool.Exec(context.Background(), "DROP TABLE audit_events"); err != nil {
		t.Fatal(err)
	}
	_, err = service.Record(context.Background(), "fono-node-local", testSubmission("evt.pg-svc-2"))
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("database failure error = %v, want ErrStoreUnavailable", err)
	}
	if strings.Contains(err.Error(), "audit_events") {
		t.Fatalf("error leaks database detail: %v", err)
	}
	_ = store.Close()
}

func TestOpenPostgresRejectsBadInputWithoutLeakingSecrets(t *testing.T) {
	if _, err := OpenPostgres(context.Background(), ""); err == nil {
		t.Fatal("empty connection string accepted")
	}
	_, err := OpenPostgres(context.Background(), "postgres://kas:segredo-ficticio@127.0.0.1:1/x?connect_timeout=1")
	if err == nil {
		t.Fatal("unreachable database accepted")
	}
	if strings.Contains(err.Error(), "segredo-ficticio") {
		t.Fatalf("error leaks credentials: %v", err)
	}
}

func rowCount(t *testing.T, store *PostgresStore, where string, args ...any) int {
	t.Helper()
	var n int
	if err := store.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE "+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgresIdenticalResendIsDuplicateAndConflictIsRejected(t *testing.T) {
	store := openTestPostgres(t, postgresTestDSN(t))
	first := testEvent("evt.pg-idem")
	if err := store.Append(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	retry := first
	retry.ReceivedAt = "2026-11-17T09:00:00Z"
	var duplicate *DuplicateError
	if err := store.Append(context.Background(), retry); !errors.As(err, &duplicate) || duplicate.Stored != first {
		t.Fatalf("identical resend = %v (%+v), want DuplicateError with the original event", err, duplicate)
	}
	changed := first
	changed.Action = "settings.deleted"
	if err := store.Append(context.Background(), changed); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting content error = %v, want ErrEventConflict", err)
	}
	if n := rowCount(t, store, "event_id = $1", first.EventID); n != 1 {
		t.Fatalf("rows for identity = %d, want 1", n)
	}
	// Same event_id under another tenant or service is a different identity.
	otherTenant := first
	otherTenant.TenantID = "tenant-demo-b"
	otherService := first
	otherService.ServiceID = "demo-node-b"
	for _, e := range []Event{otherTenant, otherService} {
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("different identity rejected: %v", err)
		}
	}
	// A conflict must not break the store.
	if err := store.Append(context.Background(), testEvent("evt.pg-idem-2")); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresConcurrentIdenticalSubmissionsStoreOneRow(t *testing.T) {
	store := openTestPostgres(t, postgresTestDSN(t))
	const n = 25
	var created, duplicates atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := testEvent("evt.pg-race")
			e.ReceivedAt = time.Date(2026, 11, 17, 9, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
			var duplicate *DuplicateError
			switch err := store.Append(context.Background(), e); {
			case err == nil:
				created.Add(1)
			case errors.As(err, &duplicate):
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
		t.Fatalf("created=%d duplicates=%d, want 1 and %d", created.Load(), duplicates.Load(), n-1)
	}
	if rows := rowCount(t, store, "event_id = $1", "evt.pg-race"); rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
}

func TestPostgresReadiness(t *testing.T) {
	store, err := OpenPostgres(context.Background(), postgresTestDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	if err := service.Ready(context.Background()); err != nil {
		t.Fatalf("connected store must be ready: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(context.Background()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("closed store must not be ready: %v", err)
	}
}

func TestPostgresVerifiedOpenRequiresAppliedMigrations(t *testing.T) {
	dsn := postgresTestDSN(t)
	if _, err := OpenPostgresVerified(context.Background(), dsn); err == nil || !strings.Contains(err.Error(), "-audit-migrate-only") {
		t.Fatalf("verified open on an empty schema error = %v, want guidance to run migrations", err)
	}
	if err := MigratePostgres(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPostgresVerified(context.Background(), dsn)
	if err != nil {
		t.Fatalf("verified open after migration: %v", err)
	}
	_ = store.Close()
}

func TestPostgresMigration0002KeepsExistingRowsWhenDuplicatesExist(t *testing.T) {
	dsn := postgresTestDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	first, err := migrationFiles.ReadFile("migrations/0001_audit_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"CREATE TABLE audit_schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())",
		string(first),
		"INSERT INTO audit_schema_migrations (name) VALUES ('0001_audit_events.sql')",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	e := testEvent("evt.pg-legacy-dup")
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO audit_events (contract_version, event_id, tenant_id, service_id, actor, action,
			resource, result, correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now(),now())`, e.ContractVersion, e.EventID, e.TenantID, e.ServiceID,
			e.Actor, e.Action, e.Resource, e.Result, e.CorrelationID, e.OccurredAt, e.ReceivedAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := OpenPostgres(ctx, dsn); err == nil {
		t.Fatal("migration 0002 must fail visibly while duplicate identities exist")
	}
	var rows, migrations int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_events").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_schema_migrations").Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || migrations != 1 {
		t.Fatalf("failed migration must roll back without deleting data: rows=%d migrations=%d", rows, migrations)
	}
}

// The runtime role needs only INSERT and SELECT on audit_events plus SELECT on
// the migrations table. Skipped when the test login cannot create roles.
func TestPostgresRuntimeRoleWithInsertSelectOnly(t *testing.T) {
	base := os.Getenv("KAS_TEST_DATABASE_URL")
	dsn := postgresTestDSN(t)
	ctx := context.Background()
	if err := MigratePostgres(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close) // registered first so it runs after the role cleanup below
	var schema string
	if err := admin.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role := fmt.Sprintf("kas_rt_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'rt_senha_ficticia'", role)); err != nil {
		t.Skipf("test login cannot create roles: %v", err)
	}
	t.Cleanup(func() {
		// DROP OWNED needs more than CREATEROLE on PostgreSQL 16, so revoke the
		// grants made below explicitly before dropping the role.
		for _, revoke := range []string{
			fmt.Sprintf("REVOKE ALL ON %s.audit_events FROM %s", schema, role),
			fmt.Sprintf("REVOKE ALL ON %s.audit_schema_migrations FROM %s", schema, role),
			fmt.Sprintf("REVOKE ALL ON SCHEMA %s FROM %s", schema, role),
			fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM %s", currentDB(t, admin), role),
			fmt.Sprintf("DROP ROLE IF EXISTS %s", role),
		} {
			if _, err := admin.Exec(ctx, revoke); err != nil {
				t.Errorf("cleanup %q: %v", revoke, err)
			}
		}
	})
	for _, grant := range []string{
		fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", currentDB(t, admin), role),
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, role),
		fmt.Sprintf("GRANT INSERT, SELECT ON %s.audit_events TO %s", schema, role),
		fmt.Sprintf("GRANT SELECT ON %s.audit_schema_migrations TO %s", schema, role),
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
	first := testEvent("evt.pg-rt")
	if err := store.Append(ctx, first); err != nil {
		t.Fatalf("runtime role must insert: %v", err)
	}
	var duplicate *DuplicateError
	if err := store.Append(ctx, first); !errors.As(err, &duplicate) {
		t.Fatalf("runtime role must detect duplicates with SELECT: %v", err)
	}
	for _, stmt := range []string{
		"UPDATE audit_events SET result = 'denied'",
		"DELETE FROM audit_events",
		"DROP TABLE audit_events",
		"CREATE TABLE intruder (id int)",
	} {
		if _, err := store.pool.Exec(ctx, stmt); err == nil {
			t.Fatalf("runtime role must not be able to run: %s", stmt)
		}
	}
}

func currentDB(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var name string
	if err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}
