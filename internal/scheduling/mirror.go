package scheduling

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// Mirror (phase 2): bookings copied, read-only, from another system. The
// importer (a separate package that knows the source) turns each source row
// into a MirrorItem with opaque ids; this file only stores them. Mirrored
// bookings never take part in the exclusion constraints (the source may hold
// overlaps, which the verification reports), and only rows the mirror owns are
// ever changed or deleted by it.

var sourcePattern = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
var statusPattern = regexp.MustCompile(`^[a-z_]{1,40}$`)

// MirrorItem is one source booking, already mapped.
type MirrorItem struct {
	BookingID  string
	ResourceID string
	ClientRef  string
	Start, End time.Time
	// Status is the KAS status; SourceStatus the original one (kept for audit).
	Status          string
	SourceStatus    string
	SourceUpdatedAt time.Time
}

// MirrorCursor is where the incremental read stopped.
type MirrorCursor struct {
	LastUpdatedAt time.Time
	LastSourceID  string
	FullLoadAt    time.Time
	ReconciledAt  time.Time
}

// MirrorRun counts what one run did.
type MirrorRun struct {
	Mode       string
	Started    time.Time
	Read       int
	Upserted   int
	Skipped    int
	Deleted    int
	Candidates int
	Cleared    int
}

func validMirrorItem(it MirrorItem) bool {
	return ValidRef(it.BookingID) && ValidRef(it.ResourceID) && ValidRef(it.ClientRef) && validStatus(it.Status) &&
		statusPattern.MatchString(it.SourceStatus) && it.End.After(it.Start) && it.End.Sub(it.Start) <= 12*time.Hour &&
		!it.SourceUpdatedAt.IsZero()
}

