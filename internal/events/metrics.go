package events

import (
	"context"
	"sync"
	"time"

	"kas-core-pilot/internal/metrics"
)

// DeliveryStats summarizes the delivery table.
type DeliveryStats struct {
	// ByState counts deliveries by state (pending, in_flight, delivered, dead).
	ByState map[string]int64
	// OldestDuePendingAge is how long the oldest pending delivery that is already
	// due has been waiting; zero when nothing is waiting.
	OldestDuePendingAge time.Duration
}

// DeliveryStats reads the counts with a single grouped query. It needs only
// SELECT on outbox_deliveries, which the runtime role already has.
func (s *PostgresStore) DeliveryStats(ctx context.Context, now time.Time) (DeliveryStats, error) {
	pool, release, err := s.usable()
	if err != nil {
		return DeliveryStats{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stats := DeliveryStats{ByState: map[string]int64{}}
	rows, err := pool.Query(ctx, `SELECT status, count(*) FROM outbox_deliveries GROUP BY status`)
	if err != nil {
		return DeliveryStats{}, ErrStoreUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return DeliveryStats{}, ErrStoreUnavailable
		}
		stats.ByState[state] = n
	}
	if rows.Err() != nil {
		return DeliveryStats{}, ErrStoreUnavailable
	}
	rows.Close()
	var oldest *time.Time
	if err := pool.QueryRow(ctx, `SELECT min(next_attempt_at) FROM outbox_deliveries WHERE status = 'pending' AND next_attempt_at <= $1`, now.UTC()).Scan(&oldest); err != nil {
		return DeliveryStats{}, ErrStoreUnavailable
	}
	if oldest != nil && now.After(*oldest) {
		stats.OldestDuePendingAge = now.Sub(*oldest)
	}
	return stats, nil
}

// StatsSource provides delivery statistics.
type StatsSource interface {
	DeliveryStats(ctx context.Context, now time.Time) (DeliveryStats, error)
}

// StatsCollector exposes delivery statistics as Prometheus gauges. The database
// is queried at most once per ttl, however often it is scraped. Labels are the
// four fixed states only: never tenants, consumers or event identifiers.
type StatsCollector struct {
	source StatsSource
	ttl    time.Duration
	now    func() time.Time

	mu       sync.Mutex
	cached   DeliveryStats
	cachedAt time.Time
	ok       bool
}

func NewStatsCollector(source StatsSource, ttl time.Duration, now func() time.Time) *StatsCollector {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &StatsCollector{source: source, ttl: ttl, now: now}
}

var deliveryStates = []string{"pending", "in_flight", "delivered", "dead"}

func (c *StatsCollector) Collect(e *metrics.Encoder) {
	c.mu.Lock()
	now := c.now()
	if c.cachedAt.IsZero() || now.Sub(c.cachedAt) >= c.ttl {
		stats, err := c.source.DeliveryStats(context.Background(), now)
		c.cachedAt, c.ok = now, err == nil
		c.cached = stats
	}
	stats, ok := c.cached, c.ok
	c.mu.Unlock()

	up := 0.0
	if ok {
		up = 1
	}
	e.Header("kas_outbox_stats_up", "Whether the last read of outbox statistics succeeded (1) or failed (0).", "gauge")
	e.Sample("kas_outbox_stats_up", nil, up)
	if !ok {
		return
	}
	e.Header("kas_outbox_deliveries", "Outbox deliveries by state.", "gauge")
	for _, state := range deliveryStates {
		e.Sample("kas_outbox_deliveries", []metrics.Label{{Name: "state", Value: state}}, float64(stats.ByState[state]))
	}
	e.Header("kas_outbox_oldest_due_pending_age_seconds", "Age of the oldest pending delivery that is already due; 0 when none is waiting.", "gauge")
	e.Sample("kas_outbox_oldest_due_pending_age_seconds", nil, stats.OldestDuePendingAge.Seconds())
}

var _ metrics.Collector = (*StatsCollector)(nil)
