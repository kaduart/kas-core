// Package pgmigrate applies and verifies ordered, embedded SQL migrations.
// Each module owns its migration files and a separate tracking table, so
// modules stay independent while sharing one tested mechanism.
package pgmigrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var tablePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Migrator describes one module's migrations.
type Migrator struct {
	// FS holds the .sql files in Dir, applied in lexical order of file name.
	FS  fs.FS
	Dir string
	// Table tracks applied file names (created on first Apply).
	Table string
	// LockID is the advisory lock key serializing concurrent Apply calls.
	LockID int64
}

func (m Migrator) validate() error {
	if m.FS == nil || !tablePattern.MatchString(m.Table) {
		return errors.New("migrator requires a file system and a valid tracking table name")
	}
	return nil
}

// Names returns the migration file names in the order they are applied.
func (m Migrator) Names() ([]string, error) {
	entries, err := fs.ReadDir(m.FS, m.Dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Apply runs every pending migration, each in its own transaction together with
// its tracking row. A failing migration is rolled back and later ones do not
// run. Concurrent callers are serialized by an advisory lock.
func (m Migrator) Apply(ctx context.Context, pool *pgxpool.Pool) error {
	if err := m.validate(); err != nil {
		return err
	}
	names, err := m.Names()
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", m.LockID); err != nil {
		return err
	}
	defer func() {
		// Fresh context so the lock is released even after cancellation.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", m.LockID)
	}()
	table := pgx.Identifier{m.Table}.Sanitize()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		name TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE name = $1)", name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := fs.ReadFile(m.FS, m.Dir+"/"+name)
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
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (name) VALUES ($1)", name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Verify fails if any migration has not been applied. It runs no DDL and needs
// only SELECT on the tracking table, so a least-privilege runtime role can use it.
func (m Migrator) Verify(ctx context.Context, pool *pgxpool.Pool) error {
	if err := m.validate(); err != nil {
		return err
	}
	names, err := m.Names()
	if err != nil {
		return err
	}
	rows, err := pool.Query(ctx, "SELECT name FROM "+pgx.Identifier{m.Table}.Sanitize())
	if err != nil {
		return errors.New("migration table is not readable; run migrations with the migration role")
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
		return fmt.Errorf("pending migrations %s; run migrations with the migration role", strings.Join(pending, ", "))
	}
	return nil
}