// GetMirrorCursor returns the saved cursor (zero when the source was never read).
func (s *Store) GetMirrorCursor(ctx context.Context, tenantID, source string) (MirrorCursor, error) {
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) {
		return MirrorCursor{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var c MirrorCursor
	var last, full, rec *time.Time
	err := s.pool.QueryRow(ctx, `SELECT last_updated_at, last_source_id, full_load_at, reconciled_at
		FROM sched_mirror_cursor WHERE tenant_id = $1 AND source = $2`, tenantID, source).Scan(&last, &c.LastSourceID, &full, &rec)
	if errors.Is(err, pgx.ErrNoRows) {
		return MirrorCursor{}, nil
	}
	if err != nil {
		return MirrorCursor{}, mapError(err)
	}
	for dst, src := range map[*time.Time]*time.Time{&c.LastUpdatedAt: last, &c.FullLoadAt: full, &c.ReconciledAt: rec} {
		if src != nil {
			*dst = src.UTC()
		}
	}
	return c, nil
}

// MirrorApply stores one batch and moves the cursor in the same transaction:
// if it fails, neither the rows nor the cursor move, and the next run repeats
// the batch (every write is an idempotent upsert). Resources are created on
// first sight with an opaque label. fullLoad marks the end of a full load.
func (s *Store) MirrorApply(ctx context.Context, tenantID, source string, items []MirrorItem, cursor MirrorCursor, fullLoad bool) (ApplyResult, error) {
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) || len(items) > 5000 {
		return ApplyResult{}, ErrInvalid
	}
	for _, it := range items {
		if !validMirrorItem(it) {
			return ApplyResult{}, ErrInvalid
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*dbTimeout)
	defer cancel()
	changed, stale := 0, 0
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		changed, stale = 0, 0
		for _, it := range items {
			if _, err := tx.Exec(ctx, `INSERT INTO sched_resources (tenant_id, id, kind, label, capacity, active)
				VALUES ($1, $2, 'person', $2, 1, true) ON CONFLICT (tenant_id, id) DO NOTHING`, tenantID, it.ResourceID); err != nil {
				return err
			}
			// A booking with this id that the source does not own (a KAS-native one,
			// or another source's) is never overwritten.
			// An update older than the one already mirrored (out of order) is ignored.
			var foreign bool
			var known *time.Time
			if err := tx.QueryRow(ctx, `SELECT
					EXISTS (SELECT 1 FROM sched_bookings b WHERE b.tenant_id = $1 AND b.id = $2
						AND NOT EXISTS (SELECT 1 FROM sched_mirror_items m WHERE m.tenant_id = $1 AND m.booking_id = $2 AND m.source = $3)),
					(SELECT source_updated_at FROM sched_mirror_items WHERE tenant_id = $1 AND booking_id = $2 AND source = $3)`,
				tenantID, it.BookingID, source).Scan(&foreign, &known); err != nil {
				return err
			}
			if foreign {
				return fmt.Errorf("%w: booking %s belongs to another owner", ErrInvalid, it.BookingID)
			}
			if known != nil && it.SourceUpdatedAt.Before(*known) {
				stale++
				continue
			}
			tag, err := tx.Exec(ctx, `INSERT INTO sched_bookings (tenant_id, id, resource_id, client_ref, starts_at, ends_at, status,
					idempotency_key, exclusive, client_exclusive, buffer_min, blocked_until)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 'mirror:' || $2, false, false, 0, $6)
				ON CONFLICT (tenant_id, id) DO UPDATE SET resource_id = EXCLUDED.resource_id, client_ref = EXCLUDED.client_ref,
					starts_at = EXCLUDED.starts_at, ends_at = EXCLUDED.ends_at, blocked_until = EXCLUDED.blocked_until,
					status = EXCLUDED.status, version = sched_bookings.version + 1, updated_at = now()
				WHERE (sched_bookings.resource_id, sched_bookings.client_ref, sched_bookings.starts_at, sched_bookings.ends_at, sched_bookings.status)
					IS DISTINCT FROM (EXCLUDED.resource_id, EXCLUDED.client_ref, EXCLUDED.starts_at, EXCLUDED.ends_at, EXCLUDED.status)`,
				tenantID, it.BookingID, it.ResourceID, it.ClientRef, it.Start, it.End, it.Status)
			if err != nil {
				return err
			}
			changed += int(tag.RowsAffected())
			if _, err := tx.Exec(ctx, `INSERT INTO sched_mirror_items (tenant_id, booking_id, source, source_status, source_updated_at)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (tenant_id, booking_id) DO UPDATE SET source_status = EXCLUDED.source_status,
					source_updated_at = EXCLUDED.source_updated_at, mirrored_at = now()`,
				tenantID, it.BookingID, source, it.SourceStatus, it.SourceUpdatedAt); err != nil {
				return err
			}
		}
		var last *time.Time
		if !cursor.LastUpdatedAt.IsZero() {
			t := cursor.LastUpdatedAt.UTC()
			last = &t
		}
		_, err := tx.Exec(ctx, `INSERT INTO sched_mirror_cursor (tenant_id, source, last_updated_at, last_source_id, full_load_at)
			VALUES ($1, $2, $3, $4, CASE WHEN $5 THEN now() END)
			ON CONFLICT (tenant_id, source) DO UPDATE SET last_updated_at = EXCLUDED.last_updated_at,
				last_source_id = EXCLUDED.last_source_id,
				full_load_at = COALESCE(EXCLUDED.full_load_at, sched_mirror_cursor.full_load_at)`,
			tenantID, source, last, cursor.LastSourceID, fullLoad)
		return err
	})
	return ApplyResult{Changed: changed, Stale: stale}, mapError(err)
}

// ApplyResult counts the rows a batch changed and the out-of-order updates it ignored.
type ApplyResult struct {
	Changed int
	Stale   int
}

// ReconcileResult counts what a reconciliation did.
type ReconcileResult struct {
	Deleted    int // missing in this check and in an earlier one: removed
	Candidates int // missing for the first time: kept, to be checked again
	Cleared    int // earlier candidates the source has again
}

