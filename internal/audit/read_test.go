package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var readBase = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// readEvent builds a valid event for a tenant, received at readBase+offset.
func readEvent(tenant, id string, offset time.Duration, entityType, entityID string) Event {
	return Event{
		ContractVersion: ContractVersion, EventID: id, TenantID: tenant, ServiceID: "fono-node-local",
		Actor: "user.demo-01", Action: "settings.updated", Resource: "setting.x", Result: "success",
		CorrelationID: "corr.read", OccurredAt: "2026-11-18T11:00:00Z",
		ReceivedAt: readBase.Add(offset).Format(time.RFC3339Nano),
		EntityType: entityType, EntityID: entityID,
	}
}

// readableStore is what the shared suite needs from a store.
type readableStore interface {
	Repository
	Reader
}

func listAll(t *testing.T, s *Service, q Query) []Event {
	t.Helper()
	var all []Event
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatal("pagination did not terminate")
		}
		page, err := s.List(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Items...)
		if page.NextCursor == "" {
			return all
		}
		q.Cursor = page.NextCursor
	}
}

func ids(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.EventID
	}
	return out
}

// runReadSuite exercises reading through the Service against any store.
func runReadSuite(t *testing.T, store readableStore) {
	ctx := context.Background()
	service := NewService(store)
	for i := 0; i < 5; i++ {
		must(t, store.Append(ctx, readEvent("tenant-demo-a", fmt.Sprintf("a-%02d", i), time.Duration(i)*time.Second, "", "")))
		must(t, store.Append(ctx, readEvent("tenant-demo-b", fmt.Sprintf("b-%02d", i), time.Duration(i)*time.Second, "", "")))
	}

	t.Run("isolation between tenants", func(t *testing.T) {
		got := ids(listAll(t, service, Query{TenantID: "tenant-demo-a", Limit: 2}))
		want := []string{"a-00", "a-01", "a-02", "a-03", "a-04"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("tenant A read %v, want %v", got, want)
		}
		for _, e := range listAll(t, service, Query{TenantID: "tenant-demo-b"}) {
			if e.TenantID != "tenant-demo-b" {
				t.Fatalf("tenant B read a record of %s", e.TenantID)
			}
		}
		if empty := listAll(t, service, Query{TenantID: "tenant-demo-unknown"}); len(empty) != 0 {
			t.Fatalf("an unknown tenant must read nothing, got %d", len(empty))
		}
	})

	t.Run("a cursor from one tenant never exposes another", func(t *testing.T) {
		page, err := service.List(ctx, Query{TenantID: "tenant-demo-a", Limit: 2})
		if err != nil || page.NextCursor == "" {
			t.Fatalf("first page = %+v, %v", page, err)
		}
		// Reusing tenant A's cursor for tenant B still only yields tenant B records.
		for _, e := range listAll(t, service, Query{TenantID: "tenant-demo-b", Limit: 2, Cursor: page.NextCursor}) {
			if e.TenantID != "tenant-demo-b" {
				t.Fatalf("cross-tenant record via cursor: %s", e.TenantID)
			}
		}
	})

	t.Run("limits and defaults", func(t *testing.T) {
		for _, limit := range []int{-1, MaxReadLimit + 1, 1000} {
			if _, err := service.List(ctx, Query{TenantID: "tenant-demo-a", Limit: limit}); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("limit %d error = %v, want ErrInvalidQuery", limit, err)
			}
		}
		page, err := service.List(ctx, Query{TenantID: "tenant-demo-a", Limit: 3})
		if err != nil || len(page.Items) != 3 || page.NextCursor == "" {
			t.Fatalf("limit 3 = %d items cursor %q, %v", len(page.Items), page.NextCursor, err)
		}
		last, err := service.List(ctx, Query{TenantID: "tenant-demo-a", Limit: 5})
		if err != nil || len(last.Items) != 5 || last.NextCursor != "" {
			t.Fatalf("exactly one full page must not offer a cursor: %d %q, %v", len(last.Items), last.NextCursor, err)
		}
	})

	t.Run("pagination neither repeats nor skips, even with equal receipt times", func(t *testing.T) {
		// 40 events share the same receipt instant, so only the id breaks ties; 10 more arrive later.
		for i := 0; i < 40; i++ {
			must(t, store.Append(ctx, readEvent("tenant-demo-c", fmt.Sprintf("c-%03d", i), time.Hour, "", "")))
		}
		for i := 0; i < 10; i++ {
			must(t, store.Append(ctx, readEvent("tenant-demo-c", fmt.Sprintf("c-late-%02d", i), 2*time.Hour+time.Duration(i)*time.Microsecond, "", "")))
		}
		unpaged := ids(listAll(t, service, Query{TenantID: "tenant-demo-c", Limit: MaxReadLimit}))
		if len(unpaged) != 50 {
			t.Fatalf("expected 50 records, read %d", len(unpaged))
		}
		for _, size := range []int{1, 3, 7, 49, 50, 51} {
			paged := ids(listAll(t, service, Query{TenantID: "tenant-demo-c", Limit: size}))
			if fmt.Sprint(paged) != fmt.Sprint(unpaged) {
				t.Fatalf("page size %d changed the sequence:\n got %v\nwant %v", size, paged, unpaged)
			}
		}
		seen := map[string]bool{}
		for _, id := range unpaged {
			if seen[id] {
				t.Fatalf("record %s repeated", id)
			}
			seen[id] = true
		}
	})

	t.Run("order is by receipt time then id, regardless of arrival order", func(t *testing.T) {
		// Appended out of receipt order (a slow writer): reads must still be in time order.
		must(t, store.Append(ctx, readEvent("tenant-demo-d", "d-late", 30*time.Second, "", "")))
		must(t, store.Append(ctx, readEvent("tenant-demo-d", "d-early", 10*time.Second, "", "")))
		must(t, store.Append(ctx, readEvent("tenant-demo-d", "d-mid", 20*time.Second, "", "")))
		got := ids(listAll(t, service, Query{TenantID: "tenant-demo-d", Limit: 1}))
		if fmt.Sprint(got) != "[d-early d-mid d-late]" {
			t.Fatalf("order = %v", got)
		}
	})

	t.Run("filters", func(t *testing.T) {
		must(t, store.Append(ctx, readEvent("tenant-demo-e", "e-1", 1*time.Second, "appointment", "app-1")))
		must(t, store.Append(ctx, readEvent("tenant-demo-e", "e-2", 2*time.Second, "appointment", "app-2")))
		must(t, store.Append(ctx, readEvent("tenant-demo-e", "e-3", 3*time.Second, "appointment", "app-1")))
		must(t, store.Append(ctx, readEvent("tenant-demo-e", "e-4", 4*time.Second, "package", "app-1")))
		must(t, store.Append(ctx, readEvent("tenant-demo-e", "e-5", 5*time.Second, "", "")))
		check := func(name string, q Query, want string) {
			t.Helper()
			q.TenantID = "tenant-demo-e"
			if got := fmt.Sprint(ids(listAll(t, service, q))); got != want {
				t.Fatalf("%s: got %s, want %s", name, got, want)
			}
		}
		check("no filter", Query{}, "[e-1 e-2 e-3 e-4 e-5]")
		check("entity type", Query{EntityType: "appointment"}, "[e-1 e-2 e-3]")
		check("entity type and id", Query{EntityType: "appointment", EntityID: "app-1"}, "[e-1 e-3]")
		check("same id under another type", Query{EntityType: "package", EntityID: "app-1"}, "[e-4]")
		check("since is inclusive", Query{Since: readBase.Add(3 * time.Second)}, "[e-3 e-4 e-5]")
		check("since and entity", Query{Since: readBase.Add(2 * time.Second), EntityType: "appointment"}, "[e-2 e-3]")
		check("since in the future", Query{Since: readBase.Add(time.Hour * 24)}, "[]")
		check("unknown entity", Query{EntityType: "appointment", EntityID: "nope"}, "[]")
		for name, q := range map[string]Query{
			"entity id without a type": {TenantID: "tenant-demo-e", EntityID: "app-1"},
			"invalid entity type":      {TenantID: "tenant-demo-e", EntityType: "bad type"},
			"invalid entity id":        {TenantID: "tenant-demo-e", EntityType: "appointment", EntityID: "x y"},
		} {
			if _, err := service.List(ctx, q); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("%s: error = %v, want ErrInvalidQuery", name, err)
			}
		}
		got := listAll(t, service, Query{TenantID: "tenant-demo-e", EntityType: "appointment", EntityID: "app-2"})
		if len(got) != 1 || got[0].EntityType != "appointment" || got[0].EntityID != "app-2" {
			t.Fatalf("entity fields must be returned: %+v", got)
		}
	})

	t.Run("invalid cursors are rejected", func(t *testing.T) {
		for _, cursor := range []string{"!!!", "bm90LWEtY3Vyc29y", EncodeCursor(Position{}) + "x", string(make([]byte, 200))} {
			if _, err := service.List(ctx, Query{TenantID: "tenant-demo-a", Cursor: cursor}); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("cursor %.20q error = %v, want ErrInvalidCursor", cursor, err)
			}
		}
	})

	t.Run("reading is concurrent with writing", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(2)
			go func(i int) {
				defer wg.Done()
				_ = store.Append(ctx, readEvent("tenant-demo-f", fmt.Sprintf("f-%03d", i), time.Duration(i)*time.Millisecond, "", ""))
			}(i)
			go func() {
				defer wg.Done()
				_, _ = service.List(ctx, Query{TenantID: "tenant-demo-f", Limit: 3})
			}()
		}
		wg.Wait()
		if n := len(listAll(t, service, Query{TenantID: "tenant-demo-f", Limit: 5})); n != 8 {
			t.Fatalf("expected 8 records after concurrent writes, read %d", n)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestJSONLReadSuite(t *testing.T) {
	store, err := OpenJSONL(testDataDir(t))
	must(t, err)
	defer store.Close()
	runReadSuite(t, store)
}

func TestPostgresReadSuite(t *testing.T) {
	store := openTestPostgres(t, postgresTestDSN(t))
	runReadSuite(t, store)
}

func TestJSONLReadSurvivesRestartWithStableIDs(t *testing.T) {
	dir := testDataDir(t)
	store, err := OpenJSONL(dir)
	must(t, err)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		must(t, store.Append(ctx, readEvent("tenant-demo-a", fmt.Sprintf("r-%02d", i), time.Duration(i)*time.Second, "", "")))
	}
	first, err := NewService(store).List(ctx, Query{TenantID: "tenant-demo-a", Limit: 5})
	must(t, err)
	must(t, store.Close())

	reopened, err := OpenJSONL(dir)
	must(t, err)
	defer reopened.Close()
	service := NewService(reopened)
	// The cursor issued before the restart still continues exactly where it stopped.
	rest := ids(listAll(t, service, Query{TenantID: "tenant-demo-a", Limit: 5, Cursor: first.NextCursor}))
	want := []string{"r-05", "r-06", "r-07", "r-08", "r-09", "r-10", "r-11"}
	if fmt.Sprint(rest) != fmt.Sprint(want) {
		t.Fatalf("after restart continued with %v, want %v", rest, want)
	}
	// New records after the restart get later ids and come last.
	must(t, reopened.Append(ctx, readEvent("tenant-demo-a", "r-12", 12*time.Second, "", "")))
	all := ids(listAll(t, service, Query{TenantID: "tenant-demo-a", Limit: 200}))
	if len(all) != 13 || all[12] != "r-12" {
		t.Fatalf("records after restart = %v", all)
	}
}

