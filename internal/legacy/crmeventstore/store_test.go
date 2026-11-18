package crmeventstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/eventreaper"
	"kas-core-pilot/internal/mongotest"
)

// All data below is fictitious. The sentinel must never reach the KAS.
const payloadSentinel = "PACIENTE-FICTICIO-NAO-DEVE-SER-LIDO"

var now = time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return now }

type env struct {
	client *mongo.Client
	db     string
	coll   *mongo.Collection
}

func setup(t *testing.T, opts ...*options.ClientOptions) env {
	t.Helper()
	c := mongotest.Client(t, opts...)
	db := mongotest.Database(t, c)
	coll := c.Database(db).Collection(DefaultCollection)
	// The legacy unique index on eventId (models/EventStore.js).
	if _, err := coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{Keys: bson.D{{Key: "eventId", Value: 1}}, Options: options.Index().SetUnique(true)}); err != nil {
		t.Fatal(err)
	}
	return env{client: c, db: db, coll: coll}
}

func legacyDoc(id, status string, created time.Time, attempts any) bson.D {
	d := bson.D{
		{Key: "eventId", Value: id},
		{Key: "eventType", Value: "APPOINTMENT_CREATED"},
		{Key: "eventVersion", Value: int32(1)},
		{Key: "aggregateType", Value: "appointment"},
		{Key: "aggregateId", Value: "agg-" + id},
		{Key: "sequenceNumber", Value: int32(1)},
		{Key: "payload", Value: bson.D{{Key: "nome", Value: payloadSentinel}}},
		{Key: "metadata", Value: bson.D{{Key: "correlationId", Value: "corr-" + id}, {Key: "source", Value: "teste"}}},
		{Key: "status", Value: status},
		{Key: "createdAt", Value: created},
	}
	if attempts != missing {
		d = append(d, bson.E{Key: "attempts", Value: attempts})
	}
	return d
}

type missingT struct{}

var missing = missingT{}

