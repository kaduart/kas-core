package scheduling

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"kas-core-pilot/internal/pgdb"
	"kas-core-pilot/internal/pgmigrate"
)

//go:embed migrations/*.sql rollback/*.sql
var sqlFiles embed.FS

var migrator = pgmigrate.Migrator{
	FS:     sqlFiles,
	Dir:    "migrations",
	Table:  "scheduling_schema_migrations",
	LockID: 0x4b415353434844, // "KASSCHD"
}

const dbTimeout = 15 * time.Second

// MaxRetries is how many times a write is repeated after a serialization
// failure or a deadlock (SQLSTATE 40001 / 40P01) before ErrUnavailable.
const MaxRetries = 3

// Store keeps the Agenda of every tenant. Every query filters by tenant_id.
type Store struct {
	pool      *pgxpool.Pool
	retryBase time.Duration
	// hook lets tests force a failure at a named stage of a write
	// ("after_insert", "after_commit"). Nil in production.
	hook func(stage string) error
}

// Open connects and applies (or only verifies) the migrations.
func Open(ctx context.Context, connString string, applyMigrations bool) (*Store, error) {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "scheduling", connString)
	if err != nil {
		return nil, err
	}
	if applyMigrations {
		err = migrator.Apply(ctx, pool)
	} else {
		err = migrator.Verify(ctx, pool)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("scheduling database schema: %w", err)
	}
	return &Store{pool: pool, retryBase: 20 * time.Millisecond}, nil
}

// Migrate applies the migrations with a migration-capable role.
func Migrate(ctx context.Context, connString string) error {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "scheduling", connString)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrator.Apply(ctx, pool)
}

// Rollback removes every scheduling table (rollback/0001_scheduling.down.sql),
// in one transaction. It deletes the data: use only with the migration role
// and an explicit decision.
func Rollback(ctx context.Context, connString string) error {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "scheduling", connString)
	if err != nil {
		return err
	}
	defer pool.Close()
	down, err := sqlFiles.ReadFile("rollback/0001_scheduling.down.sql")
	if err != nil {
		return err
	}
	return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, string(down))
		return err
	})
}

func (s *Store) Close() { s.pool.Close() }

// Ready pings the database.
func (s *Store) Ready(ctx context.Context) error { return s.pool.Ping(ctx) }