func TestJSONLReadIgnoresHistoricalDuplicateLines(t *testing.T) {
	dir := testDataDir(t)
	store, err := OpenJSONL(dir)
	must(t, err)
	must(t, store.Append(context.Background(), readEvent("tenant-demo-a", "dup-1", time.Second, "", "")))
	must(t, store.Close())
	path := dir + "/audit.jsonl"
	existing := mustRead(t, path)
	mustWrite(t, path, append(existing, existing...)) // a file from before uniqueness existed
	reopened, err := OpenJSONL(dir)
	must(t, err)
	defer reopened.Close()
	if got := ids(listAll(t, NewService(reopened), Query{TenantID: "tenant-demo-a"})); fmt.Sprint(got) != "[dup-1]" {
		t.Fatalf("a duplicated identity must be read once, got %v", got)
	}
	// A new record is numbered after every existing line, duplicates included.
	must(t, reopened.Append(context.Background(), readEvent("tenant-demo-a", "dup-2", 2*time.Second, "", "")))
	if got := ids(listAll(t, NewService(reopened), Query{TenantID: "tenant-demo-a"})); fmt.Sprint(got) != "[dup-1 dup-2]" {
		t.Fatalf("records = %v", got)
	}
}

func TestEntityFieldsAreValidatedOnSubmission(t *testing.T) {
	s := testSubmission("evt.ent-1")
	s.EntityType, s.EntityID = "appointment", "app-1"
	if err := ValidateSubmission(s); err != nil {
		t.Fatalf("valid entity reference rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Submission){
		"type without id": func(s *Submission) { s.EntityID = "" },
		"id without type": func(s *Submission) { s.EntityType = "" },
		"bad type":        func(s *Submission) { s.EntityType = "bad type" },
		"free text id":    func(s *Submission) { s.EntityID = "Maria da Silva" },
	} {
		c := s
		mutate(&c)
		if err := ValidateSubmission(c); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s: error = %v, want ErrInvalidEvent", name, err)
		}
	}
	if err := ValidateSubmission(testSubmission("evt.ent-2")); err != nil {
		t.Fatalf("an event without an entity must stay valid: %v", err)
	}
}

