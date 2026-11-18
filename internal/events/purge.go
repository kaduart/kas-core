package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"kas-core-pilot/internal/pgdb"
)

// Retention windows documented in the contract. Purge applies them only when an
// operator runs it; nothing is deleted automatically.
const (
	DeliveredRetention = 30 * 24 * time.Hour
	DeadRetention      = 90 * 24 * time.Hour
	purgeBatchSize     = 1000
)

// PurgeResult counts the rows removed (or, in a dry run, that would be removed).
type PurgeResult struct {
	DryRun    bool
	Delivered int64
	Dead      int64
	Events    int64
	Cutoffs   [2]time.Time // delivered cutoff, dead cutoff
}

// PurgePostgres removes delivered deliveries older than deliveredRetention, dead
// deliveries older than deadRetention, and then events that no longer have any
// delivery and were received before the delivered cutoff. Pending and in-flight
// deliveries are never touched, so their events stay. It needs DELETE rights, so
// run it with the migration (schema owner) role, never with the runtime role.
// Rows are deleted in small batches to keep locks short. With dryRun it only
// counts.
func PurgePostgres(ctx context.Context, connString string, now time.Time, deliveredRetention, deadRetention time.Duration, dryRun bool) (PurgeResult, error) {
	if deliveredRetention <= 0 || deadRetention <= 0 {
		return PurgeResult{}, errors.New("retention windows must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pool, err := pgdb.Open(ctx, "events", connString)
	if err != nil {
		return PurgeResult{}, err
	}
	defer pool.Close()
	deliveredCutoff, deadCutoff := now.UTC().Add(-deliveredRetention), now.UTC().Add(-deadRetention)
	result := PurgeResult{DryRun: dryRun, Cutoffs: [2]time.Time{deliveredCutoff, deadCutoff}}

	steps := []struct {
		name        string
		countSQL    string
		deleteSQL   string
		cutoff      time.Time
		destination *int64
	}{
		{"delivered deliveries",
			`SELECT count(*) FROM outbox_deliveries WHERE status = 'delivered' AND delivered_at < $1`,
			`DELETE FROM outbox_deliveries WHERE id IN (SELECT id FROM outbox_deliveries WHERE status = 'delivered' AND delivered_at < $1 ORDER BY id LIMIT $2)`,
			deliveredCutoff, &result.Delivered},
		{"dead deliveries",
			`SELECT count(*) FROM outbox_deliveries WHERE status = 'dead' AND dead_at < $1`,
			`DELETE FROM outbox_deliveries WHERE id IN (SELECT id FROM outbox_deliveries WHERE status = 'dead' AND dead_at < $1 ORDER BY id LIMIT $2)`,
			deadCutoff, &result.Dead},
		{"events",
			`SELECT count(*) FROM outbox_events e WHERE e.received_at < $1 AND NOT EXISTS (SELECT 1 FROM outbox_deliveries d WHERE d.event_pk = e.id)`,
			`DELETE FROM outbox_events WHERE id IN (SELECT e.id FROM outbox_events e WHERE e.received_at < $1 AND NOT EXISTS (SELECT 1 FROM outbox_deliveries d WHERE d.event_pk = e.id) ORDER BY e.id LIMIT $2)`,
			deliveredCutoff, &result.Events},
	}
	for _, step := range steps {
		if dryRun {
			// A dry run cannot see events freed by deliveries it did not delete,
			// so the events count is a lower bound.
			if err := pool.QueryRow(ctx, step.countSQL, step.cutoff).Scan(step.destination); err != nil {
				return result, fmt.Errorf("count %s: %w", step.name, err)
			}
			continue
		}
		for {
			tag, err := pool.Exec(ctx, step.deleteSQL, step.cutoff, purgeBatchSize)
			if err != nil {
				return result, fmt.Errorf("purge %s: %w", step.name, err)
			}
			*step.destination += tag.RowsAffected()
			if tag.RowsAffected() < purgeBatchSize {
				break
			}
		}
	}
	return result, nil
}
