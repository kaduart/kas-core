package outboxrecovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Mode selects whether decisions are written.
type Mode string

const (
	// ModeShadow reads and reports; it never writes.
	ModeShadow Mode = "shadow"
	// ModeExecutor applies decisions with conditional writes.
	ModeExecutor Mode = "executor"
)

// ParseMode validates a mode name.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeShadow, ModeExecutor:
		return Mode(s), nil
	}
	return "", fmt.Errorf("unknown mode %q (use shadow or executor)", s)
}

// Store is what an adapter for a concrete outbox must provide.
//
// Recover and Exhaust are conditional: they apply only if the entry is still
// exactly as observed (same status, attempt counter, retry instant and
// recovery bookkeeping). They report applied=false when someone else changed
// the entry first; that is a lost race, not an error.
type Store interface {
	// Scan returns failed, not yet exhausted entries with an ID strictly
	// greater than after, in ID order, at most limit.
	Scan(ctx context.Context, after string, limit int) ([]Entry, error)
	Recover(ctx context.Context, e Entry, now time.Time) (applied bool, err error)
	Exhaust(ctx context.Context, e Entry, reason string, now time.Time) (applied bool, err error)
	// Census counts entries for the shadow report (no payload is read).
	Census(ctx context.Context, now time.Time, stalePendingAfter time.Duration) (Census, error)
}

// Census is a read-only picture of the outbox.
type Census struct {
	ByStatus map[string]int64 `json:"byStatus"`
	// FailedExhausted: failed entries this package already gave up on.
	FailedExhausted int64 `json:"failedExhausted"`
	// PendingStale: pending entries due for longer than the stale threshold,
	// a sign that no dispatcher is running.
	PendingStale int64 `json:"pendingStale"`
}

// Alert is raised for every entry this package gives up on.
type Alert struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// AlertSink receives alerts. Delivery to a monitored channel is the caller's
// job; a sink error is reported but does not undo the write.
type AlertSink func(ctx context.Context, a Alert) error

// Cycle is the outcome of one pass.
type Cycle struct {
	Mode      Mode                `json:"mode"`
	At        time.Time           `json:"at"`
	Scanned   int                 `json:"scanned"`
	Decisions map[string]int      `json:"decisions"` // reason -> count
	Recovered int                 `json:"recovered"`
	Exhausted int                 `json:"exhausted"`
	LostRaces int                 `json:"lostRaces"`
	AlertErrs int                 `json:"alertErrors"`
	Census    *Census             `json:"census,omitempty"`
	WouldDo   map[string][]string `json:"wouldDo,omitempty"` // shadow: action -> IDs
}

// Runner executes cycles.
type Runner struct {
	Config Config
	Mode   Mode
	Store  Store
	Alert  AlertSink
	Now    func() time.Time
	// StalePendingAfter feeds the census; zero disables the stale count.
	StalePendingAfter time.Duration
	// MaxPages bounds one cycle (pages of Config.BatchSize). Zero means 1.
	MaxPages int
}

// ErrInvalid is returned for a misconfigured runner.
var ErrInvalid = errors.New("invalid runner")

// RunCycle scans failed entries page by page and applies the policy. Each
// write is independent and conditional, so a cycle interrupted at any point
// (crash, cancellation, database outage) can simply run again.
func (r *Runner) RunCycle(ctx context.Context) (Cycle, error) {
	if r.Store == nil || r.Now == nil {
		return Cycle{}, ErrInvalid
	}
	if err := r.Config.Validate(); err != nil {
		return Cycle{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if r.Mode != ModeShadow && r.Mode != ModeExecutor {
		return Cycle{}, fmt.Errorf("%w: mode %q", ErrInvalid, r.Mode)
	}
	now := r.Now()
	c := Cycle{Mode: r.Mode, At: now, Decisions: map[string]int{}}
	if r.Mode == ModeShadow {
		c.WouldDo = map[string][]string{}
	}
	pages := r.MaxPages
	if pages < 1 {
		pages = 1
	}
	after := ""
	for p := 0; p < pages; p++ {
		batch, err := r.Store.Scan(ctx, after, r.Config.BatchSize)
		if err != nil {
			return c, fmt.Errorf("scan: %w", err)
		}
		for _, e := range batch {
			if err := ctx.Err(); err != nil {
				return c, err
			}
			after = e.ID
			c.Scanned++
			d := Decide(r.Config, now, e)
			c.Decisions[d.Reason]++
			if d.Action == ActionNone {
				continue
			}
			if r.Mode == ModeShadow {
				c.WouldDo[string(d.Action)] = append(c.WouldDo[string(d.Action)], e.ID)
				continue
			}
			var applied bool
			switch d.Action {
			case ActionRecover:
				applied, err = r.Store.Recover(ctx, e, now)
			case ActionExhaust:
				applied, err = r.Store.Exhaust(ctx, e, d.Reason, now)
			}
			if err != nil {
				return c, fmt.Errorf("%s %s: %w", d.Action, e.ID, err)
			}
			if !applied {
				c.LostRaces++
				continue
			}
			if d.Action == ActionRecover {
				c.Recovered++
				continue
			}
			c.Exhausted++
			if r.Alert != nil {
				if err := r.Alert(ctx, Alert{ID: e.ID, Reason: d.Reason}); err != nil {
					c.AlertErrs++
				}
			}
		}
		if len(batch) < r.Config.BatchSize {
			break
		}
	}
	for _, ids := range c.WouldDo {
		sort.Strings(ids)
	}
	if r.Mode == ModeShadow {
		cen, err := r.Store.Census(ctx, now, r.StalePendingAfter)
		if err != nil {
			return c, fmt.Errorf("census: %w", err)
		}
		c.Census = &cen
	}
	return c, nil
}