// Missing lists what a tenant still has to configure (module readiness).
func (s *Store) Missing(ctx context.Context, tenantID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var resources, rules bool
	if err := s.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM sched_resources WHERE tenant_id = $1 AND active),
		EXISTS (SELECT 1 FROM sched_weekly_rules WHERE tenant_id = $1)`, tenantID).Scan(&resources, &rules); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var missing []string
	if !resources {
		missing = append(missing, "resources")
	}
	if !rules {
		missing = append(missing, "weekly_rules")
	}
	return missing, nil
}

// --- configuration ---------------------------------------------------------

// PutSettings creates or replaces the tenant settings.
func (s *Store) PutSettings(ctx context.Context, tenantID string, st Settings) error {
	if !ValidRef(tenantID) || ValidateSettings(st) != nil {
		return ErrInvalid
	}
	if st.Holidays == nil {
		st.Holidays = []string{}
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, `INSERT INTO sched_settings (tenant_id, zone, holidays, client_exclusive)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id) DO UPDATE SET zone = EXCLUDED.zone, holidays = EXCLUDED.holidays,
			client_exclusive = EXCLUDED.client_exclusive, updated_at = now()`,
		tenantID, st.Zone, st.Holidays, st.ClientExclusive)
	return mapError(err)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func getSettings(ctx context.Context, q querier, tenantID string) (Settings, error) {
	st := Settings{}
	err := q.QueryRow(ctx, `SELECT zone, holidays, client_exclusive FROM sched_settings WHERE tenant_id = $1`, tenantID).
		Scan(&st.Zone, &st.Holidays, &st.ClientExclusive)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings(), nil
	}
	return st, err
}

// GetSettings returns the tenant settings (defaults when never saved).
func (s *Store) GetSettings(ctx context.Context, tenantID string) (Settings, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	st, err := getSettings(ctx, s.pool, tenantID)
	return st, mapError(err)
}

// PutResource creates or replaces one resource.
func (s *Store) PutResource(ctx context.Context, tenantID string, r Resource) error {
	if !ValidRef(tenantID) || !ValidRef(r.ID) || r.Label == "" || len(r.Label) > 120 {
		return ErrInvalid
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, `INSERT INTO sched_resources (tenant_id, id, kind, label, tags, capacity, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, id) DO UPDATE SET kind = EXCLUDED.kind, label = EXCLUDED.label, tags = EXCLUDED.tags,
			capacity = EXCLUDED.capacity, active = EXCLUDED.active`,
		tenantID, r.ID, r.Kind, r.Label, r.Tags, r.Capacity, r.Active)
	return mapError(err)
}

func getResource(ctx context.Context, q querier, tenantID, id string, lock string) (Resource, error) {
	r := Resource{ID: id}
	err := q.QueryRow(ctx, `SELECT kind, label, tags, capacity, active FROM sched_resources
		WHERE tenant_id = $1 AND id = $2 `+lock, tenantID, id).Scan(&r.Kind, &r.Label, &r.Tags, &r.Capacity, &r.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return r, err
}

// PutWeeklyRules replaces all weekly rules of one resource.
func (s *Store) PutWeeklyRules(ctx context.Context, tenantID, resourceID string, rules []WeeklyRule) error {
	if !ValidRef(tenantID) || !ValidRef(resourceID) || len(rules) > 100 {
		return ErrInvalid
	}
	for _, r := range rules {
		if ValidateRule(r) != nil {
			return ErrInvalid
		}
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	return mapError(pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := getResource(ctx, tx, tenantID, resourceID, "FOR UPDATE"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sched_weekly_rules WHERE tenant_id = $1 AND resource_id = $2`, tenantID, resourceID); err != nil {
			return err
		}
		for _, r := range rules {
			if _, err := tx.Exec(ctx, `INSERT INTO sched_weekly_rules (tenant_id, resource_id, weekday, start_min, end_min, valid_from, valid_to)
				VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')::date, NULLIF($7, '')::date)`,
				tenantID, resourceID, r.Weekday, r.StartMin, r.EndMin, r.ValidFrom, r.ValidTo); err != nil {
				return err
			}
		}
		return nil
	}))
}

// AddException closes an interval of one resource.
func (s *Store) AddException(ctx context.Context, tenantID, resourceID string, e Exception) error {
	if !ValidRef(tenantID) || !ValidRef(resourceID) || !e.End.After(e.Start) {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, `INSERT INTO sched_exceptions (tenant_id, resource_id, starts_at, ends_at, reason)
		VALUES ($1, $2, $3, $4, $5)`, tenantID, resourceID, e.Start, e.End, e.Reason)
	return mapError(err)
}

// PutService creates or replaces one service.
func (s *Store) PutService(ctx context.Context, tenantID string, sv Service) error {
	if !ValidRef(tenantID) || !ValidRef(sv.ID) || sv.Label == "" || len(sv.Label) > 120 {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, `INSERT INTO sched_services (tenant_id, id, label, duration_min, buffer_min)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, id) DO UPDATE SET label = EXCLUDED.label, duration_min = EXCLUDED.duration_min,
			buffer_min = EXCLUDED.buffer_min`, tenantID, sv.ID, sv.Label, sv.DurationMin, sv.BufferMin)
	return mapError(err)
}

func (s *Store) getService(ctx context.Context, tenantID, id string) (Service, error) {
	sv := Service{ID: id}
	err := s.pool.QueryRow(ctx, `SELECT label, duration_min, buffer_min FROM sched_services WHERE tenant_id = $1 AND id = $2`,
		tenantID, id).Scan(&sv.Label, &sv.DurationMin, &sv.BufferMin)
	if errors.Is(err, pgx.ErrNoRows) {
		return Service{}, ErrNotFound
	}
	return sv, err
}

// --- reads and calculations ------------------------------------------------

