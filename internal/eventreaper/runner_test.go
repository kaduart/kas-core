package eventreaper

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory Store with the same conditional semantics as the
// MongoDB adapter, used to test the runner without a database.
type memStore struct {
	mu        sync.Mutex
	events    map[string]*Candidate
	writes    int
	failApply map[string]error
	failFind  error
	onApply   func(n int)
	msgs      map[string]*string // optional observed error message per event (nil entry: default)
}

func newMem(cs ...Candidate) *memStore {
	m := &memStore{events: map[string]*Candidate{}, failApply: map[string]error{}}
	for i := range cs {
		c := cs[i]
		m.events[c.EventID] = &c
	}
	return m
}

func (m *memStore) Candidates(_ context.Context, _ Protocol, _ time.Time, cutoff time.Time, limit int) ([]Candidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failFind != nil {
		return nil, m.failFind
	}
	var out []Candidate
	for _, c := range m.events {
		if c.Status == StatusProcessing && c.CreatedAt.Before(cutoff) {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].EventID < out[j].EventID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) Apply(_ context.Context, t Transition) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failApply[t.Candidate.EventID]; err != nil {
		return false, err
	}
	e, ok := m.events[t.Candidate.EventID]
	if !ok || e.Status != StatusProcessing || !e.CreatedAt.Before(t.Cutoff) || e.Attempts != t.Candidate.Attempts {
		return false, nil
	}
	e.Status = t.To
	m.writes++
	if m.onApply != nil {
		m.onApply(m.writes)
	}
	return true, nil
}

func (m *memStore) Observe(_ context.Context, ids []string) ([]Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Observation
	for _, id := range ids {
		if e, ok := m.events[id]; ok {
			msg := ""
			if e.Status != StatusProcessing {
				msg = LegacyConfig().RecoveryMessage()
			}
			if p, ok := m.msgs[id]; ok {
				msg = *p
			}
			out = append(out, Observation{EventID: id, Found: true, Status: e.Status, ErrorMessage: msg})
		}
	}
	return out, nil
}

type okLease struct{ ok bool }

func (l okLease) Acquire(context.Context) (bool, error) { return l.ok, nil }
func (l okLease) Release(context.Context) error         { return nil }

func fixed() time.Time { return t0 }

func old(id string, attempts float64) Candidate {
	return Candidate{EventID: id, Status: StatusProcessing, CreatedAt: t0.Add(-time.Hour), Attempts: NumberAttempts(attempts)}
}

func TestShadowNeverWrites(t *testing.T) {
	m := newMem(old("a", 0), old("b", 3))
	r := &Runner{Config: LegacyConfig(), Mode: ModeShadow, Store: m, Now: fixed}
	rep := r.RunCycle(context.Background())
	if m.writes != 0 {
		t.Fatalf("shadow wrote %d times", m.writes)
	}
	if rep.Total != 2 || rep.Reset != 1 || rep.Failed != 1 {
		t.Fatalf("unexpected report %+v", rep)
	}
	for _, o := range rep.Outcomes {
		if o.Result != "predicted" {
			t.Fatalf("shadow outcome %+v", o)
		}
	}
}