func TestCursorRoundTripAndBounds(t *testing.T) {
	p := Position{RecordedMicros: 1763467200123456, ID: 987654321}
	got, err := DecodeCursor(EncodeCursor(p))
	if err != nil || got != p {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "AAAA", EncodeCursor(Position{ID: -1})} {
		if _, err := DecodeCursor(bad); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor %q error = %v", bad, err)
		}
	}
}

func TestListReportsUnsupportedStoreAndNilService(t *testing.T) {
	if _, err := NewService(appendOnly{}).List(context.Background(), Query{TenantID: "tenant-demo-a"}); !errors.Is(err, ErrReadUnsupported) {
		t.Fatalf("a store that cannot be read must say so: %v", err)
	}
	var nilService *Service
	if _, err := nilService.List(context.Background(), Query{TenantID: "tenant-demo-a"}); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("nil service: %v", err)
	}
}

type appendOnly struct{}

func (appendOnly) Append(context.Context, Event) error { return nil }

// --- PostgreSQL specifics: migration 0003 and the index that serves reads ---

func TestPostgresMigration0003AddsEntityColumnsAndReadIndexes(t *testing.T) {
	store := openTestPostgres(t, postgresTestDSN(t))
	ctx := context.Background()
	var applied int
	must(t, store.pool.QueryRow(ctx, "SELECT count(*) FROM audit_schema_migrations").Scan(&applied))
	if applied != 3 {
		t.Fatalf("migrations recorded = %d, want 3", applied)
	}
	for _, index := range []string{"audit_events_tenant_order_idx", "audit_events_tenant_entity_idx"} {
		var n int
		must(t, store.pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`, index).Scan(&n))
		if n != 1 {
			t.Fatalf("index %s is missing", index)
		}
	}
	// Both columns or neither: the database refuses a half-filled reference.
	if _, err := store.pool.Exec(ctx, `INSERT INTO audit_events (contract_version, event_id, tenant_id, service_id, actor, action,
		resource, result, correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at, entity_type)
		VALUES ('v1','half','t','s','user.a','a.b','r','success','c','x','y',now(),now(),'appointment')`); err == nil {
		t.Fatal("the database must reject an entity_type without an entity_id")
	}
}

func TestPostgresReadUsesTheReadIndexes(t *testing.T) {
	store := openTestPostgres(t, postgresTestDSN(t))
	ctx := context.Background()
	// One connection for every statement, so the SET below applies to the EXPLAINs.
	conn, err := store.pool.Acquire(ctx)
	must(t, err)
	defer conn.Release()
	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO audit_events (contract_version, event_id, tenant_id, service_id, actor, action,
		resource, result, correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at, entity_type, entity_id)
		SELECT 'v1','e'||g,'tenant-'||(g%20),'s','user.a','a.b','r','success','c','x','y',now(),now()+g*interval '1 second',
		       CASE WHEN g%3=0 THEN 'appointment' END, CASE WHEN g%3=0 THEN 'id-'||(g%7) END
		FROM generate_series(1, 6000) g`)
	exec("ANALYZE audit_events")
	// The planner may legitimately prefer a scan on tiny tables; disabling it proves the
	// indexes can serve the exact shape of the reads, in order, without a sort step.
	exec("SET enable_seqscan = off")
	plan := func(query string) string {
		t.Helper()
		rows, err := conn.Query(ctx, "EXPLAIN (COSTS OFF) "+query)
		must(t, err)
		defer rows.Close()
		var out string
		for rows.Next() {
			var line string
			must(t, rows.Scan(&line))
			out += line + "\n"
		}
		return out
	}
	ordered := plan(`SELECT id FROM audit_events WHERE tenant_id = 'tenant-3' AND (received_at, id) > (now(), 0) ORDER BY received_at, id LIMIT 51`)
	if !strings.Contains(ordered, "audit_events_tenant_order_idx") || strings.Contains(ordered, "Sort") {
		t.Fatalf("the unfiltered read must use the ordered index without a sort:\n%s", ordered)
	}
	byEntity := plan(`SELECT id FROM audit_events WHERE tenant_id = 'tenant-3' AND entity_type = 'appointment' AND entity_id = 'id-2' ORDER BY received_at, id LIMIT 51`)
	// A selective entity filter matches few rows, so the planner may sort them in memory;
	// what matters is that it is served by the entity index and not by a scan of the tenant.
	if !strings.Contains(byEntity, "audit_events_tenant_entity_idx") {
		t.Fatalf("the entity read must use the entity index:\n%s", byEntity)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := osReadFile(path)
	must(t, err)
	return data
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	must(t, osWriteFile(path, data))
}