func insert(t *testing.T, e env, docs ...bson.D) {
	t.Helper()
	in := make([]any, len(docs))
	for i, d := range docs {
		in[i] = d
	}
	if _, err := e.coll.InsertMany(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func getDoc(t *testing.T, e env, id string) bson.M {
	t.Helper()
	var m bson.M
	if err := e.coll.FindOne(context.Background(), bson.D{{Key: "eventId", Value: id}}).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func errMsg(m bson.M) string {
	if d, ok := m["error"].(bson.D); ok {
		for _, f := range d {
			if f.Key == "message" {
				s, _ := f.Value.(string)
				return s
			}
		}
	}
	return ""
}

func TestCandidatesUseTheLegacySelection(t *testing.T) {
	e := setup(t)
	cut := now.Add(-15 * time.Minute)
	insert(t, e,
		legacyDoc("old-1", "processing", now.Add(-20*time.Minute), int32(0)),
		legacyDoc("old-2", "processing", now.Add(-60*time.Minute), int32(1)),
		legacyDoc("boundary", "processing", cut, int32(0)),
		legacyDoc("young", "processing", now.Add(-time.Minute), int32(0)),
		legacyDoc("pending", "pending", now.Add(-time.Hour), int32(0)),
		legacyDoc("processed", "processed", now.Add(-time.Hour), int32(0)),
		legacyDoc("failed", "failed", now.Add(-time.Hour), int32(0)),
		legacyDoc("dlq", "dead_letter", now.Add(-time.Hour), int32(0)),
	)
	// createdAt missing or stored as a string never matches {$lt: Date}.
	insert(t, e, bson.D{{Key: "eventId", Value: "no-created"}, {Key: "status", Value: "processing"}},
		bson.D{{Key: "eventId", Value: "string-created"}, {Key: "status", Value: "processing"}, {Key: "createdAt", Value: "2020-01-01"}})
	s := New(e.client, e.db, "", nil)
	got, err := s.Candidates(context.Background(), cut, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.EventID)
	}
	if strings.Join(ids, ",") != "old-2,old-1" {
		t.Fatalf("got %v, want [old-2 old-1] (oldest first, boundary excluded)", ids)
	}
	if lim, _ := s.Candidates(context.Background(), cut, 1); len(lim) != 1 || lim[0].EventID != "old-2" {
		t.Fatalf("limit not honoured: %+v", lim)
	}
}

// The KAS must never read payload, metadata or aggregate data. This test looks
// at the commands actually sent to MongoDB, not only at the decoded struct.
func TestTheKASNeverRequestsPayloadOrMetadata(t *testing.T) {
	var mu sync.Mutex
	var cmds []bson.Raw
	mon := &event.CommandMonitor{Started: func(_ context.Context, ev *event.CommandStartedEvent) {
		if ev.CommandName == "find" {
			mu.Lock()
			cmds = append(cmds, append(bson.Raw(nil), ev.Command...))
			mu.Unlock()
		}
	}}
	e := setup(t, options.Client().SetMonitor(mon))
	insert(t, e, legacyDoc("a", "processing", now.Add(-time.Hour), int32(0)), legacyDoc("b", "processing", now.Add(-time.Hour), int32(5)))
	s := New(e.client, e.db, "", nil)
	r := &eventreaper.Runner{Config: eventreaper.LegacyConfig(), Mode: eventreaper.ModeShadow, Store: s, Now: fixedNow}
	r.RunCycle(context.Background())
	r.RunCycle(context.Background()) // second cycle exercises Observe
	if _, err := s.Apply(context.Background(), eventreaper.Transition{Candidate: eventreaper.Candidate{EventID: "a", Attempts: eventreaper.NumberAttempts(0)}, To: "pending", Message: "m", Cutoff: now}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cmds) < 3 {
		t.Fatalf("expected find commands, got %d", len(cmds))
	}
	allowed := map[string]bool{"_id": true, "eventId": true, "eventType": true, "status": true, "createdAt": true, "attempts": true, "processingToken": true, "error.message": true}
	for _, c := range cmds {
		proj, err := c.LookupErr("projection")
		if err != nil {
			t.Fatalf("find without projection: %s", c)
		}
		elems, _ := proj.Document().Elements()
		for _, el := range elems {
			if !allowed[el.Key()] {
				t.Fatalf("projection requests %q: %s", el.Key(), c)
			}
		}
		if strings.Contains(c.String(), payloadSentinel) {
			t.Fatal("payload sentinel appeared in a command")
		}
	}
}

func TestApplyWritesExactlyWhatTheLegacyReaperStores(t *testing.T) {
	e := setup(t)
	d := legacyDoc("x", "processing", now.Add(-time.Hour), int32(2))
	d = append(d, bson.E{Key: "error", Value: bson.D{{Key: "message", Value: "old"}, {Key: "stack", Value: "trace"}, {Key: "code", Value: "E1"}}})
	insert(t, e, d)
	before := getDoc(t, e, "x")
	s := New(e.client, e.db, "", nil)
	cands, _ := s.Candidates(context.Background(), now.Add(-15*time.Minute), 10)
	ok, err := s.Apply(context.Background(), eventreaper.Transition{Candidate: cands[0], To: "pending", Message: eventreaper.LegacyConfig().RecoveryMessage(), Cutoff: now.Add(-15 * time.Minute)})
	if err != nil || !ok {
		t.Fatalf("apply ok=%v err=%v", ok, err)
	}
	after := getDoc(t, e, "x")
	if after["status"] != "pending" || errMsg(after) != "Auto-recovered from stuck processing after 15min" {
		t.Fatalf("unexpected %v", after)
	}
	// error is replaced as a whole: stack and code are gone, nothing else added.
	if ed := after["error"].(bson.D); len(ed) != 1 {
		t.Fatalf("error should hold only message, got %v", ed)
	}
	for k, v := range before {
		if k == "status" || k == "error" {
			continue
		}
		if fmt.Sprint(after[k]) != fmt.Sprint(v) {
			t.Fatalf("field %s changed: %v -> %v", k, v, after[k])
		}
	}
	if len(after) != len(before) {
		t.Fatalf("fields added or removed: before %d after %d", len(before), len(after))
	}
}

// DV-1: the legacy update filters only by eventId, so a completion that lands
// between its read and its write is overwritten. The port re-checks the state.
func TestCompletionBetweenReadAndWriteIsNotOverwritten(t *testing.T) {
	e := setup(t)
	insert(t, e, legacyDoc("race", "processing", now.Add(-time.Hour), int32(0)))
	s := New(e.client, e.db, "", nil)
	cands, _ := s.Candidates(context.Background(), now.Add(-15*time.Minute), 10)
	// The worker finishes (legacy markProcessed: save() by _id).
	if _, err := e.coll.UpdateOne(context.Background(), bson.D{{Key: "eventId", Value: "race"}}, bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "processed"}, {Key: "processedBy", Value: "worker-ficticio"}}}}); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Apply(context.Background(), eventreaper.Transition{Candidate: cands[0], To: "pending", Message: "m", Cutoff: now.Add(-15 * time.Minute)})
	if err != nil || ok {
		t.Fatalf("expected lost race, ok=%v err=%v", ok, err)
	}
	if got := getDoc(t, e, "race")["status"]; got != "processed" {
		t.Fatalf("completion overwritten: %v", got)
	}

	// For the record: the legacy write (updateOne({eventId}, $set)) does overwrite.
	legacyWrite := bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "pending"}, {Key: "error", Value: bson.D{{Key: "message", Value: "m"}}}}}}
	if _, err := e.coll.UpdateOne(context.Background(), bson.D{{Key: "eventId", Value: "race"}}, legacyWrite); err != nil {
		t.Fatal(err)
	}
	if got := getDoc(t, e, "race")["status"]; got != "pending" {
		t.Fatalf("legacy emulation should overwrite, got %v", got)
	}
}

