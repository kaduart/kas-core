package audit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
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
	if applied != 1 {
		t.Fatalf("migrations recorded = %d, want 1", applied)
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