// MirrorReconcile compares the mirrored bookings that start in [from, to)
// with present (every id the source has in that window). This is how physical
// deletes in the source reach the mirror, with a confirmation: a booking
// missing for the first time only becomes a candidate; it is deleted when a
// later reconciliation (minGap after the first) still misses it. Only rows
// owned by this source are touched.
func (s *Store) MirrorReconcile(ctx context.Context, tenantID, source string, from, to time.Time, present []string, minGap time.Duration) (ReconcileResult, error) {
	var res ReconcileResult
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) || !to.After(from) || minGap < 0 {
		return res, ErrInvalid
	}
	if present == nil {
		present = []string{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*dbTimeout)
	defer cancel()
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		res = ReconcileResult{}
		// Candidates that came back.
		tag, err := tx.Exec(ctx, `DELETE FROM sched_mirror_missing x WHERE x.tenant_id = $1 AND x.booking_id = ANY($2)`, tenantID, present)
		if err != nil {
			return err
		}
		res.Cleared = int(tag.RowsAffected())
		// Missing now and already a candidate from an earlier check: delete.
		tag, err = tx.Exec(ctx, `DELETE FROM sched_bookings b USING sched_mirror_items m, sched_mirror_missing x
			WHERE b.tenant_id = $1 AND m.tenant_id = $1 AND x.tenant_id = $1 AND m.booking_id = b.id AND x.booking_id = b.id
				AND m.source = $2 AND b.starts_at >= $3 AND b.starts_at < $4 AND NOT (b.id = ANY($5))
				AND x.first_seen_at <= now() - $6::interval`,
			tenantID, source, from, to, present, minGap)
		if err != nil {
			return err
		}
		res.Deleted = int(tag.RowsAffected())
		// Missing for the first time: candidate.
		tag, err = tx.Exec(ctx, `INSERT INTO sched_mirror_missing (tenant_id, booking_id)
			SELECT b.tenant_id, b.id FROM sched_bookings b JOIN sched_mirror_items m ON m.tenant_id = b.tenant_id AND m.booking_id = b.id
			WHERE b.tenant_id = $1 AND m.source = $2 AND b.starts_at >= $3 AND b.starts_at < $4 AND NOT (b.id = ANY($5))
			ON CONFLICT (tenant_id, booking_id) DO UPDATE SET checks = sched_mirror_missing.checks + 1`,
			tenantID, source, from, to, present)
		if err != nil {
			return err
		}
		// Includes candidates seen again but still inside minGap (checks > 1).
		res.Candidates = int(tag.RowsAffected())
		_, err = tx.Exec(ctx, `UPDATE sched_mirror_cursor SET reconciled_at = now() WHERE tenant_id = $1 AND source = $2`, tenantID, source)
		return err
	})
	return res, mapError(err)
}

// MirrorLock takes a session-level advisory lock for (tenant, source), so two
// importers never run at the same time on the same mirror. ok = false means
// another importer holds it. release must be called.
func (s *Store) MirrorLock(ctx context.Context, tenantID, source string) (release func(), ok bool, err error) {
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) {
		return nil, false, ErrInvalid
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, mapError(err)
	}
	key := tenantID + "|mirror|" + source
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 11))`, key).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, mapError(err)
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 11))`, key)
		conn.Release()
	}, true, nil
}

// RecordMirrorRun keeps the counters of one run.
func (s *Store) RecordMirrorRun(ctx context.Context, tenantID, source string, run MirrorRun) error {
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, `INSERT INTO sched_mirror_runs (tenant_id, source, mode, started_at, read, upserted, skipped, deleted, candidates, cleared)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, tenantID, source, run.Mode, run.Started, run.Read, run.Upserted, run.Skipped,
		run.Deleted, run.Candidates, run.Cleared)
	return mapError(err)
}

// DayLoad is the mirror's view of one resource on one local day.
type DayLoad struct {
	ResourceID string `json:"resource_id"`
	Day        string `json:"day"`
	Active     int    `json:"active"`
	BusyMin    int    `json:"busy_min"`
	Overlaps   int    `json:"overlaps"`
}

// MirrorDayLoads summarizes the active mirrored bookings per resource and
// local day in [from, to) (verification against the source). Overlaps counts
// pairs of active bookings of the same resource that overlap.
func (s *Store) MirrorDayLoads(ctx context.Context, tenantID, source, zone string, from, to time.Time) ([]DayLoad, error) {
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) || !to.After(from) {
		return nil, ErrInvalid
	}
	if _, err := zoneOf(Settings{Zone: zone}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `WITH a AS (
			SELECT b.id, b.resource_id, b.starts_at, b.ends_at, to_char(b.starts_at AT TIME ZONE $5, 'YYYY-MM-DD') AS day
			FROM sched_bookings b
			JOIN sched_mirror_items m ON m.tenant_id = b.tenant_id AND m.booking_id = b.id AND m.source = $2
			WHERE b.tenant_id = $1 AND b.starts_at >= $3 AND b.starts_at < $4 AND b.status IN ('tentative', 'scheduled', 'confirmed')),
		o AS (
			SELECT x.resource_id, x.day, count(*) AS n FROM a x JOIN a y
				ON x.resource_id = y.resource_id AND x.day = y.day AND x.id < y.id AND x.starts_at < y.ends_at AND y.starts_at < x.ends_at
			GROUP BY x.resource_id, x.day)
		SELECT a.resource_id, a.day, count(*), (sum(EXTRACT(EPOCH FROM a.ends_at - a.starts_at)) / 60)::int, COALESCE(max(o.n), 0)::int
		FROM a LEFT JOIN o ON o.resource_id = a.resource_id AND o.day = a.day
		GROUP BY a.resource_id, a.day ORDER BY a.resource_id, a.day`, tenantID, source, from, to, zone)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []DayLoad{}
	for rows.Next() {
		var d DayLoad
		if err := rows.Scan(&d.ResourceID, &d.Day, &d.Active, &d.BusyMin, &d.Overlaps); err != nil {
			return nil, mapError(err)
		}
		out = append(out, d)
	}
	return out, mapError(rows.Err())
}

// MirrorRunRecord is one stored run (counters only).
type MirrorRunRecord struct {
	ID         int64     `json:"id"`
	Mode       string    `json:"mode"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Read       int       `json:"read"`
	Upserted   int       `json:"upserted"`
	Skipped    int       `json:"skipped"`
	Deleted    int       `json:"deleted"`
	Candidates int       `json:"candidates"`
	Cleared    int       `json:"cleared"`
}