func TestShadowVerificationComparesPredictionsWithTheNextCycle(t *testing.T) {
	m := newMem(old("agree", 0), old("diverge", 0), old("stuck", 0))
	r := &Runner{Config: LegacyConfig(), Mode: ModeShadow, Store: m, Now: fixed}
	r.RunCycle(context.Background())
	// Between cycles, the "Node reaper" acts: one as predicted, one differently.
	m.events["agree"].Status = StatusPending
	m.events["diverge"].Status = StatusFailed
	rep := r.RunCycle(context.Background())
	got := map[string]string{}
	for _, c := range rep.Checks {
		got[c.EventID] = c.Verdict
	}
	want := map[string]string{"agree": "agree", "diverge": "diverge", "stuck": "still_processing"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestExecutorAppliesAndContinuesAfterAPerEventError(t *testing.T) {
	m := newMem(old("a", 0), old("b", 3), old("c", 1))
	m.failApply["b"] = errors.New("write conflict")
	r := &Runner{Config: LegacyConfig(), Mode: ModeExecutor, Store: m, Lease: okLease{true}, Now: fixed}
	rep := r.RunCycle(context.Background())
	if rep.Reset != 2 || rep.Failed != 0 || rep.Errors != 1 {
		t.Fatalf("unexpected report %+v", rep)
	}
	if m.events["a"].Status != StatusPending || m.events["c"].Status != StatusPending || m.events["b"].Status != StatusProcessing {
		t.Fatalf("unexpected states a=%s b=%s c=%s", m.events["a"].Status, m.events["b"].Status, m.events["c"].Status)
	}
}

func TestExecutorWithoutLeadershipDoesNothing(t *testing.T) {
	m := newMem(old("a", 0))
	r := &Runner{Config: LegacyConfig(), Mode: ModeExecutor, Store: m, Lease: okLease{false}, Now: fixed}
	rep := r.RunCycle(context.Background())
	if rep.Skipped != "not_leader" || m.writes != 0 {
		t.Fatalf("report %+v writes %d", rep, m.writes)
	}
	r.Lease = nil
	if rep := r.RunCycle(context.Background()); rep.Err == "" || m.writes != 0 {
		t.Fatalf("executor without lease must refuse, got %+v", rep)
	}
}

type leaseLostStore struct{ *memStore }

func (s leaseLostStore) Apply(context.Context, Transition) (bool, error) { return false, ErrLeaseLost }

func TestExecutorStopsTheCycleWhenTheLeaseIsLost(t *testing.T) {
	m := newMem(old("a", 0), old("b", 0))
	r := &Runner{Config: LegacyConfig(), Mode: ModeExecutor, Store: leaseLostStore{m}, Lease: okLease{true}, Now: fixed}
	rep := r.RunCycle(context.Background())
	if rep.Skipped != "lease_lost" || len(rep.Outcomes) != 1 {
		t.Fatalf("report %+v", rep)
	}
}

func TestBatchLimitAndOrderFollowTheLegacyQuery(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 250; i++ {
		c := old(string(rune('A'+i%26))+time.Duration(i).String(), 0)
		c.CreatedAt = t0.Add(-time.Hour - time.Duration(i)*time.Second)
		cs = append(cs, c)
	}
	m := newMem(cs...)
	r := &Runner{Config: LegacyConfig(), Mode: ModeExecutor, Store: m, Lease: okLease{true}, Now: fixed}
	rep := r.RunCycle(context.Background())
	if rep.Total != 100 || rep.Reset != 100 {
		t.Fatalf("first cycle %+v", rep)
	}
	// The oldest 100 go first.
	for _, c := range cs[150:] {
		if m.events[c.EventID].Status != StatusPending {
			t.Fatalf("oldest event %s not handled first", c.EventID)
		}
	}
	r.RunCycle(context.Background())
	if rep := r.RunCycle(context.Background()); rep.Total != 50 {
		t.Fatalf("third cycle should see the last 50, got %d", rep.Total)
	}
}

func TestQueryFailureIsReportedAndTheNextCycleRecovers(t *testing.T) {
	m := newMem(old("a", 0))
	m.failFind = errors.New("server selection timeout")
	r := &Runner{Config: LegacyConfig(), Mode: ModeExecutor, Store: m, Lease: okLease{true}, Now: fixed}
	if rep := r.RunCycle(context.Background()); rep.Err == "" {
		t.Fatal("expected an error report")
	}
	m.failFind = nil
	if rep := r.RunCycle(context.Background()); rep.Reset != 1 {
		t.Fatalf("recovery cycle %+v", rep)
	}
}

func TestOverlappingCyclesAreSkipped(t *testing.T) {
	m := newMem(old("a", 0))
	block := make(chan struct{})
	entered := make(chan struct{})
	m.onApply = func(int) { close(entered); <-block }
	r := &Runner{Config: LegacyConfig(), Mode: ModeExecutor, Store: m, Lease: okLease{true}, Now: fixed}
	done := make(chan CycleReport)
	go func() { done <- r.RunCycle(context.Background()) }()
	<-entered
	if rep := r.RunCycle(context.Background()); rep.Skipped != "already_running" {
		t.Fatalf("second cycle should be skipped, got %+v", rep)
	}
	close(block)
	<-done
}

func TestLoopRunsAfterWarmupAndStopsOnCancel(t *testing.T) {
	m := newMem()
	r := &Runner{Config: LegacyConfig(), Mode: ModeShadow, Store: m, Now: fixed}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	n := 0
	done := make(chan struct{})
	go func() {
		r.Loop(ctx, time.Millisecond, 5*time.Millisecond, func(CycleReport) {
			mu.Lock()
			n++
			if n == 3 {
				cancel()
			}
			mu.Unlock()
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}
}

func TestParseMode(t *testing.T) {
	if _, err := ParseMode("executor"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMode("write"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestShadowCheckClassifiesWhoWroteTheObservedMessage(t *testing.T) {
	m := newMem(old("reaper", 0), old("worker", 0), old("blank", 0), old("gone", 0))
	r := &Runner{Config: LegacyConfig(), Mode: ModeShadow, Store: m, Now: fixed}
	r.RunCycle(context.Background())
	workerMsg, blank := "timeout calling provider", ""
	m.msgs = map[string]*string{"worker": &workerMsg, "blank": &blank}
	m.events["reaper"].Status = StatusPending
	m.events["worker"].Status = StatusPending
	m.events["blank"].Status = "processed"
	delete(m.events, "gone")
	rep := r.RunCycle(context.Background())
	got := map[string][2]string{}
	for _, c := range rep.Checks {
		got[c.EventID] = [2]string{c.Verdict, c.ObservedReason}
	}
	want := map[string][2]string{
		"reaper": {"agree", "reaper"},
		"worker": {"diverge", "other"},
		"blank":  {"diverge", "none"},
		"gone":   {"gone", ""},
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %v want %v", k, got[k], v)
		}
	}
	raw, _ := json.Marshal(rep.Checks)
	if strings.Contains(string(raw), workerMsg) {
		t.Fatal("the report must not copy the observed message text")
	}
}

func TestLoopCadenceIsFixedLikeSetInterval(t *testing.T) {
	prev := t0
	if got := nextTick(prev, 5*time.Minute, t0.Add(40*time.Millisecond)); !got.Equal(t0.Add(5 * time.Minute)) {
		t.Fatalf("a short cycle must not delay the next start: %v", got.Sub(t0))
	}
	// A cycle that took 12 minutes skips the starts at +5 and +10 and keeps the grid.
	if got := nextTick(prev, 5*time.Minute, t0.Add(12*time.Minute)); !got.Equal(t0.Add(15 * time.Minute)) {
		t.Fatalf("overrun: next at %v", got.Sub(t0))
	}
	if got := nextTick(prev, 5*time.Minute, t0.Add(5*time.Minute)); !got.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("exactly on the tick: next at %v", got.Sub(t0))
	}
}