func TestAttemptsChangedAfterReadMakesTheWriteMiss(t *testing.T) {
	e := setup(t)
	insert(t, e, legacyDoc("att", "processing", now.Add(-time.Hour), int32(2)))
	s := New(e.client, e.db, "", nil)
	cands, _ := s.Candidates(context.Background(), now.Add(-15*time.Minute), 10)
	if _, err := e.coll.UpdateOne(context.Background(), bson.D{{Key: "eventId", Value: "att"}}, bson.D{{Key: "$inc", Value: bson.D{{Key: "attempts", Value: 1}}}}); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Apply(context.Background(), eventreaper.Transition{Candidate: cands[0], To: "pending", Message: "m", Cutoff: now.Add(-15 * time.Minute)})
	if err != nil || ok {
		t.Fatalf("decision taken on attempts=2 must not apply to attempts=3; ok=%v err=%v", ok, err)
	}
}

func TestAttemptsGuardDistinguishesNullAndMissing(t *testing.T) {
	e := setup(t)
	insert(t, e, legacyDoc("null", "processing", now.Add(-time.Hour), nil), legacyDoc("miss", "processing", now.Add(-time.Hour), missing))
	s := New(e.client, e.db, "", nil)
	cands, _ := s.Candidates(context.Background(), now.Add(-15*time.Minute), 10)
	byID := map[string]eventreaper.Candidate{}
	for _, c := range cands {
		byID[c.EventID] = c
	}
	if byID["null"].Attempts.Kind != eventreaper.AttemptsNull || byID["miss"].Attempts.Kind != eventreaper.AttemptsMissing {
		t.Fatalf("decoded kinds: %+v", byID)
	}
	// Using the "null" observation for the "missing" document must miss.
	c := byID["null"]
	c.EventID = "miss"
	if ok, err := s.Apply(context.Background(), eventreaper.Transition{Candidate: c, To: "pending", Message: "m", Cutoff: now}); err != nil || ok {
		t.Fatalf("null guard matched a missing field: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Apply(context.Background(), eventreaper.Transition{Candidate: byID["miss"], To: "failed", Message: "m", Cutoff: now}); err != nil || !ok {
		t.Fatalf("missing guard: ok=%v err=%v", ok, err)
	}
}

func seedMany(t *testing.T, e env, n int) {
	t.Helper()
	docs := make([]bson.D, n)
	for i := 0; i < n; i++ {
		docs[i] = legacyDoc(fmt.Sprintf("evt-%05d", i), "processing", now.Add(-time.Hour-time.Duration(i)*time.Millisecond), int32(i%5))
	}
	insert(t, e, docs...)
}

func runUntilEmpty(ctx context.Context, r *eventreaper.Runner, out chan<- eventreaper.Outcome) {
	for i := 0; i < 1000 && ctx.Err() == nil; i++ {
		rep := r.RunCycle(ctx)
		for _, o := range rep.Outcomes {
			out <- o
		}
		if rep.Skipped == "" && rep.Err == "" && rep.Total == 0 {
			return
		}
		if rep.Skipped != "" {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// Two executors without a lease: only the conditional update protects them.
// Every event must be moved exactly once.
func TestTwoUnfencedExecutorsNeverMoveAnEventTwice(t *testing.T) {
	e := setup(t)
	const n = 1000
	seedMany(t, e, n)
	out := make(chan eventreaper.Outcome, 4*n)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &eventreaper.Runner{Config: eventreaper.LegacyConfig(), Mode: eventreaper.ModeExecutor, Store: New(e.client, e.db, "", nil), Lease: alwaysLeader{}, Now: fixedNow}
			runUntilEmpty(context.Background(), r, out)
		}()
	}
	wg.Wait()
	close(out)
	assertExactlyOnce(t, e, out, n)
}

type alwaysLeader struct{}

func (alwaysLeader) Acquire(context.Context) (bool, error) { return true, nil }
func (alwaysLeader) Release(context.Context) error         { return nil }

func assertExactlyOnce(t *testing.T, e env, out <-chan eventreaper.Outcome, n int) {
	t.Helper()
	applied := map[string]int{}
	for o := range out {
		if o.Result == "applied" {
			applied[o.EventID]++
		}
	}
	if len(applied) != n {
		t.Fatalf("applied %d distinct events, want %d", len(applied), n)
	}
	for id, c := range applied {
		if c != 1 {
			t.Fatalf("%s applied %d times", id, c)
		}
	}
	left, _ := e.coll.CountDocuments(context.Background(), bson.D{{Key: "status", Value: "processing"}})
	if left != 0 {
		t.Fatalf("%d events still processing", left)
	}
	failed, _ := e.coll.CountDocuments(context.Background(), bson.D{{Key: "status", Value: "failed"}})
	if failed != int64(n*2/5) { // attempts 3 and 4 out of 0..4
		t.Fatalf("failed=%d want %d", failed, n*2/5)
	}
}

// Two executors with the lease: one leads at a time, and the result is the
// same set of transitions.
func TestTwoLeasedExecutorsShareTheWorkWithoutOverlap(t *testing.T) {
	e := setup(t)
	const n = 600
	seedMany(t, e, n)
	out := make(chan eventreaper.Outcome, 4*n)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := NewLease(e.client, e.db, "", fmt.Sprintf("exec-%d", i), 300*time.Millisecond)
			r := &eventreaper.Runner{Config: eventreaper.LegacyConfig(), Mode: eventreaper.ModeExecutor, Store: New(e.client, e.db, "", l), Lease: l, Now: fixedNow}
			runUntilEmpty(context.Background(), r, out)
		}(i)
	}
	wg.Wait()
	close(out)
	assertExactlyOnce(t, e, out, n)
}

