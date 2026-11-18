// Package alerts is a small, generic alert center: deduplication, cooldown
// with aggregation, severity escalation and retry of failed deliveries.
//
// It knows nothing about the CRM, MongoDB or any delivery channel. Producers
// raise Alerts; the center decides when a Notification goes to a Sink.
//
// Rules:
//
//   - Dedup: the same alert key (source, kind, subject) raised again inside
//     DedupWindow is counted, not notified again.
//
//   - Cooldown: per group (source, kind), at most one notification per
//     Cooldown. Alerts raised during the cooldown are aggregated and go out in
//     the next notification (Flush or the next Raise after the cooldown), so
//     nothing is silently lost.
//
//   - Escalation: an alert more severe than the last one notified for its
//     group bypasses the cooldown.
//
//   - Delivery failure: the group keeps its pending alerts and its cooldown is
//     not started; the next Raise or Flush tries again.
//
//   - Tenants: every key and group includes the tenant, so one tenant's
//     repeats, cooldown or failing destination never hold back another's.
//
// State lives in memory and, with SetStore, is saved after every change and
// restored on start (Restore), so a restart keeps dedup, cooldowns and alerts
// whose delivery failed. Delivery happens before the save: a crash between
// the two re-sends that notification after the restart (at least once; never
// lost, possibly repeated). Several instances need one store document each or
// a leader lease; two centers sharing a document overwrite each other.
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
	Tenant   string // isolation boundary (clinic/customer); "" is a single-tenant install
	Source   string // producer, e.g. "outbox-recovery"
	Kind     string // what happened, e.g. "entry_exhausted"
	Subject  string // what it happened to, e.g. an entry ID
	Severity Severity
	Summary  string
}

// Key identifies an alert for deduplication.
func (a Alert) Key() string { return a.group() + "\x1f" + a.Subject }

func (a Alert) group() string { return a.Tenant + "\x1f" + a.Source + "\x1f" + a.Kind }

// Notification is what reaches the sink: one or more alerts of a group.
type Notification struct {
	Tenant     string    `json:"tenant,omitempty"`
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

// GroupState is the persisted form of a group.
type GroupState struct {
	LastSent     time.Time `json:"lastSent" bson:"lastSent"`
	LastSeverity Severity  `json:"lastSeverity" bson:"lastSeverity"`
	Pending      []Alert   `json:"pending" bson:"pending"`
	Duplicates   int       `json:"duplicates" bson:"duplicates"`
	FirstAt      time.Time `json:"firstAt" bson:"firstAt"`
	LastAt       time.Time `json:"lastAt" bson:"lastAt"`
}

// Snapshot is everything a restart needs.
type Snapshot struct {
	Seen   map[string]time.Time  `json:"seen" bson:"seen"`
	Groups map[string]GroupState `json:"groups" bson:"groups"`
}

// Store saves and loads a center's snapshot. Load returns (nil, nil) when
// nothing was saved yet.
type Store interface {
	Load(ctx context.Context) (*Snapshot, error)
	Save(ctx context.Context, s Snapshot) error
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

	store       Store
	persistErrs int
}

// SetStore makes the center save its state after every change.
func (c *Center) SetStore(s Store) { c.mu.Lock(); c.store = s; c.mu.Unlock() }

// Restore loads a saved snapshot into an empty center. Call it before the
// first Raise. Pending alerts come back pending; they go out on the next
// Flush or Raise of their group, subject to the restored cooldown.
func (c *Center) Restore(ctx context.Context, s Store) error {
	snap, err := s.Load(ctx)
	if err != nil || snap == nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, t := range snap.Seen {
		c.seen[k] = t
	}
	for name, gs := range snap.Groups {
		g := &group{lastSent: gs.LastSent, lastSeverity: gs.LastSeverity, duplicates: gs.Duplicates,
			firstAt: gs.FirstAt, lastAt: gs.LastAt, pendingSet: map[string]bool{}}
		for _, a := range gs.Pending {
			g.pending = append(g.pending, a)
			g.pendingSet[a.Key()] = true
		}
		c.groups[name] = g
	}
	return nil
}

// PersistErrors counts failed saves. A failed save keeps running in memory.
func (c *Center) PersistErrors() int { c.mu.Lock(); defer c.mu.Unlock(); return c.persistErrs }

func (c *Center) persistLocked(ctx context.Context) {
	if c.store == nil {
		return
	}
	snap := Snapshot{Seen: make(map[string]time.Time, len(c.seen)), Groups: make(map[string]GroupState, len(c.groups))}
	for k, t := range c.seen {
		snap.Seen[k] = t
	}
	for name, g := range c.groups {
		snap.Groups[name] = GroupState{LastSent: g.lastSent, LastSeverity: g.lastSeverity, Pending: append([]Alert(nil), g.pending...),
			Duplicates: g.duplicates, FirstAt: g.firstAt, LastAt: g.lastAt}
	}
	if err := c.store.Save(ctx, snap); err != nil {
		c.persistErrs++
	}
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
	defer c.persistLocked(ctx)
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
	defer c.persistLocked(ctx)
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
	n.Tenant, n.Source, n.Kind, n.Severity, n.Summary = top.Tenant, top.Source, top.Kind, top.Severity.String(), top.Summary
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
