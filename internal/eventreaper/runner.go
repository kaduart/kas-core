package eventreaper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Mode selects whether the runner writes.
type Mode string

const (
	// ModeShadow reads, decides and reports; it never writes.
	ModeShadow Mode = "shadow"
	// ModeExecutor applies decisions with an atomic conditional update and
	// only while it holds the executor lease.
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

// Transition is an atomic compare-and-set request: move EventID from
// "processing" to To only if the document still matches what was observed.
type Transition struct {
	Candidate Candidate
	To        string
	Message   string
	Cutoff    time.Time
}

// ErrLeaseLost means the executor no longer owns the lease; no write was made.
var ErrLeaseLost = errors.New("executor lease lost")

// Observation is the current state of an event, used by shadow verification.
type Observation struct {
	EventID      string
	Found        bool
	Status       string
	ErrorMessage string
}

// Store is everything the runner needs from the legacy database. The MongoDB
// implementation lives in internal/legacy/crmeventstore.
type Store interface {
	// Candidates returns up to limit events with status "processing" and
	// createdAt < cutoff, oldest first, without payload.
	Candidates(ctx context.Context, cutoff time.Time, limit int) ([]Candidate, error)
	// Apply performs one transition atomically. applied=false with a nil
	// error means the document no longer matched (someone else changed it).
	Apply(ctx context.Context, t Transition) (applied bool, err error)
	// Observe reads the current status of the given events.
	Observe(ctx context.Context, eventIDs []string) ([]Observation, error)
}

// Lease grants exclusive executor ownership with a fencing token.
type Lease interface {
	// Acquire takes or renews the lease. It returns false when another holder
	// owns a live lease.
	Acquire(ctx context.Context) (bool, error)
	// Release gives the lease up so another executor can take it at once.
	Release(ctx context.Context) error
}

// Outcome of one event in a cycle.
type Outcome struct {
	EventID   string `json:"event_id"`
	EventType string `json:"event_type,omitempty"`
	From      string `json:"from"`
	To        string `json:"to"`
	Attempts  string `json:"attempts"`
	// Result: predicted (shadow), applied, lost_race, lease_lost, error.
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// ShadowCheck compares a previous shadow prediction with what the database
// shows now (normally after the Node reaper had its turn).
type ShadowCheck struct {
	EventID   string `json:"event_id"`
	Predicted string `json:"predicted"`
	Observed  string `json:"observed"`
	// Verdict: agree, still_processing, diverge, gone.
	Verdict string `json:"verdict"`
}

// CycleReport summarises one run. It contains identifiers and states only.
type CycleReport struct {
	Mode      Mode          `json:"mode"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
	Cutoff    time.Time     `json:"cutoff"`
	Total     int           `json:"total"`
	Reset     int           `json:"reset"`
	Failed    int           `json:"failed"`
	LostRace  int           `json:"lost_race"`
	Errors    int           `json:"errors"`
	Skipped   string        `json:"skipped,omitempty"` // already_running, not_leader, lease_error
	Outcomes  []Outcome     `json:"outcomes,omitempty"`
	Checks    []ShadowCheck `json:"shadow_checks,omitempty"`
	Err       string        `json:"error,omitempty"`
}

// Runner executes the policy against a Store.
type Runner struct {
	Config Config
	Mode   Mode
	Store  Store
	Lease  Lease // required in executor mode
	Now    func() time.Time
	Logger *slog.Logger
	Stats  *Stats
	// CycleTimeout bounds one cycle in Loop, so a database that stops
	// answering (paused, partitioned) cannot hang the reaper. Zero: no bound.
	CycleTimeout time.Duration

	mu      sync.Mutex
	running bool
	pending map[string]string // shadow predictions awaiting verification
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// RunCycle runs one reaper pass. Like the legacy runOnce, a pass that starts
// while another is still running is skipped, and errors are reported, not
// returned, so the loop keeps its schedule.
func (r *Runner) RunCycle(ctx context.Context) CycleReport {
	started := r.now()
	rep := CycleReport{Mode: r.Mode, StartedAt: started, Cutoff: r.Config.Cutoff(started)}
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		rep.Skipped = "already_running"
		r.finish(&rep, started)
		return rep
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	if r.Mode == ModeExecutor {
		if r.Lease == nil {
			rep.Err = "executor mode requires a lease"
			r.finish(&rep, started)
			return rep
		}
		ok, err := r.Lease.Acquire(ctx)
		if err != nil {
			rep.Skipped, rep.Err = "lease_error", err.Error()
			r.finish(&rep, started)
			return rep
		}
		if !ok {
			rep.Skipped = "not_leader"
			r.finish(&rep, started)
			return rep
		}
	}

	if r.Mode == ModeShadow {
		r.verifyShadow(ctx, &rep)
	}

	cands, err := r.Store.Candidates(ctx, rep.Cutoff, r.Config.BatchSize)
	if err != nil {
		rep.Err = "candidates: " + err.Error()
		r.finish(&rep, started)
		return rep
	}
	rep.Total = len(cands)
	predictions := map[string]string{}
	for _, c := range cands {
		d := Decide(r.Config, started, c)
		if !d.Eligible {
			// The query already filters; this only happens if the store and
			// the policy disagree, which is a bug worth seeing.
			rep.Errors++
			rep.Outcomes = append(rep.Outcomes, Outcome{EventID: c.EventID, From: c.Status, Result: "error", Error: "store returned ineligible event: " + d.Reason})
			continue
		}
		o := Outcome{EventID: c.EventID, EventType: c.EventType, From: d.From, To: d.To, Attempts: describeAttempts(c.Attempts)}
		if r.Mode == ModeShadow {
			o.Result = "predicted"
			predictions[c.EventID] = d.To
			r.count(&rep, d.To)
			rep.Outcomes = append(rep.Outcomes, o)
			continue
		}
		applied, err := r.Store.Apply(ctx, Transition{Candidate: c, To: d.To, Message: r.Config.RecoveryMessage(), Cutoff: rep.Cutoff})
		switch {
		case errors.Is(err, ErrLeaseLost):
			o.Result = "lease_lost"
			rep.Outcomes = append(rep.Outcomes, o)
			rep.Skipped = "lease_lost"
			r.logger().Warn("event_reaper_lease_lost", "event_id", c.EventID)
			r.finish(&rep, started)
			return rep
		case err != nil:
			// Legacy behaviour: log and continue with the next event.
			o.Result, o.Error = "error", err.Error()
			rep.Errors++
		case !applied:
			o.Result = "lost_race"
			rep.LostRace++
		default:
			o.Result = "applied"
			r.count(&rep, d.To)
		}
		rep.Outcomes = append(rep.Outcomes, o)
	}
	if r.Mode == ModeShadow {
		r.mu.Lock()
		r.pending = predictions
		r.mu.Unlock()
	}
	r.finish(&rep, started)
	return rep
}

func (r *Runner) count(rep *CycleReport, to string) {
	if to == StatusPending {
		rep.Reset++
	} else {
		rep.Failed++
	}
}

func (r *Runner) verifyShadow(ctx context.Context, rep *CycleReport) {
	r.mu.Lock()
	prev := r.pending
	r.pending = nil
	r.mu.Unlock()
	if len(prev) == 0 {
		return
	}
	ids := make([]string, 0, len(prev))
	for id := range prev {
		ids = append(ids, id)
	}
	obs, err := r.Store.Observe(ctx, ids)
	if err != nil {
		rep.Err = "shadow verification: " + err.Error()
		return
	}
	seen := map[string]Observation{}
	for _, o := range obs {
		seen[o.EventID] = o
	}
	msg := r.Config.RecoveryMessage()
	for _, id := range ids {
		o := seen[id]
		c := ShadowCheck{EventID: id, Predicted: prev[id], Observed: o.Status}
		switch {
		case !o.Found:
			c.Verdict = "gone"
		case o.Status == StatusProcessing:
			c.Verdict = "still_processing"
		case o.Status == prev[id] && o.ErrorMessage == msg:
			c.Verdict = "agree"
		default:
			c.Verdict = "diverge"
		}
		rep.Checks = append(rep.Checks, c)
	}
}

func (r *Runner) finish(rep *CycleReport, started time.Time) {
	rep.Duration = r.now().Sub(started)
	if r.Stats != nil {
		r.Stats.record(rep)
	}
	attrs := []any{"mode", rep.Mode, "total", rep.Total, "reset", rep.Reset, "failed", rep.Failed,
		"lost_race", rep.LostRace, "errors", rep.Errors, "duration_ms", rep.Duration.Milliseconds()}
	if rep.Skipped != "" {
		attrs = append(attrs, "skipped", rep.Skipped)
	}
	if len(rep.Checks) > 0 {
		v := map[string]int{}
		for _, c := range rep.Checks {
			v[c.Verdict]++
		}
		attrs = append(attrs, "shadow_checks", v)
	}
	if rep.Err != "" {
		r.logger().Error("event_reaper_cycle", append(attrs, "error", rep.Err)...)
		return
	}
	r.logger().Info("event_reaper_cycle", attrs...)
}

// Loop runs a first cycle after warmup and then one every interval until ctx
// ends (legacy: setTimeout 2 min, setInterval 5 min). Each report is passed to
// onReport when it is not nil.
func (r *Runner) Loop(ctx context.Context, warmup, interval time.Duration, onReport func(CycleReport)) {
	timer := time.NewTimer(warmup)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		cctx, cancel := ctx, context.CancelFunc(func() {})
		if r.CycleTimeout > 0 {
			cctx, cancel = context.WithTimeout(ctx, r.CycleTimeout)
		}
		rep := r.RunCycle(cctx)
		cancel()
		if onReport != nil {
			onReport(rep)
		}
		timer.Reset(interval)
	}
}

func describeAttempts(a Attempts) string {
	switch a.Kind {
	case AttemptsMissing:
		return "missing"
	case AttemptsNull:
		return "null"
	case AttemptsNumber:
		return formatJSNumber(a.Number)
	case AttemptsString:
		return "string"
	case AttemptsBool:
		return "bool"
	}
	return "other"
}