func TestOnlyOneLiveLeaseHolder(t *testing.T) {
	e := setup(t)
	a := NewLease(e.client, e.db, "", "a", time.Minute)
	b := NewLease(e.client, e.db, "", "b", time.Minute)
	ctx := context.Background()
	if ok, err := a.Acquire(ctx); err != nil || !ok {
		t.Fatalf("a: %v %v", ok, err)
	}
	if ok, err := b.Acquire(ctx); err != nil || ok {
		t.Fatalf("b must not acquire a live lease: %v %v", ok, err)
	}
	if ok, _ := a.Acquire(ctx); !ok {
		t.Fatal("a must renew")
	}
	if err := a.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.Acquire(ctx); err != nil || !ok {
		t.Fatalf("b after release: %v %v", ok, err)
	}
}

// A former holder whose lease expired and was taken over cannot write, even
// if it still believes it is the executor (paused process, GC pause, network
// partition). This is the fencing guarantee on the KAS side.
func TestFormerLeaseHolderIsFencedOut(t *testing.T) {
	e := setup(t)
	insert(t, e, legacyDoc("f", "processing", now.Add(-time.Hour), int32(0)))
	ctx := context.Background()
	a := NewLease(e.client, e.db, "", "a", 200*time.Millisecond)
	if ok, _ := a.Acquire(ctx); !ok {
		t.Fatal("a acquire")
	}
	sa := New(e.client, e.db, "", a)
	cands, _ := sa.Candidates(ctx, now.Add(-15*time.Minute), 10)
	time.Sleep(400 * time.Millisecond)
	b := NewLease(e.client, e.db, "", "b", time.Minute)
	if ok, _ := b.Acquire(ctx); !ok {
		t.Fatal("b should take over the expired lease")
	}
	_, err := sa.Apply(ctx, eventreaper.Transition{Candidate: cands[0], To: "pending", Message: "m", Cutoff: now.Add(-15 * time.Minute)})
	if !errors.Is(err, eventreaper.ErrLeaseLost) {
		t.Fatalf("former holder wrote or failed differently: %v", err)
	}
	if got := getDoc(t, e, "f")["status"]; got != "processing" {
		t.Fatalf("fenced write leaked: %v", got)
	}
	// Even if it re-acquires, b holds a live lease.
	if ok, _ := a.Acquire(ctx); ok {
		t.Fatal("a must not regain a live lease held by b")
	}
}

