// Package pgtest provides an isolated PostgreSQL schema for integration tests.
// Tests run only when KAS_TEST_DATABASE_URL points to a disposable local
// database; otherwise they are skipped.
package pgtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BaseURL returns KAS_TEST_DATABASE_URL or skips the test.
func BaseURL(t testing.TB) string {
	t.Helper()
	base := os.Getenv("KAS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("KAS_TEST_DATABASE_URL is not set; skipping PostgreSQL integration tests")
	}
	return base
}

// SchemaDSN creates a unique schema and returns a connection string whose
// search_path points to it. The schema is dropped when the test ends.
func SchemaDSN(t testing.TB) string {
	t.Helper()
	base := BaseURL(t)
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
