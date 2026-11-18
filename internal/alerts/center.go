// Package alerts is a small, generic alert center: deduplication, cooldown
// with aggregation, severity escalation and retry of failed deliveries.
//
// It knows nothing about the CRM, MongoDB or any delivery channel. Producers
// raise Alerts; the center decides when a Notification goes to a Sink.
//
// Rules:
//   - Dedup: the same alert key (source, kind, subject) raised again inside
//     DedupWindow is counted, not notified again.
//   - Cooldown: per group (source, kind), at most one notification per
//     Cooldown. Alerts raised during the cooldown are aggregated and go out in
//     the next notification (Flush or the next Raise after the cooldown), so
//     nothing is silently lost.
//   - Escalation: an alert more severe than the last one notified for its
//     group bypasses the cooldown.
//   - Delivery failure: the group keeps its pending alerts and its cooldown is
//     not started; the next Raise or Flush tries again.
//
// State is in memory and per process. Running several instances means several
// independent centers; put the center behind the same leader lease the
// producers already use, or accept one notification per instance.
package alerts

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Severity orders alerts. Higher is worse.
type Severity int

const (
	Info Severity = iota + 1
	Warning
	Critical
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Critical:
		return "critical"
	}
	return "unknown"
}

// Alert is one observation from a producer.
type Alert struct {
	Source   string // producer, e.g. "outbox-recovery"
	Kind     string // what happened, e.g. "entry_exhausted"
	Subject  string // what it happened to, e.g. an entry ID
	Severity Severity
	Summary  string
}

// Key identifies an alert for deduplication.
func (a Alert) Key() string { return a.Source + "\xff" + a.Kind + "\xff" + a.Subject }

func (a Alert) group() string { return a.Source + "\xff" + a.Kind }

// Notification is what reaches the sink: one or more alerts of a group.
type Notification struct {
	Source     string    `json:"source"`
	Kind       string    `json:"kind"`
	Severity   string    `json:"severity"`
	Summary    string    `json:"summary"`         // summary of the most severe, then latest, alert
	Subjects   []string  `json:"subjects"`        // distinct subjects, capped at Config.MaxSubjects
	Alerts     int       `json:"alerts"`          // distinct alerts covered
	Duplicates int       `json:"duplicates"`      // repeats folded by dedup since the last notification
	Omitted    int       `json:"omittedSubjects"` // subjects beyond MaxSubjects
	FirstAt    time.Time `json:"firstAt"`
	LastAt     time.Time `json:"lastAt"`
	Escalation bool      `json:"escalation,omitempty"` // sent during cooldown because severity rose
}

// Sink delivers notifications. An error means "not delivered, try again".
type Sink interface {
	Deliver(ctx context.Context, n Notification) error
}

// Config tunes the center.
type Config struct {
	DedupWindow time.Duration
	Cooldown    time.Duration
	MaxSubjects int // per notification; the rest is counted in Omitted
}

// DefaultConfig: repeats folded for 1 h, one notification per group every 15 min.
func DefaultConfig() Config {
	return Config{DedupWindow: time.Hour, Cooldown: 15 * time.Minute, MaxSubjects: 20}
}

// Decision says what Raise did.
type Decision string

const (
	Delivered      Decision = "delivered"
	Duplicate      Decision = "duplicate"       // same key inside the dedup window
	Deferred       Decision = "deferred"        // aggregated, waits for the cooldown
	DeliveryFailed Decision = "delivery_failed" // kept pending, retried later
)

// ErrInvalidAlert is returned for an alert without source, kind or a known severity.
var ErrInvalidAlert = errors.New("alert needs source, kind and severity")

type group struct {
	lastSent     time.Time
	lastSeverity Severity
	pending      []Alert
	pendingSet   map[string]bool
	duplicates   int
	firstAt      time.Time
	lastAt       time.Time
}

// Center is safe for concurrent use.
type Center struct {
	cfg  Config
	sink Sink
	now  func() time.Time

	mu     sync.Mutex
	seen   map[string]time.Time // key -> first raise inside the current window
	groups map[string]*group
	stats  map[Decision]int
}