// Restart in the middle of a batch: the process dies after some writes; a new
// process finishes the job and the final state is the same as one clean run.
func TestRestartMidBatchConverges(t *testing.T) {
	e := setup(t)
	const n = 250
	seedMany(t, e, n)
	ctx, cancel := context.WithCancel(context.Background())
	l1 := NewLease(e.client, e.db, "", "first", time.Minute)
	s1 := &cancelAfter{Store: New(e.client, e.db, "", l1), n: 37, cancel: cancel}
	r1 := &eventreaper.Runner{Config: eventreaper.LegacyConfig(), Mode: eventreaper.ModeExecutor, Store: s1, Lease: l1, Now: fixedNow}
	rep := r1.RunCycle(ctx)
	if rep.Errors == 0 {
		t.Fatalf("the first run should have been interrupted: %+v", rep)
	}
	moved, _ := e.coll.CountDocuments(context.Background(), bson.D{{Key: "status", Value: bson.D{{Key: "$ne", Value: "processing"}}}})
	if moved != 37 {
		t.Fatalf("moved before crash = %d, want 37", moved)
	}
	// The crashed process never released the lease: the new one waits for TTL
	// in production; here the lease is released to keep the test fast.
	_ = l1.Release(context.Background())
	l2 := NewLease(e.client, e.db, "", "second", time.Minute)
	r2 := &eventreaper.Runner{Config: eventreaper.LegacyConfig(), Mode: eventreaper.ModeExecutor, Store: New(e.client, e.db, "", l2), Lease: l2, Now: fixedNow}
	out := make(chan eventreaper.Outcome, 4*n)
	runUntilEmpty(context.Background(), r2, out)
	close(out)
	applied := 0
	for o := range out {
		if o.Result == "applied" {
			applied++
		}
	}
	if applied != n-37 {
		t.Fatalf("second run applied %d, want %d", applied, n-37)
	}
	failed, _ := e.coll.CountDocuments(context.Background(), bson.D{{Key: "status", Value: "failed"}})
	if failed != int64(n*2/5) {
		t.Fatalf("final failed=%d want %d", failed, n*2/5)
	}
}

