package events

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kas-core-pilot/internal/pgdb"
	"kas-core-pilot/internal/pgmigrate"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var eventsMigrator = pgmigrate.Migrator{
	FS:     migrationFiles,
	Dir:    "migrations",
	Table:  "events_schema_migrations",
	LockID: 0x4b4153455645, // "KASEVE"
}

const dbOperationTimeout = 5 * time.Second

// Delivery is one claimed attempt to deliver an event to a consumer.
type Delivery struct {
	ID         int64
	ConsumerID string
	// Attempt counts claims, including this one (1 on the first delivery).
	Attempt int
	Event   Event
}

// PostgresStore is the PostgreSQL outbox. The runtime role needs only:
//   - outbox_events: INSERT and SELECT (never UPDATE or DELETE);
//   - outbox_deliveries: INSERT, SELECT and UPDATE (never DELETE);
//   - SELECT on events_schema_migrations, plus USAGE on the schema.
type PostgresStore struct {
	mu     sync.RWMutex
	pool   *pgxpool.Pool
	closed bool
}

// OpenPostgres connects and applies pending migrations (migration-capable role).
func OpenPostgres(ctx context.Context, connString string) (*PostgresStore, error) {
	return openPostgres(ctx, connString, true)
}

// OpenPostgresVerified connects without DDL and fails if migrations are pending.
func OpenPostgresVerified(ctx context.Context, connString string) (*PostgresStore, error) {
	return openPostgres(ctx, connString, false)
}

// MigratePostgres applies pending migrations and closes the connection.
func MigratePostgres(ctx context.Context, connString string) error {
	store, err := openPostgres(ctx, connString, true)
	if err != nil {
		return err
	}
	return store.Close()
}

func openPostgres(ctx context.Context, connString string, applyMigrations bool) (*PostgresStore, error) {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "events", connString)
	if err != nil {
		return nil, err
	}
	if applyMigrations {
		err = eventsMigrator.Apply(ctx, pool)
	} else {
		err = eventsMigrator.Verify(ctx, pool)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("events database schema: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) usable() (*pgxpool.Pool, func(), error) {
	s.mu.RLock()
	if s.closed || s.pool == nil {
		s.mu.RUnlock()
		return nil, nil, ErrStoreUnavailable
	}
	return s.pool, s.mu.RUnlock, nil
}

// Publish stores the event and one pending delivery per consumer in a single
// transaction. A concurrent identical publication waits for the first to commit
// and is then reported as a duplicate.
func (s *PostgresStore) Publish(ctx context.Context, event Event, consumers []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	occurred, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil {
		return ErrInvalidEvent
	}
	received, err := time.Parse(time.RFC3339Nano, event.ReceivedAt)
	if err != nil {
		return ErrInvalidEvent
	}
	pool, release, err := s.usable()
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, dbOperationTimeout)
	defer cancel()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return ErrStoreUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var pk int64
	err = tx.QueryRow(ctx, `INSERT INTO outbox_events
		(contract_version, event_id, tenant_id, service_id, event_type, correlation_id,
		 occurred_at_raw, received_at_raw, occurred_at, received_at, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
		ON CONFLICT (tenant_id, service_id, event_id) DO NOTHING
		RETURNING id`,
		event.ContractVersion, event.EventID, event.TenantID, event.ServiceID, event.EventType,
		event.CorrelationID, event.OccurredAt, event.ReceivedAt, occurred.UTC(), received.UTC(),
		string(event.Payload)).Scan(&pk)
	switch {
	case err == nil:
		for _, consumer := range consumers {
			if _, err := tx.Exec(ctx, `INSERT INTO outbox_deliveries
				(event_pk, consumer_id, status, attempts, next_attempt_at, created_at)
				VALUES ($1, $2, 'pending', 0, $3, $3)`, pk, consumer, received.UTC()); err != nil {
				return ErrStoreUnavailable
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return ErrStoreUnavailable
		}
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		_ = tx.Rollback(ctx)
		return s.compareWithStored(ctx, pool, event)
	default:
		return ErrStoreUnavailable
	}
}

func (s *PostgresStore) compareWithStored(ctx context.Context, pool *pgxpool.Pool, event Event) error {
	var stored Event
	var payload []byte
	err := pool.QueryRow(ctx, `SELECT contract_version, event_id, tenant_id, service_id, event_type,
		correlation_id, occurred_at_raw, received_at_raw, payload
		FROM outbox_events WHERE tenant_id = $1 AND service_id = $2 AND event_id = $3`,
		event.TenantID, event.ServiceID, event.EventID).Scan(&stored.ContractVersion, &stored.EventID,
		&stored.TenantID, &stored.ServiceID, &stored.EventType, &stored.CorrelationID,
		&stored.OccurredAt, &stored.ReceivedAt, &payload)
	if err != nil {
		return ErrStoreUnavailable
	}
	if stored.Payload, err = CanonicalPayload(payload); err != nil {
		return ErrStoreUnavailable
	}
	if stored.ContentHash() == event.ContentHash() {
		return &DuplicateError{Stored: stored}
	}
	return ErrEventConflict
}

// Claim leases up to limit deliveries for the given consumers. A delivery is
// claimable when it is pending and due, or in flight with an expired lease.
// Deliveries whose expired lease already used every attempt are buried first.
// Claiming counts as an attempt. It uses FOR UPDATE SKIP LOCKED so concurrent
// workers never claim the same row.
func (s *PostgresStore) Claim(ctx context.Context, workerID string, consumers []string, now time.Time, lease time.Duration, maxAttempts, limit int) ([]Delivery, error) {
	if len(consumers) == 0 || limit < 1 {
		return nil, nil
	}
	pool, release, err := s.usable()
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, dbOperationTimeout)
	defer cancel()
	now = now.UTC()
	if _, err := pool.Exec(ctx, `UPDATE outbox_deliveries
		SET status = 'dead', dead_at = $1, locked_until = NULL, locked_by = NULL, last_error_code = 'lease_expired'
		WHERE status = 'in_flight' AND locked_until < $1 AND attempts >= $2 AND consumer_id = ANY($3)`,
		now, maxAttempts, consumers); err != nil {
		return nil, ErrStoreUnavailable
	}
	rows, err := pool.Query(ctx, `WITH claimed AS (
		UPDATE outbox_deliveries
		SET status = 'in_flight', attempts = attempts + 1, locked_until = $2, locked_by = $3
		WHERE id IN (
			SELECT id FROM outbox_deliveries
			WHERE consumer_id = ANY($4)
			  AND ((status = 'pending' AND next_attempt_at <= $1)
			    OR (status = 'in_flight' AND locked_until < $1 AND attempts < $5))
			ORDER BY next_attempt_at, id
			LIMIT $6
			FOR UPDATE SKIP LOCKED)
		RETURNING id, event_pk, consumer_id, attempts)
		SELECT c.id, c.consumer_id, c.attempts, e.contract_version, e.event_id, e.tenant_id, e.service_id,
		       e.event_type, e.correlation_id, e.occurred_at_raw, e.received_at_raw, e.payload
		FROM claimed c JOIN outbox_events e ON e.id = c.event_pk
		ORDER BY c.id`,
		now, now.Add(lease), workerID, consumers, maxAttempts, limit)
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		var payload []byte
		if err := rows.Scan(&d.ID, &d.ConsumerID, &d.Attempt, &d.Event.ContractVersion, &d.Event.EventID,
			&d.Event.TenantID, &d.Event.ServiceID, &d.Event.EventType, &d.Event.CorrelationID,
			&d.Event.OccurredAt, &d.Event.ReceivedAt, &payload); err != nil {
			return nil, ErrStoreUnavailable
		}
		if d.Event.Payload, err = CanonicalPayload(payload); err != nil {
			return nil, ErrStoreUnavailable
		}
		out = append(out, d)
	}
	if rows.Err() != nil {
		return nil, ErrStoreUnavailable
	}
	return out, nil
}

