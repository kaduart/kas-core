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
		 correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at,
		 entity_type, entity_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (tenant_id, service_id, event_id) DO NOTHING`,
		event.ContractVersion, event.EventID, event.TenantID, event.ServiceID, event.Actor,
		event.Action, event.Resource, event.Result, event.CorrelationID,
		event.OccurredAt, event.ReceivedAt, occurred.UTC(), received.UTC(),
		nullable(event.EntityType), nullable(event.EntityID))
	if err != nil {
		return ErrStoreUnavailable
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// Identity already stored: compare with the committed row.
	var stored Event
	var entityType, entityID *string
	err = s.pool.QueryRow(ctx, `SELECT contract_version, event_id, tenant_id, service_id, actor,
		action, resource, result, correlation_id, occurred_at_raw, received_at_raw, entity_type, entity_id
		FROM audit_events WHERE tenant_id = $1 AND service_id = $2 AND event_id = $3`,
		event.TenantID, event.ServiceID, event.EventID).Scan(&stored.ContractVersion, &stored.EventID,
		&stored.TenantID, &stored.ServiceID, &stored.Actor, &stored.Action, &stored.Resource,
		&stored.Result, &stored.CorrelationID, &stored.OccurredAt, &stored.ReceivedAt, &entityType, &entityID)
	if err != nil {
		return ErrStoreUnavailable
	}
	stored.EntityType, stored.EntityID = deref(entityType), deref(entityID)
	if sameContent(stored, event) {
		return &DuplicateError{Stored: stored}
	}
	return ErrEventConflict
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Query reads one page for the tenant of q in the stable order (received_at, id).
// The tenant condition is part of every query and of the index prefix, so no
// other tenant's rows can be read. It needs only SELECT on audit_events.
func (s *PostgresStore) Query(ctx context.Context, q Query, after *Position) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.pool == nil {
		return Page{}, ErrStoreUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, postgresAppendTimeout)
	defer cancel()

	args := []any{q.TenantID}
	where := "tenant_id = $1"
	add := func(cond string, value any) {
		args = append(args, value)
		where += " AND " + fmt.Sprintf(cond, len(args))
	}
	if !q.Since.IsZero() {
		add("received_at >= $%d", q.Since.UTC())
	}
	if q.EntityType != "" {
		add("entity_type = $%d", q.EntityType)
	}
	if q.EntityID != "" {
		add("entity_id = $%d", q.EntityID)
	}
	if after != nil {
		args = append(args, time.UnixMicro(after.RecordedMicros).UTC(), after.ID)
		where += fmt.Sprintf(" AND (received_at, id) > ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, q.Limit+1)
	rows, err := s.pool.Query(ctx, `SELECT id, received_at, contract_version, event_id, tenant_id, service_id, actor,
		action, resource, result, correlation_id, occurred_at_raw, received_at_raw, entity_type, entity_id
		FROM audit_events WHERE `+where+fmt.Sprintf(" ORDER BY received_at, id LIMIT $%d", len(args)), args...)
	if err != nil {
		return Page{}, ErrStoreUnavailable
	}
	defer rows.Close()
	var page Page
	var last Position
	for rows.Next() {
		var id int64
		var receivedAt time.Time
		var e Event
		var entityType, entityID *string
		if err := rows.Scan(&id, &receivedAt, &e.ContractVersion, &e.EventID, &e.TenantID, &e.ServiceID, &e.Actor,
			&e.Action, &e.Resource, &e.Result, &e.CorrelationID, &e.OccurredAt, &e.ReceivedAt, &entityType, &entityID); err != nil {
			return Page{}, ErrStoreUnavailable
		}
		e.EntityType, e.EntityID = deref(entityType), deref(entityID)
		if len(page.Items) == q.Limit {
			page.NextCursor = EncodeCursor(last)
			break
		}
		page.Items = append(page.Items, e)
		last = Position{RecordedMicros: receivedAt.UnixMicro(), ID: id}
	}
	if err := rows.Err(); err != nil {
		return Page{}, ErrStoreUnavailable
	}
	return page, nil
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
