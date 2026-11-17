package pgmigrate

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"kas-core-pilot/internal/pgdb"
	"kas-core-pilot/internal/pgtest"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testMigrator(files map[string]string) Migrator {
	fsys := fstest.MapFS{}
	for name, sql := range files {
		fsys["m/"+name] = &fstest.MapFile{Data: []byte(sql)}
	}
	return Migrator{FS: fsys, Dir: "m", Table: "t_migrations", LockID: 4242}
}

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgdb.Open(context.Background(), "test", pgtest.SchemaDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestNamesAreSortedAndOnlySQL(t *testing.T) {
	m := testMigrator(map[string]string{"0002_b.sql": "", "0001_a.sql": "", "README.md": ""})
	names, err := m.Names()
	if err != nil || strings.Join(names, ",") != "0001_a.sql,0002_b.sql" {
		t.Fatalf("names = %v, %v", names, err)
	}
}

func TestInvalidMigratorIsRejected(t *testing.T) {
	if err := (Migrator{Table: "ok"}).validate(); err == nil {
		t.Fatal("missing file system accepted")
	}
	if err := (Migrator{FS: fstest.MapFS{}, Table: "bad name; drop"}).validate(); err == nil {
		t.Fatal("unsafe table name accepted")
	}
}

func TestApplyIsIdempotentOrderedAndVerifiable(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	m := testMigrator(map[string]string{
		"0001_a.sql": "CREATE TABLE a (id int)",
		"0002_b.sql": "CREATE TABLE b (id int REFERENCES a_missing_ok_if_dropped(id))",
	})
	// 0002 references a missing table: it must fail, roll back and leave 0001 applied.
	if err := m.Apply(ctx, pool); err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("failing migration error = %v", err)
	}
	var tracked int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM t_migrations").Scan(&tracked); err != nil || tracked != 1 {
		t.Fatalf("tracked after failure = %d, %v; want 1", tracked, err)
	}
	if err := m.Verify(ctx, pool); err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("verify must report the pending migration: %v", err)
	}
	fixed := testMigrator(map[string]string{
		"0001_a.sql": "CREATE TABLE a (id int)",
		"0002_b.sql": "CREATE TABLE b (id int)",
	})
	for i := 0; i < 2; i++ { // second run is a no-op
		if err := fixed.Apply(ctx, pool); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	if err := fixed.Verify(ctx, pool); err != nil {
		t.Fatalf("verify after apply: %v", err)
	}
}

func TestVerifyOnEmptySchemaPointsToMigrationRole(t *testing.T) {
	pool := openPool(t)
	err := testMigrator(map[string]string{"0001_a.sql": "SELECT 1"}).Verify(context.Background(), pool)
	if err == nil || !strings.Contains(err.Error(), "migration role") {
		t.Fatalf("verify on empty schema = %v", err)
	}
}

func TestConcurrentApplyRunsEachMigrationOnce(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	m := testMigrator(map[string]string{"0001_a.sql": "CREATE TABLE once (id int)"})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- m.Apply(ctx, pool) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent apply: %v", err)
		}
	}
	var tracked int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM t_migrations").Scan(&tracked); err != nil || tracked != 1 {
		t.Fatalf("tracked = %d, %v; want 1", tracked, err)
	}
}
