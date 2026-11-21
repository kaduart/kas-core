package scheduling

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"kas-core-pilot/internal/pgtest"
)

// Integration tests: they run against a real PostgreSQL (KAS_TEST_DATABASE_URL),
// each in its own schema, with fictitious data only.

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := pgtest.SchemaDSN(t)
	s, err := Open(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	return s, dsn
}

// widePool replaces the store's pool (8 connections in production) by one large
// enough for every concurrent request to hold its own connection.
func widePool(t *testing.T, s *Store, dsn string, size int32) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns, cfg.MinConns = size, size
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Open every connection before the race, so none is created inside it.
	conns := make([]*pgxpool.Conn, 0, size)
	for i := int32(0); i < size; i++ {
		c, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		c.Release()
	}
	old := s.pool
	s.pool = pool
	t.Cleanup(func() { pool.Close(); old.Close() })
}

const (
	tenantA = "clinica-aurora"
	tenantB = "escola-exemplo"
)

func seed(t *testing.T, s *Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutSettings(ctx, tenant, Settings{Zone: DefaultZone, Holidays: []string{"2027-01-01"}, ClientExclusive: true}))
	must(s.PutResource(ctx, tenant, Resource{ID: "sala-1", Kind: "room", Label: "Sala 1", Capacity: 1, Active: true}))
	must(s.PutResource(ctx, tenant, Resource{ID: "grupo-1", Kind: "room", Label: "Grupo", Capacity: 3, Active: true}))
	rules := []WeeklyRule{}
	for wd := 1; wd <= 5; wd++ {
		rules = append(rules, WeeklyRule{Weekday: wd, StartMin: 8 * 60, EndMin: 18 * 60})
	}
	must(s.PutWeeklyRules(ctx, tenant, "sala-1", rules))
	must(s.PutWeeklyRules(ctx, tenant, "grupo-1", rules))
	must(s.PutService(ctx, tenant, Service{ID: "sessao-40", Label: "Sessão 40 min", DurationMin: 40, BufferMin: 0}))
}

func book(resource, client, key, from, to string) NewBooking {
	return NewBooking{ResourceID: resource, ClientRef: client, IdempotencyKey: key, Actor: "teste",
		Start: at(from), End: at(to)}
}

