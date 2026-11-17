package audit

import (
	"context"
	"embed"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"kas-core-pilot/internal/pgdb"
	"kas-core-pilot/internal/pgmigrate"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// auditMigrator keeps the tracking table and lock key used before migrations
// were shared, so databases migrated by earlier builds remain compatible.
var auditMigrator = pgmigrate.Migrator{
	FS:     migrationFiles,
	Dir:    "migrations",
	Table:  "audit_schema_migrations",
	LockID: 0x4b415341554449, // "KASAUDI"
}

const postgresAppendTimeout = 3 * time.Second

// PostgresStore appends audit events to PostgreSQL. Append returns only after
// the INSERT is committed under the server's synchronous_commit setting, so a
// nil error means the server acknowledged the commit. An event identity is
// (tenant_id, service_id, event_id): an identical resend is reported as a
// *DuplicateError without a new row, and different content under the same
// identity is ErrEventConflict. It makes no claim of immutability, tamper
// resistance or exactly-once delivery.
type PostgresStore struct {
	mu     sync.RWMutex
	pool   *pgxpool.Pool
	closed bool
}

// OpenPostgres connects, verifies the connection and applies pending schema
// migrations. The connection string is never included in returned errors. Use it
// when the connecting role may run DDL; otherwise use MigratePostgres with a
// migration role and OpenPostgresVerified with the runtime role.
func OpenPostgres(ctx context.Context, connString string) (*PostgresStore, error) {
	return openPostgres(ctx, connString, true)
}

// OpenPostgresVerified connects without running DDL and fails if any embedded
// migration has not been applied. The runtime role then needs only INSERT and
// SELECT on audit_events and SELECT on audit_schema_migrations.
func OpenPostgresVerified(ctx context.Context, connString string) (*PostgresStore, error) {
	return openPostgres(ctx, connString, false)
}

// MigratePostgres applies pending migrations with the given (migration) role
// and closes the connection.
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
	pool, err := pgdb.Open(ctx, "audit", connString)
	if err != nil {
		return nil, err
	}
	if applyMigrations {
		err = auditMigrator.Apply(ctx, pool)
	} else {
		err = auditMigrator.Verify(ctx, pool)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("audit database schema: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Append(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateEvent(event); err != nil {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.pool == nil {
		return ErrStoreUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, postgresAppendTimeout)
	defer cancel()
	tag, err := s.pool.Exec(ctx, `INSERT INTO audit_events
		(contract_version, event_id, tenant_id, service_id, actor, action, resource, result,
		 correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (tenant_id, service_id, event_id) DO NOTHING`,
		event.ContractVersion, event.EventID, event.TenantID, event.ServiceID, event.Actor,
		event.Action, event.Resource, event.Result, event.CorrelationID,
		event.OccurredAt, event.ReceivedAt, occurred.UTC(), received.UTC())
	if err != nil {
		return ErrStoreUnavailable
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// Identity already stored: compare with the committed row.
	var stored Event
	err = s.pool.QueryRow(ctx, `SELECT contract_version, event_id, tenant_id, service_id, actor,
		action, resource, result, correlation_id, occurred_at_raw, received_at_raw
		FROM audit_events WHERE tenant_id = $1 AND service_id = $2 AND event_id = $3`,
		event.TenantID, event.ServiceID, event.EventID).Scan(&stored.ContractVersion, &stored.EventID,
		&stored.TenantID, &stored.ServiceID, &stored.Actor, &stored.Action, &stored.Resource,
		&stored.Result, &stored.CorrelationID, &stored.OccurredAt, &stored.ReceivedAt)
	if err != nil {
		return ErrStoreUnavailable
	}
	if sameContent(stored, event) {
		return &DuplicateError{Stored: stored}
	}
	return ErrEventConflict
}

// Ready reports whether the database currently answers a ping.
func (s *PostgresStore) Ready(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.pool == nil {
		return ErrStoreUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		return ErrStoreUnavailable
	}
	return nil
}

// Close waits for in-flight appends, then releases all connections.
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

var _ Repository = (*PostgresStore)(nil)