// New returns a center. now may be nil (time.Now).
func New(cfg Config, sink Sink, now func() time.Time) *Center {
	if now == nil {
		now = time.Now
	}
	if cfg.MaxSubjects <= 0 {
		cfg.MaxSubjects = 20
	}
	return &Center{cfg: cfg, sink: sink, now: now, seen: map[string]time.Time{}, groups: map[string]*group{}, stats: map[Decision]int{}}
}

// Raise records an alert and delivers what is due for its group.
func (c *Center) Raise(ctx context.Context, a Alert) (Decision, error) {
	if a.Source == "" || a.Kind == "" || a.Severity < Info || a.Severity > Critical {
		return "", ErrInvalidAlert
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	g := c.groups[a.group()]
	if g == nil {
		g = &group{pendingSet: map[string]bool{}}
		c.groups[a.group()] = g
	}

	if first, ok := c.seen[a.Key()]; ok && now.Sub(first) < c.cfg.DedupWindow {
		g.duplicates++
		c.stats[Duplicate]++
		return Duplicate, nil
	}
	c.seen[a.Key()] = now

	if !g.pendingSet[a.Key()] {
		g.pendingSet[a.Key()] = true
		g.pending = append(g.pending, a)
	}
	if g.firstAt.IsZero() {
		g.firstAt = now
	}
	g.lastAt = now

	inCooldown := !g.lastSent.IsZero() && now.Sub(g.lastSent) < c.cfg.Cooldown
	escalation := inCooldown && a.Severity > g.lastSeverity
	if inCooldown && !escalation {
		c.stats[Deferred]++
		return Deferred, nil
	}
	if err := c.deliverLocked(ctx, g, now, escalation); err != nil {
		c.stats[DeliveryFailed]++
		return DeliveryFailed, err
	}
	c.stats[Delivered]++
	return Delivered, nil
}

// Flush delivers every group whose cooldown has passed and has pending alerts,
// and forgets dedup keys older than the window. Call it periodically.
func (c *Center) Flush(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, t := range c.seen {
		if now.Sub(t) >= c.cfg.DedupWindow {
			delete(c.seen, k)
		}
	}
	names := make([]string, 0, len(c.groups))
	for name := range c.groups {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		g := c.groups[name]
		if len(g.pending) == 0 || (!g.lastSent.IsZero() && now.Sub(g.lastSent) < c.cfg.Cooldown) {
			continue
		}
		if err := c.deliverLocked(ctx, g, now, false); err != nil {
			c.stats[DeliveryFailed]++
			errs = append(errs, err)
			continue
		}
		c.stats[Delivered]++
	}
	return errors.Join(errs...)
}

// Pending returns how many alerts wait for delivery, across groups.
func (c *Center) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, g := range c.groups {
		n += len(g.pending)
	}
	return n
}

// Stats returns cumulative decision counters.
func (c *Center) Stats() map[Decision]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[Decision]int, len(c.stats))
	for k, v := range c.stats {
		out[k] = v
	}
	return out
}

func (c *Center) deliverLocked(ctx context.Context, g *group, now time.Time, escalation bool) error {
	n := Notification{FirstAt: g.firstAt, LastAt: g.lastAt, Duplicates: g.duplicates, Alerts: len(g.pending), Escalation: escalation}
	var top Alert
	for _, a := range g.pending {
		if top.Severity == 0 || a.Severity >= top.Severity {
			top = a
		}
		if a.Subject == "" {
			continue
		}
		if len(n.Subjects) < c.cfg.MaxSubjects {
			n.Subjects = append(n.Subjects, a.Subject)
		} else {
			n.Omitted++
		}
	}
	n.Source, n.Kind, n.Severity, n.Summary = top.Source, top.Kind, top.Severity.String(), top.Summary
	if c.sink == nil {
		return errors.New("alerts: no sink")
	}
	if err := c.sink.Deliver(ctx, n); err != nil {
		return err
	}
	g.lastSent, g.lastSeverity = now, top.Severity
	g.pending, g.pendingSet, g.duplicates = nil, map[string]bool{}, 0
	g.firstAt, g.lastAt = time.Time{}, time.Time{}
	return nil
}
