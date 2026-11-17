package audit

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID is an arbitrary application-level advisory lock key that
// serializes schema migration across concurrently starting KAS processes.
const migrationLockID int64 = 0x4b415341554449 // "KASAUDI"

const (
	postgresOpenTimeout   = 10 * time.Second
	postgresAppendTimeout = 3 * time.Second
)

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
	if strings.TrimSpace(connString) == "" {
		return nil, errors.New("audit database connection string is required")
	}
	ctx, cancel := context.WithTimeout(ctx, postgresOpenTimeout)
	defer cancel()
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, errors.New("audit database connection string is invalid")
	}
	if err := requireSecureTransport(config); err != nil {
		return nil, err
	}
	if config.MaxConns > 8 || config.MaxConns < 2 {
		config.MaxConns = 8
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("audit database connection could not be created")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("audit database is unreachable")
	}
	if applyMigrations {
		err = migrate(ctx, pool)
	} else {
		err = verifyMigrations(ctx, pool)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("audit database schema: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// requireSecureTransport rejects any connection attempt to a non-loopback host
// that could run without TLS (sslmode=disable, allow or prefer). TLS modes
// require, verify-ca and verify-full pass; verify-full is recommended because
// require encrypts without authenticating the server.
func requireSecureTransport(config *pgxpool.Config) error {
	cc := config.ConnConfig
	if !isLocalDatabaseHost(cc.Host) && cc.TLSConfig == nil {
		return errTLSRequired
	}
	for _, fallback := range cc.Fallbacks {
		if !isLocalDatabaseHost(fallback.Host) && fallback.TLSConfig == nil {
			return errTLSRequired
		}
	}
	return nil
}

var errTLSRequired = errors.New("TLS is required for a non-loopback audit database host; use sslmode=require, verify-ca or verify-full")

func isLocalDatabaseHost(host string) bool {
	if host == "" || strings.HasPrefix(host, "/") || strings.EqualFold(host, "localhost") {
		return true // empty means the default local socket
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func verifyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	names, err := embeddedMigrationNames()
	if err != nil {
		return err
	}
	rows, err := pool.Query(ctx, "SELECT name FROM audit_schema_migrations")
	if err != nil {
		return errors.New("migration table is not readable; run migrations with the migration role (-audit-migrate-only)")
	}
	defer rows.Close()
	applied := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var pending []string
	for _, name := range names {
		if !applied[name] {
			pending = append(pending, name)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("pending migrations %s; run migrations with the migration role (-audit-migrate-only)", strings.Join(pending, ", "))
	}
	return nil
}

func embeddedMigrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return err
	}
	defer func() {
		// Use a fresh context so the lock is released even after cancellation.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS audit_schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := embeddedMigrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM audit_schema_migrations WHERE name = $1)", name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO audit_schema_migrations (name) VALUES ($1)", name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
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