// MirrorRuns lists the latest runs of (tenant, source), newest first.
func (s *Store) MirrorRuns(ctx context.Context, tenantID, source string, limit int) ([]MirrorRunRecord, error) {
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT id, mode, started_at, finished_at, read, upserted, skipped, deleted, candidates, cleared
		FROM sched_mirror_runs WHERE tenant_id = $1 AND source = $2 ORDER BY id DESC LIMIT $3`, tenantID, source, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []MirrorRunRecord{}
	for rows.Next() {
		var r MirrorRunRecord
		if err := rows.Scan(&r.ID, &r.Mode, &r.StartedAt, &r.FinishedAt, &r.Read, &r.Upserted, &r.Skipped, &r.Deleted, &r.Candidates, &r.Cleared); err != nil {
			return nil, mapError(err)
		}
		out = append(out, r)
	}
	return out, mapError(rows.Err())
}

// ComparisonCounts is the summary of one comparison kept in the history.
type ComparisonCounts struct {
	ID             int64     `json:"id,omitempty"`
	WindowFrom     string    `json:"window_from"`
	WindowTo       string    `json:"window_to"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	ResourceDays   int       `json:"resource_days"`
	Equal          int       `json:"equal"`
	ImportErrors   int       `json:"import_errors"`
	ClinicalRules  int       `json:"clinical_rules"`
	RealDivergence int       `json:"real_divergence"`
	PatternsEqual  int       `json:"weekly_patterns_equal"`
	PatternsTotal  int       `json:"weekly_patterns_total"`
}

// RecordComparison stores a comparison summary and returns its id and time.
func (s *Store) RecordComparison(ctx context.Context, tenantID, source string, c ComparisonCounts) (int64, time.Time, error) {
	var at time.Time
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) {
		return 0, at, ErrInvalid
	}
	if _, ok := parseDate(c.WindowFrom); !ok {
		return 0, at, ErrInvalid
	}
	if _, ok := parseDate(c.WindowTo); !ok {
		return 0, at, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO sched_mirror_comparisons (tenant_id, source, window_from, window_to, resource_days,
			equal_days, import_errors, clinical_rules, unexplained, patterns_equal, patterns_total)
		VALUES ($1, $2, $3::date, $4::date, $5, $6, $7, $8, $9, $10, $11) RETURNING id, created_at`, tenantID, source, c.WindowFrom, c.WindowTo,
		c.ResourceDays, c.Equal, c.ImportErrors, c.ClinicalRules, c.RealDivergence, c.PatternsEqual, c.PatternsTotal).Scan(&id, &at)
	return id, at, mapError(err)
}

// ListComparisons returns the latest comparison summaries, newest first.
func (s *Store) ListComparisons(ctx context.Context, tenantID, source string, limit int) ([]ComparisonCounts, error) {
	if !ValidRef(tenantID) || !sourcePattern.MatchString(source) || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT id, window_from::text, window_to::text, created_at, resource_days, equal_days, import_errors,
			clinical_rules, unexplained, patterns_equal, patterns_total
		FROM sched_mirror_comparisons WHERE tenant_id = $1 AND source = $2 ORDER BY id DESC LIMIT $3`, tenantID, source, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []ComparisonCounts{}
	for rows.Next() {
		var c ComparisonCounts
		if err := rows.Scan(&c.ID, &c.WindowFrom, &c.WindowTo, &c.CreatedAt, &c.ResourceDays, &c.Equal, &c.ImportErrors,
			&c.ClinicalRules, &c.RealDivergence, &c.PatternsEqual, &c.PatternsTotal); err != nil {
			return nil, mapError(err)
		}
		out = append(out, c)
	}
	return out, mapError(rows.Err())
}
