// Package modules remembers the contract state of each catalog module per
// tenant and announces every change on the outbox (spec "KAS por módulos",
// section 8.1). The configuration file is the source of truth; Sync compares it
// with the last applied state at startup. A change and the event announcing it
// commit in one transaction. The package holds no business rule.
package modules

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/pgdb"
	"kas-core-pilot/internal/pgmigrate"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrator = pgmigrate.Migrator{
	FS:     migrationFiles,
	Dir:    "migrations",
	Table:  "module_schema_migrations",
	LockID: 0x4b41534d4f4453, // "KASMODS"
}

// Event types announcing a module change. Declare them in event_types (with a
// consumer) for other systems to receive them; without a consumer the event is
// still recorded on the outbox.
const (
	EventActivated = "module.activated"
	EventPaused    = "module.paused"
	EventRemoved   = "module.removed"

	// ServiceID is the service identity of the events Sync publishes.
	ServiceID = "kas-core"
)

// Source is what Sync needs from the configuration.
type Source interface {
	Version() string
	ContractedModules() map[string]map[string]string
	ConsumersFor(tenantID, eventType string) []string
}

// Change is one applied transition.
type Change struct {
	TenantID  string
	Module    string
	From, To  string
	EventType string
	EventID   string
}

// Store keeps module states in the events database.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// Open connects, applies (or verifies) the migrations and checks that the
// outbox tables exist in the same database.
func Open(ctx context.Context, connString string, applyMigrations bool) (*Store, error) {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "modules", connString)
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
		return nil, fmt.Errorf("modules database schema: %w", err)
	}
	var outbox *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('outbox_events')::text`).Scan(&outbox); err != nil || outbox == nil {
		pool.Close()
		return nil, errors.New("module store needs the events outbox tables in the same database (run the events migrations first)")
	}
	return &Store{pool: pool, now: time.Now}, nil
}

// Migrate applies the migrations with a migration-capable role.
func Migrate(ctx context.Context, connString string) error {
	ctx, cancel := context.WithTimeout(ctx, pgdb.OpenTimeout)
	defer cancel()
	pool, err := pgdb.Open(ctx, "modules", connString)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrator.Apply(ctx, pool)
}

func (s *Store) Close() { s.pool.Close() }

// Sync applies the configured states. Running it again with the same
// configuration changes nothing and publishes nothing. A module that disappears
// from a tenant's block becomes "absent" (EventRemoved); history is never
// deleted, and the module's data elsewhere is untouched.
func (s *Store) Sync(ctx context.Context, src Source) ([]Change, error) {
	desired := src.ContractedModules()
	version := src.Version()
	var changes []Change
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x4b41534d4f4453)); err != nil {
			return err
		}
		stored := map[string]map[string]string{}
		rows, err := tx.Query(ctx, `SELECT tenant_id, module_key, status FROM module_states`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var tenant, module, status string
			if err := rows.Scan(&tenant, &module, &status); err != nil {
				rows.Close()
				return err
			}
			if stored[tenant] == nil {
				stored[tenant] = map[string]string{}
			}
			stored[tenant][module] = status
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		type key struct{ tenant, module string }
		targets := map[key]string{}
		for tenant, mods := range desired {
			for module, status := range mods {
				targets[key{tenant, module}] = status
			}
		}
		for tenant, mods := range stored {
			for module, status := range mods {
				if _, still := targets[key{tenant, module}]; !still && status != configstore.ModuleStatusAbsent {
					targets[key{tenant, module}] = configstore.ModuleStatusAbsent
				}
			}
		}
		keys := make([]key, 0, len(targets))
		for k := range targets {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].tenant != keys[j].tenant {
				return keys[i].tenant < keys[j].tenant
			}
			return keys[i].module < keys[j].module
		})

		now := s.now().UTC()
		for _, k := range keys {
			to := targets[k]
			from, known := stored[k.tenant][k.module]
			if !known {
				from = configstore.ModuleStatusAbsent
			}
			if from == to {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO module_states (tenant_id, module_key, status, config_version, changed_at)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (tenant_id, module_key) DO UPDATE SET status = EXCLUDED.status,
				config_version = EXCLUDED.config_version, changed_at = EXCLUDED.changed_at`,
				k.tenant, k.module, to, version, now); err != nil {
				return err
			}
			var historyID int64
			if err := tx.QueryRow(ctx, `INSERT INTO module_state_history (tenant_id, module_key, from_status, to_status, config_version, recorded_at)
				VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, k.tenant, k.module, from, to, version, now).Scan(&historyID); err != nil {
				return err
			}
			eventType := EventActivated
			switch to {
			case configstore.ModuleStatusPaused:
				eventType = EventPaused
			case configstore.ModuleStatusAbsent:
				eventType = EventRemoved
			}
			payload, _ := json.Marshal(map[string]string{"module": k.module, "status": to, "previous_status": from})
			eventID := "module-state-" + strconv.FormatInt(historyID, 10)
			ts := now.Format(time.RFC3339Nano)
			if _, err := events.PublishTx(ctx, tx, events.Event{
				ContractVersion: events.ContractVersion, EventID: eventID, TenantID: k.tenant, ServiceID: ServiceID,
				EventType: eventType, CorrelationID: eventID, OccurredAt: ts, ReceivedAt: ts, Payload: payload,
			}, src.ConsumersFor(k.tenant, eventType)); err != nil {
				return err
			}
			changes = append(changes, Change{TenantID: k.tenant, Module: k.module, From: from, To: to, EventType: eventType, EventID: eventID})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("module state sync: %w", err)
	}
	return changes, nil
}

// HistoryEntry is one recorded transition.
type HistoryEntry struct {
	Module, From, To, ConfigVersion string
	RecordedAt                      time.Time
}

// History returns the transitions of one tenant, oldest first. Only that
// tenant's rows are read.
func (s *Store) History(ctx context.Context, tenantID string) ([]HistoryEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT module_key, from_status, to_status, config_version, recorded_at
		FROM module_state_history WHERE tenant_id = $1 ORDER BY id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.Module, &h.From, &h.To, &h.ConfigVersion, &h.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