type cancelAfter struct {
	eventreaper.Store
	n      int
	count  int
	cancel func()
}

func (c *cancelAfter) Apply(ctx context.Context, t eventreaper.Transition) (bool, error) {
	if c.count == c.n {
		c.cancel()
	}
	c.count++
	return c.Store.Apply(ctx, t)
}

func TestUnreachableDatabaseIsReportedNotFatal(t *testing.T) {
	c, err := mongo.Connect(options.Client().ApplyURI("mongodb://127.0.0.1:1/?directConnection=true").SetServerSelectionTimeout(300 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect(context.Background())
	l := NewLease(c, "nenhum", "", "x", time.Minute)
	r := &eventreaper.Runner{Config: eventreaper.LegacyConfig(), Mode: eventreaper.ModeExecutor, Store: New(c, "nenhum", "", l), Lease: l, Now: fixedNow}
	rep := r.RunCycle(context.Background())
	if rep.Err == "" || rep.Skipped != "lease_error" {
		t.Fatalf("expected a lease error report, got %+v", rep)
	}
	sh := &eventreaper.Runner{Config: eventreaper.LegacyConfig(), Mode: eventreaper.ModeShadow, Store: New(c, "nenhum", "", nil), Now: fixedNow}
	if rep := sh.RunCycle(context.Background()); rep.Err == "" {
		t.Fatalf("expected a query error report, got %+v", rep)
	}
}

// F-1: what a fenced claim protocol would give, once the CRM adopts it. The
// legacy consumer completes unconditionally and cannot be stopped by the KAS.
func TestFencedClaimProtocolStopsAStaleConsumer(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d := legacyDoc("tok", "processing", now.Add(-time.Hour), int32(0))
	d = append(d, bson.E{Key: "processingToken", Value: "claim-1"})
	insert(t, e, d, legacyDoc("legacy", "processing", now.Add(-time.Hour), int32(0)))
	s := New(e.client, e.db, "", nil)
	cands, _ := s.Candidates(ctx, now.Add(-15*time.Minute), 10)
	for _, c := range cands {
		if ok, err := s.Apply(ctx, eventreaper.Transition{Candidate: c, To: "pending", Message: "m", Cutoff: now.Add(-15 * time.Minute)}); err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", c.EventID, ok, err)
		}
	}
	if _, has := getDoc(t, e, "tok")["processingToken"]; has {
		t.Fatal("reaper must drop the stale claim token")
	}
	// Fenced consumer: completion conditioned on its token misses.
	r, _ := e.coll.UpdateOne(ctx, bson.D{{Key: "eventId", Value: "tok"}, {Key: "status", Value: "processing"}, {Key: "processingToken", Value: "claim-1"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "processed"}}}})
	if r.ModifiedCount != 0 {
		t.Fatal("stale fenced consumer completed an execution it no longer owns")
	}
	// Legacy consumer: markProcessed saves by _id and wins (known gap, F-1).
	r, _ = e.coll.UpdateOne(ctx, bson.D{{Key: "eventId", Value: "legacy"}}, bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "processed"}}}})
	if r.ModifiedCount != 1 {
		t.Fatal("legacy unconditional completion should still succeed (documented gap)")
	}
}