// settle updates a delivery only if this worker still holds the lease for this
// attempt. It returns false when the lease was lost (expired and reclaimed).
func (s *PostgresStore) settle(ctx context.Context, sql string, args ...any) (bool, error) {
	pool, release, err := s.usable()
	if err != nil {
		return false, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, dbOperationTimeout)
	defer cancel()
	tag, err := pool.Exec(ctx, sql, args...)
	if err != nil {
		return false, ErrStoreUnavailable
	}
	return tag.RowsAffected() == 1, nil
}

// Complete marks the delivery delivered.
func (s *PostgresStore) Complete(ctx context.Context, workerID string, d Delivery, now time.Time) (bool, error) {
	return s.settle(ctx, `UPDATE outbox_deliveries
		SET status = 'delivered', delivered_at = $4, locked_until = NULL, locked_by = NULL
		WHERE id = $1 AND status = 'in_flight' AND locked_by = $2 AND attempts = $3`,
		d.ID, workerID, d.Attempt, now.UTC())
}

// Retry returns the delivery to pending, due at next, recording an error code.
func (s *PostgresStore) Retry(ctx context.Context, workerID string, d Delivery, next time.Time, code string) (bool, error) {
	return s.settle(ctx, `UPDATE outbox_deliveries
		SET status = 'pending', next_attempt_at = $4, locked_until = NULL, locked_by = NULL, last_error_code = $5
		WHERE id = $1 AND status = 'in_flight' AND locked_by = $2 AND attempts = $3`,
		d.ID, workerID, d.Attempt, next.UTC(), code)
}

// Bury marks the delivery dead. Dead rows are kept; nothing deletes them.
func (s *PostgresStore) Bury(ctx context.Context, workerID string, d Delivery, now time.Time, code string) (bool, error) {
	return s.settle(ctx, `UPDATE outbox_deliveries
		SET status = 'dead', dead_at = $4, locked_until = NULL, locked_by = NULL, last_error_code = $5
		WHERE id = $1 AND status = 'in_flight' AND locked_by = $2 AND attempts = $3`,
		d.ID, workerID, d.Attempt, now.UTC(), code)
}

// Ready reports whether the database currently answers a ping.
func (s *PostgresStore) Ready(ctx context.Context) error {
	pool, release, err := s.usable()
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		return ErrStoreUnavailable
	}
	return nil
}

// Close waits for in-flight operations, then releases all connections.
func (s *PostgresStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.pool.Close()
	return nil
}

var (
	_ Repository       = (*PostgresStore)(nil)
	_ DeliveryStore    = (*PostgresStore)(nil)
	_ ReadinessChecker = (*PostgresStore)(nil)
)