func countRows(t *testing.T, s *Store, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrationIsReversible(t *testing.T) {
	s, dsn := openStore(t)
	ctx := context.Background()
	seed(t, s, tenantA)
	if _, _, err := s.CreateBooking(ctx, tenantA, book("sala-1", "cli-1", "k1", "2027-01-04 09:00", "2027-01-04 09:40")); err != nil {
		t.Fatal(err)
	}
	if s2, err := Open(ctx, dsn, false); err != nil {
		t.Fatalf("verify after apply: %v", err)
	} else {
		s2.Close()
	}
	if err := Rollback(ctx, dsn); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND (tablename LIKE 'sched%' OR tablename = 'scheduling_schema_migrations')`); n != 0 {
		t.Fatalf("%d scheduling tables left after rollback", n)
	}
	if _, err := Open(ctx, dsn, false); err == nil {
		t.Fatal("verify must fail after rollback")
	}
	if err := Rollback(ctx, dsn); err != nil {
		t.Fatalf("second rollback must be harmless: %v", err)
	}
	s3, err := Open(ctx, dsn, true)
	if err != nil {
		t.Fatalf("re-apply after rollback: %v", err)
	}
	s3.Close()
}

func TestTenantIsolation(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	seed(t, s, tenantA)
	seed(t, s, tenantB)
	a, _, err := s.CreateBooking(ctx, tenantA, NewBooking{ID: "bk-1", ResourceID: "sala-1", ClientRef: "cli-1", IdempotencyKey: "k1",
		Actor: "teste", Start: at("2027-01-04 09:00"), End: at("2027-01-04 09:40")})
	if err != nil {
		t.Fatal(err)
	}
	// Same booking id, same key, same resource id and same time in another tenant.
	if _, _, err := s.CreateBooking(ctx, tenantB, NewBooking{ID: "bk-1", ResourceID: "sala-1", ClientRef: "cli-1", IdempotencyKey: "k1",
		Actor: "teste", Start: at("2027-01-04 09:00"), End: at("2027-01-04 09:40")}); err != nil {
		t.Fatalf("tenant B must not see tenant A's ids or slots: %v", err)
	}
	if _, _, err := s.CreateBooking(ctx, tenantB, NewBooking{ID: "bk-2", ResourceID: "sala-1", ClientRef: "cli-9", IdempotencyKey: "k2",
		Actor: "teste", Start: at("2027-01-04 10:00"), End: at("2027-01-04 10:40")}); err != nil {
		t.Fatal(err)
	}
	from, to := at("2027-01-04 00:00"), at("2027-01-05 00:00")
	listA, _ := s.ListBookings(ctx, tenantA, "", from, to)
	listB, _ := s.ListBookings(ctx, tenantB, "", from, to)
	if len(listA) != 1 || len(listB) != 2 {
		t.Fatalf("lists leak across tenants: A=%d B=%d", len(listA), len(listB))
	}
	slotsA, err := s.Availability(ctx, tenantA, AvailabilityQuery{ResourceID: "sala-1", ServiceID: "sessao-40", From: "2027-01-04", To: "2027-01-04"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sl := range slotsA {
		if sl.Start.Equal(at("2027-01-04 10:00")) {
			continue
		}
		if sl.Start.Equal(at("2027-01-04 09:00")) {
			t.Fatal("A's own booking must hide 09:00")
		}
	}
	if _, err := s.Reschedule(ctx, "outra-empresa", a.ID, 1, at("2027-01-04 11:00"), at("2027-01-04 11:40"), "teste"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant must not move A's booking: %v", err)
	}
	if _, err := s.Cancel(ctx, "outra-empresa", a.ID, 1, "teste"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant must not cancel A's booking: %v", err)
	}
	conflicts, err := s.CheckConflicts(ctx, tenantA, []Candidate{{Key: "x", ResourceID: "sala-1", ClientRef: "cli-2",
		Start: at("2027-01-04 10:00"), End: at("2027-01-04 10:40")}})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("B's 10:00 booking must not conflict in A: %+v %v", conflicts, err)
	}
}

// Every statement of the store that touches a scheduling table filters or
// writes tenant_id. A new query without it fails here before review.
func TestEveryQueryIsScopedByTenant(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	queries := regexp.MustCompile("(?s)`([^`]*)`").FindAllStringSubmatch(string(src), -1)
	checked := 0
	for _, q := range queries {
		sql := q[1]
		if !regexp.MustCompile(`(?i)\b(FROM|INTO|UPDATE)\s+sched_`).MatchString(sql) {
			continue
		}
		checked++
		if !strings.Contains(sql, "tenant_id") {
			t.Errorf("query without tenant_id:\n%s", sql)
		}
	}
	if checked < 15 {
		t.Fatalf("only %d queries found; the scan is broken", checked)
	}
}

func TestSchemaHoldsNoPersonalData(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name LIKE 'sched\_%'`)
	if err != nil {
		t.Fatal(err)
	}
	personal := regexp.MustCompile(`(?i)name|phone|fone|email|mail|cpf|rg|birth|nasc|address|endere|note|obs|diagn|patient|paciente`)
	n := 0
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		n++
		if personal.MatchString(column) {
			t.Errorf("column %s.%s looks like personal data", table, column)
		}
	}
	rows.Close()
	if n < 30 {
		t.Fatalf("only %d columns read", n)
	}
	seed(t, s, tenantA)
	for _, ref := range []string{"maria@exemplo.com", "Maria Silva", "(61) 99999-0000", ""} {
		_, err := s.pool.Exec(ctx, `INSERT INTO sched_bookings (tenant_id, id, resource_id, client_ref, starts_at, ends_at, status,
			idempotency_key, exclusive, client_exclusive) VALUES ($1, 'x', 'sala-1', $2, now(), now() + interval '1 hour', 'scheduled', 'kx', true, true)`,
			tenantA, ref)
		if err == nil {
			t.Fatalf("database accepted client_ref %q", ref)
		}
	}
}

func TestAvailabilityFromDatabase(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	seed(t, s, tenantA)
	if _, _, err := s.CreateBooking(ctx, tenantA, book("sala-1", "cli-1", "k1", "2027-01-04 09:20", "2027-01-04 10:00")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddException(ctx, tenantA, "sala-1", Exception{Start: at("2027-01-04 12:00"), End: at("2027-01-04 14:00"), Reason: "off"}); err != nil {
		t.Fatal(err)
	}
	slots, err := s.Availability(ctx, tenantA, AvailabilityQuery{ResourceID: "sala-1", ServiceID: "sessao-40", From: "2027-01-01", To: "2027-01-04"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, sl := range slots {
		got[sl.Start.In(sp).Format("01-02 15:04")] = true
	}
	// 01-01 is a holiday, 01-02/03 a weekend; 09:20 is booked (08:40-09:20 only
	// touches it and stays open); 12:00 to 14:00 is closed.
	for _, closed := range []string{"01-01 08:00", "01-02 08:00", "01-03 08:00", "01-04 09:20", "01-04 12:00", "01-04 13:20"} {
		if got[closed] {
			t.Errorf("%s must not be offered", closed)
		}
	}
	for _, open := range []string{"01-04 08:00", "01-04 08:40", "01-04 10:00", "01-04 14:00", "01-04 17:20"} {
		if !got[open] {
			t.Errorf("%s must be offered", open)
		}
	}
	if _, err := s.Availability(ctx, tenantA, AvailabilityQuery{ResourceID: "nao-existe", DurationMin: 40, From: "2027-01-04", To: "2027-01-04"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown resource: %v", err)
	}
}
