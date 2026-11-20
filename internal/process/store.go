package process

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"kas-core-pilot/internal/pgdb"
	"kas-core-pilot/internal/pgmigrate"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var processMigrator = pgmigrate.Migrator{
	FS:     migrationFiles,
	Dir:    "migrations",
	Table:  "process_schema_migrations",
	LockID: 0x4b415350524f43, // "KASPROC"
}

// Event types the engine publishes to the outbox. They must be declared in the
// configuration's event_types, with a consumer bound to the external system.
const (
	EventCommandRequested   = "process.command.requested"
	EventCommandStatusQuery = "process.command.status_query"
	EventWaitOpened         = "process.wait.opened"

	// DefaultServiceID is the service identity of events the engine publishes.
	DefaultServiceID = "kas-process"

	dbTimeout = 5 * time.Second
)

var (
	ErrUnavailable       = errors.New("process store unavailable")
	ErrInvalidInput      = errors.New("invalid process request")
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("idempotency conflict")
	ErrInstanceActive    = errors.New("instance_active")
	ErrRoleNotAllowed    = errors.New("role_not_allowed")
	ErrActorNotInRole    = errors.New("actor_not_in_role")
	ErrTaskClosed        = errors.New("task_closed")
	ErrClockSkew         = errors.New("clock_skew")
	ErrCommandUnknown    = errors.New("command_unknown")
	ErrWaitTypeMismatch  = errors.New("wait_type_mismatch")
	ErrCommandNotPending = errors.New("command_not_pending")
	ErrResultRefRequired = errors.New("result_ref_required")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Registry tells which outbox consumers receive an event type for a tenant.
type Registry interface {
	ConsumersFor(tenantID, eventType string) []string
}

// Config holds tunables; zero values select the defaults of the spec.
type Config struct {
	Registry Registry
	// Now is the KAS server clock: received_at, executed_at and every decision
	// about whether a deadline has passed use it. Tests inject a fake clock.
	Now func() time.Time
	// Settle is added to cutoff_at to schedule a deadline timer (default 30s). It
	// must exceed the longest transaction that records a submission (5s).
	Settle         time.Duration
	MaxSkew        time.Duration // default 30s
	MaxReportDelay time.Duration // default 10m
	ServiceID      string        // default "kas-process"
	Logger         *slog.Logger
}

// Engine runs process definitions over PostgreSQL. It must share the database
// of the event outbox so that state changes and the events announcing them
// commit in one transaction.
type Engine struct {
	pool *pgxpool.Pool
	cfg  Config
	defs sync.Map // tenant/key/version -> *Definition

	// testHookAfterCommand, when set by tests, runs after a command and its
	// outbox event were written and before the transaction commits.
	testHookAfterCommand func() error
}

// Open connects, applies (or verifies) the process migrations and checks that
// the outbox tables exist in the same database.
func Open(ctx context.Context, connString string, applyMigrations bool, cfg Config) (*Engine, error) {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "process", connString)
	if err != nil {
		return nil, err
	}
	if applyMigrations {
		err = processMigrator.Apply(ctx, pool)
	} else {
		err = processMigrator.Verify(ctx, pool)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("process database schema: %w", err)
	}
	var outbox *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('outbox_events')::text`).Scan(&outbox); err != nil || outbox == nil {
		pool.Close()
		return nil, errors.New("process store needs the events outbox tables in the same database (run the events migrations first)")
	}
	return NewEngine(pool, cfg), nil
}

// Migrate applies the process migrations with a migration-capable role.
func Migrate(ctx context.Context, connString string) error {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "process", connString)
	if err != nil {
		return err
	}
	defer pool.Close()
	return processMigrator.Apply(ctx, pool)
}

// NewEngine wraps an open pool whose schema is already migrated.
func NewEngine(pool *pgxpool.Pool, cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Settle <= 0 {
		cfg.Settle = 30 * time.Second
	}
	if cfg.MaxSkew <= 0 {
		cfg.MaxSkew = 30 * time.Second
	}
	if cfg.MaxReportDelay <= 0 {
		cfg.MaxReportDelay = 10 * time.Minute
	}
	if cfg.ServiceID == "" {
		cfg.ServiceID = DefaultServiceID
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Engine{pool: pool, cfg: cfg}
}

// Close releases the pool.
func (e *Engine) Close() { e.pool.Close() }

// Ready pings the database.
func (e *Engine) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := e.pool.Ping(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (e *Engine) now() time.Time { return pgTime(e.cfg.Now()) }

// pgTime normalizes an instant to what Postgres timestamptz stores (UTC,
// microseconds), so an identical resend compares equal to the stored row.
func pgTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// inTx runs fn in a transaction with the module's timeout and maps unexpected
// errors to ErrUnavailable.
func (e *Engine) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}

func uniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}

// known reports whether err is one of the engine's own sentinel errors.
func known(err error) bool {
	for _, k := range []error{ErrInvalidInput, ErrNotFound, ErrConflict, ErrInstanceActive, ErrRoleNotAllowed,
		ErrActorNotInRole, ErrTaskClosed, ErrClockSkew, ErrCommandUnknown, ErrWaitTypeMismatch,
		ErrCommandNotPending, ErrResultRefRequired, ErrInvalidDefinition, ErrUnavailable} {
		if errors.Is(err, k) {
			return true
		}
	}
	return false
}

func mapErr(err error) error {
	if err == nil || known(err) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}