const bookingColumns = `id, resource_id, client_ref, COALESCE(service_id, ''), starts_at, ends_at, status,
	COALESCE(series_id, ''), version, updated_at`

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	err := row.Scan(&b.ID, &b.ResourceID, &b.ClientRef, &b.ServiceID, &b.Start, &b.End, &b.Status, &b.SeriesID, &b.Version, &b.UpdatedAt)
	b.Start, b.End, b.UpdatedAt = b.Start.UTC(), b.End.UTC(), b.UpdatedAt.UTC()
	return b, err
}

func collectBookings(rows pgx.Rows, err error) ([]Booking, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Booking{}
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListBookings returns the bookings that touch [from, to), optionally of one resource.
func (s *Store) ListBookings(ctx context.Context, tenantID, resourceID string, from, to time.Time) ([]Booking, error) {
	if !ValidRef(tenantID) || (resourceID != "" && !ValidRef(resourceID)) || !to.After(from) || to.Sub(from) > MaxRangeDays*24*time.Hour {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	out, err := collectBookings(s.pool.Query(ctx, `SELECT `+bookingColumns+` FROM sched_bookings
		WHERE tenant_id = $1 AND ($2 = '' OR resource_id = $2) AND starts_at < $4 AND ends_at > $3
		ORDER BY starts_at, id LIMIT 5000`, tenantID, resourceID, from, to))
	return out, mapError(err)
}

// AvailabilityQuery asks for the free slots of one resource.
type AvailabilityQuery struct {
	ResourceID  string
	ServiceID   string // gives duration and buffer, or
	DurationMin int    // explicit duration (buffer 0)
	From, To    string // local dates
	StepMin     int
	NotBefore   time.Time
}

// Availability loads the resource's data and runs the pure calculation.
func (s *Store) Availability(ctx context.Context, tenantID string, q AvailabilityQuery) ([]Slot, error) {
	if !ValidRef(tenantID) || !ValidRef(q.ResourceID) {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	in := AvailabilityInput{From: q.From, To: q.To, DurationMin: q.DurationMin, StepMin: q.StepMin, NotBefore: q.NotBefore}
	if q.ServiceID != "" {
		sv, err := s.getService(ctx, tenantID, q.ServiceID)
		if err != nil {
			return nil, mapError(err)
		}
		in.DurationMin, in.BufferMin = sv.DurationMin, sv.BufferMin
	}
	var err error
	if in.Settings, err = getSettings(ctx, s.pool, tenantID); err != nil {
		return nil, mapError(err)
	}
	if in.Resource, err = getResource(ctx, s.pool, tenantID, q.ResourceID, ""); err != nil {
		return nil, mapError(err)
	}
	loc, err := zoneOf(in.Settings)
	if err != nil {
		return nil, err
	}
	from, ok1 := parseDate(q.From)
	to, ok2 := parseDate(q.To)
	if !ok1 || !ok2 || to.Before(from) {
		return nil, ErrInvalid
	}
	// The local period, widened by a day each side for the buffer and the zone.
	lo := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -1)
	hi := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 2)

	rows, err := s.pool.Query(ctx, `SELECT weekday, start_min, end_min, COALESCE(valid_from::text, ''), COALESCE(valid_to::text, '')
		FROM sched_weekly_rules WHERE tenant_id = $1 AND resource_id = $2`, tenantID, q.ResourceID)
	if err != nil {
		return nil, mapError(err)
	}
	for rows.Next() {
		var r WeeklyRule
		if err := rows.Scan(&r.Weekday, &r.StartMin, &r.EndMin, &r.ValidFrom, &r.ValidTo); err != nil {
			rows.Close()
			return nil, mapError(err)
		}
		in.Rules = append(in.Rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	rows, err = s.pool.Query(ctx, `SELECT starts_at, ends_at, reason FROM sched_exceptions
		WHERE tenant_id = $1 AND resource_id = $2 AND starts_at < $4 AND ends_at > $3`, tenantID, q.ResourceID, lo, hi)
	if err != nil {
		return nil, mapError(err)
	}
	for rows.Next() {
		var e Exception
		if err := rows.Scan(&e.Start, &e.End, &e.Reason); err != nil {
			rows.Close()
			return nil, mapError(err)
		}
		in.Exceptions = append(in.Exceptions, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	if in.Bookings, err = collectBookings(s.pool.Query(ctx, `SELECT `+bookingColumns+` FROM sched_bookings
		WHERE tenant_id = $1 AND resource_id = $2 AND status IN ('tentative', 'scheduled', 'confirmed')
			AND starts_at < $4 AND ends_at > $3`, tenantID, q.ResourceID, lo, hi)); err != nil {
		return nil, mapError(err)
	}
	return Availability(in)
}

// CheckConflicts loads what the candidates could clash with and runs FindConflicts.
func (s *Store) CheckConflicts(ctx context.Context, tenantID string, candidates []Candidate) ([]Conflict, error) {
	if !ValidRef(tenantID) || len(candidates) == 0 || len(candidates) > MaxCandidates {
		return nil, ErrInvalid
	}
	lo, hi := candidates[0].Start, candidates[0].End
	resources, clients := []string{}, []string{}
	for _, c := range candidates {
		if !ValidRef(c.ResourceID) || !ValidRef(c.ClientRef) || !c.End.After(c.Start) {
			return nil, ErrInvalid
		}
		if c.Start.Before(lo) {
			lo = c.Start
		}
		if c.End.After(hi) {
			hi = c.End
		}
		resources, clients = append(resources, c.ResourceID), append(clients, c.ClientRef)
	}
	if hi.Sub(lo) > MaxSeriesHorizon {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	st, err := getSettings(ctx, s.pool, tenantID)
	if err != nil {
		return nil, mapError(err)
	}
	capacity := map[string]int{}
	rows, err := s.pool.Query(ctx, `SELECT id, capacity FROM sched_resources WHERE tenant_id = $1 AND id = ANY($2)`, tenantID, resources)
	if err != nil {
		return nil, mapError(err)
	}
	for rows.Next() {
		var id string
		var c int
		if err := rows.Scan(&id, &c); err != nil {
			rows.Close()
			return nil, mapError(err)
		}
		capacity[id] = c
	}
	rows.Close()
	existing, err := collectBookings(s.pool.Query(ctx, `SELECT `+bookingColumns+` FROM sched_bookings
		WHERE tenant_id = $1 AND (resource_id = ANY($2) OR client_ref = ANY($3))
			AND status IN ('tentative', 'scheduled', 'confirmed') AND starts_at < $5 AND ends_at > $4`,
		tenantID, resources, clients, lo, hi))
	if err != nil {
		return nil, mapError(err)
	}
	return FindConflicts(candidates, existing, capacity, st.ClientExclusive)
}

// --- writes ------------------------------------------------------------------

// NewBooking is a booking request. The store fills Exclusive/ClientExclusive
// from the resource and the settings.
type NewBooking struct {
	ID             string
	ResourceID     string
	ClientRef      string
	ServiceID      string
	Start, End     time.Time
	Status         string // default scheduled
	SeriesID       string
	IdempotencyKey string
	Actor          string
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "bk_" + hex.EncodeToString(b)
}

func (s *Store) runHook(stage string) error {
	if s.hook == nil {
		return nil
	}
	return s.hook(stage)
}

// retryable: serialization failure or deadlock, safe to run the whole
// transaction again.
func retryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "40P01")
}

// inTx runs fn in a transaction, repeating it up to MaxRetries times with a
// growing wait after a serialization failure or deadlock. Each attempt is
// rolled back whole, so nothing partial is ever left.
func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	wait := s.retryBase
	for attempt := 0; ; attempt++ {
		err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, fn)
		if err == nil || !retryable(err) {
			return err
		}
		if attempt >= MaxRetries {
			return fmt.Errorf("%w: %d attempts: %v", ErrUnavailable, attempt+1, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// lockResource serializes writes on one resource for the rest of the
// transaction. For a group (capacity > 1) it is what keeps the count of taken
// places from racing. For an exclusive resource the exclusion constraint is
// still what refuses a double booking; the lock only makes concurrent requests
// queue instead of inserting together, which would make PostgreSQL abort some
// of them as deadlocks (each waiting on the other's uncommitted row). Refs
// cannot contain "|", so "tenant|resource" is unambiguous.
func lockResource(ctx context.Context, tx pgx.Tx, tenantID, resourceID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || '|' || $2, 7))`, tenantID, resourceID)
	return err
}

func groupFull(ctx context.Context, tx pgx.Tx, tenantID string, r Resource, iv Interval, except string) (bool, error) {
	var taken int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM sched_bookings WHERE tenant_id = $1 AND resource_id = $2 AND id <> $3
		AND status IN ('tentative', 'scheduled', 'confirmed') AND starts_at < $5 AND ends_at > $4`,
		tenantID, r.ID, except, iv.Start, iv.End).Scan(&taken)
	return taken >= r.Capacity, err
}

func logWrite(ctx context.Context, tx pgx.Tx, tenantID, bookingID, action string, version int, actor string) error {
	_, err := tx.Exec(ctx, `INSERT INTO sched_booking_log (tenant_id, booking_id, action, version, actor) VALUES ($1, $2, $3, $4, $5)`,
		tenantID, bookingID, action, version, actor)
	return err
}

func (s *Store) byIdempotencyKey(ctx context.Context, tenantID, key string) (Booking, bool, error) {
	b, err := scanBooking(s.pool.QueryRow(ctx, `SELECT `+bookingColumns+` FROM sched_bookings WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, false, nil
	}
	return b, err == nil, err
}

func sameRequest(b Booking, nb NewBooking) bool {
	return b.ResourceID == nb.ResourceID && b.ClientRef == nb.ClientRef && b.ServiceID == nb.ServiceID &&
		b.Start.Equal(nb.Start) && b.End.Equal(nb.End)
}

// CreateBooking writes one booking. The same IdempotencyKey always returns the
// same booking (created = false on a repeat), even after the first answer was
// lost. Errors: ErrSlotTaken, ErrClientBusy, ErrNotFound (resource),
// ErrIdempotencyMismatch, ErrInvalid, ErrUnavailable.
func (s *Store) CreateBooking(ctx context.Context, tenantID string, nb NewBooking) (Booking, bool, error) {
	if nb.ID == "" {
		nb.ID = newID()
	}
	if nb.Status == "" {
		nb.Status = StatusScheduled
	}
	if !ValidRef(tenantID) || !ValidRef(nb.ID) || !ValidRef(nb.ResourceID) || !ValidRef(nb.ClientRef) ||
		!ValidRef(nb.IdempotencyKey) || !ValidRef(nb.Actor) || !ActiveStatus(nb.Status) ||
		(nb.ServiceID != "" && !ValidRef(nb.ServiceID)) || (nb.SeriesID != "" && !ValidRef(nb.SeriesID)) ||
		!nb.End.After(nb.Start) || nb.End.Sub(nb.Start) > 12*time.Hour {
		return Booking{}, false, ErrInvalid
	}
	nb.Start, nb.End = nb.Start.UTC().Truncate(time.Second), nb.End.UTC().Truncate(time.Second)
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	var created Booking
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		r, err := getResource(ctx, tx, tenantID, nb.ResourceID, "FOR SHARE")
		if err != nil {
			return err
		}
		if !r.Active {
			return ErrInvalid
		}
		st, err := getSettings(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		exclusive := r.Capacity <= 1
		if err := lockResource(ctx, tx, tenantID, r.ID); err != nil {
			return err
		}
		if !exclusive {
			full, err := groupFull(ctx, tx, tenantID, r, Interval{nb.Start, nb.End}, "")
			if err != nil {
				return err
			}
			if full {
				return ErrSlotTaken
			}
		}
		created, err = scanBooking(tx.QueryRow(ctx, `INSERT INTO sched_bookings
			(tenant_id, id, resource_id, client_ref, service_id, starts_at, ends_at, status, series_id, idempotency_key, exclusive, client_exclusive)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, NULLIF($9, ''), $10, $11, $12)
			RETURNING `+bookingColumns,
			tenantID, nb.ID, nb.ResourceID, nb.ClientRef, nb.ServiceID, nb.Start, nb.End, nb.Status, nb.SeriesID,
			nb.IdempotencyKey, exclusive, st.ClientExclusive))
		if err != nil {
			return err
		}
		if err := s.runHook("after_insert"); err != nil {
			return err
		}
		return logWrite(ctx, tx, tenantID, created.ID, "create", 1, nb.Actor)
	})
	if err == nil {
		if hookErr := s.runHook("after_commit"); hookErr != nil {
			return Booking{}, false, hookErr
		}
		return created, true, nil
	}
	// A refused write may be the repeat of one that already succeeded (its
	// answer lost): the idempotency key decides.
	if existing, found, lookupErr := s.byIdempotencyKey(ctx, tenantID, nb.IdempotencyKey); lookupErr == nil && found {
		if sameRequest(existing, nb) {
			return existing, false, nil
		}
		return Booking{}, false, ErrIdempotencyMismatch
	}
	return Booking{}, false, mapError(err)
}

// Reschedule moves an active booking. expectedVersion must match the stored
// version (optimistic lock): of two concurrent moves of the same booking, one
// wins and the other gets ErrVersionConflict. The old slot is released only if
// the move is committed.
func (s *Store) Reschedule(ctx context.Context, tenantID, id string, expectedVersion int, start, end time.Time, actor string) (Booking, error) {
	if !ValidRef(tenantID) || !ValidRef(id) || !ValidRef(actor) || expectedVersion < 1 || !end.After(start) || end.Sub(start) > 12*time.Hour {
		return Booking{}, ErrInvalid
	}
	start, end = start.UTC().Truncate(time.Second), end.UTC().Truncate(time.Second)
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var moved Booking
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanBooking(tx.QueryRow(ctx, `SELECT `+bookingColumns+` FROM sched_bookings WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Version != expectedVersion {
			return ErrVersionConflict
		}
		if !ActiveStatus(cur.Status) {
			return ErrInvalid
		}
		r, err := getResource(ctx, tx, tenantID, cur.ResourceID, "FOR SHARE")
		if err != nil {
			return err
		}
		if err := lockResource(ctx, tx, tenantID, r.ID); err != nil {
			return err
		}
		if r.Capacity > 1 {
			full, err := groupFull(ctx, tx, tenantID, r, Interval{start, end}, id)
			if err != nil {
				return err
			}
			if full {
				return ErrSlotTaken
			}
		}
		moved, err = scanBooking(tx.QueryRow(ctx, `UPDATE sched_bookings SET starts_at = $3, ends_at = $4, version = version + 1, updated_at = now()
			WHERE tenant_id = $1 AND id = $2 RETURNING `+bookingColumns, tenantID, id, start, end))
		if err != nil {
			return err
		}
		if err := s.runHook("after_update"); err != nil {
			return err
		}
		return logWrite(ctx, tx, tenantID, id, "reschedule", moved.Version, actor)
	})
	return moved, mapError(err)
}

// Cancel releases a booking's slot (same optimistic lock as Reschedule).
func (s *Store) Cancel(ctx context.Context, tenantID, id string, expectedVersion int, actor string) (Booking, error) {
	if !ValidRef(tenantID) || !ValidRef(id) || !ValidRef(actor) || expectedVersion < 1 {
		return Booking{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var out Booking
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var version int
		var status string
		err := tx.QueryRow(ctx, `SELECT version, status FROM sched_bookings WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id).Scan(&version, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return ErrVersionConflict
		}
		if !ActiveStatus(status) {
			return ErrInvalid
		}
		out, err = scanBooking(tx.QueryRow(ctx, `UPDATE sched_bookings SET status = 'canceled', version = version + 1, updated_at = now()
			WHERE tenant_id = $1 AND id = $2 RETURNING `+bookingColumns, tenantID, id))
		if err != nil {
			return err
		}
		return logWrite(ctx, tx, tenantID, id, "cancel", out.Version, actor)
	})
	return out, mapError(err)
}

// mapError turns database errors into the package errors. Errors already from
// this package pass through.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrNotFound, ErrInvalid, ErrSlotTaken, ErrClientBusy, ErrVersionConflict, ErrIdempotencyMismatch, ErrUnavailable} {
		if errors.Is(err, known) {
			return err
		}
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23P01":
			if pg.ConstraintName == "sched_bookings_client_no_overlap" {
				return ErrClientBusy
			}
			return ErrSlotTaken
		case "23503":
			return ErrNotFound
		case "23505":
			return ErrIdempotencyMismatch
		case "23514", "22P02", "22007", "22008":
			return ErrInvalid
		}
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}
