package audit

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
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
// nil error means the server acknowledged the commit. It makes no claim of
// immutability, tamper resistance or exactly-once delivery: a retried submission
// with the same event_id is stored again, as with the JSON Lines store.
type PostgresStore struct {
	mu     sync.RWMutex
	pool   *pgxpool.Pool
	closed bool
}

// OpenPostgres connects, verifies the connection and applies pending schema
// migrations. The connection string is never included in returned errors.
func OpenPostgres(ctx context.Context, connString string) (*PostgresStore, error) {
	if strings.TrimSpace(connString) == "" {
		return nil, errors.New("audit database connection string is required")
	}
	ctx, cancel := context.WithTimeout(ctx, postgresOpenTimeout)
	defer cancel()
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, errors.New("audit database connection string is invalid")
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
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("audit database migration failed: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
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
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
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
	_, err = s.pool.Exec(ctx, `INSERT INTO audit_events
		(contract_version, event_id, tenant_id, service_id, actor, action, resource, result,
		 correlation_id, occurred_at_raw, received_at_raw, occurred_at, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		event.ContractVersion, event.EventID, event.TenantID, event.ServiceID, event.Actor,
		event.Action, event.Resource, event.Result, event.CorrelationID,
		event.OccurredAt, event.ReceivedAt, occurred.UTC(), received.UTC())
	if err != nil {
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
