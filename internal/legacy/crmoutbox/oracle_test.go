package crmoutbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"kas-core-pilot/internal/mongotest"
	"kas-core-pilot/internal/outboxrecovery"
)

// dispatcher drives the unmodified CRM dispatcher through legacy-oracle/outbox.
type dispatcher struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	mu  sync.Mutex
}

func startDispatcher(t *testing.T, db string) *dispatcher {
	t.Helper()
	dir := os.Getenv("KAS_TEST_ORACLE_OUTBOX_DIR")
	if dir == "" {
		t.Skip("KAS_TEST_ORACLE_OUTBOX_DIR is not set; skipping the Node outbox oracle")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	cmd := exec.Command("node", filepath.Join(dir, "oracle.js"))
	cmd.Env = append(os.Environ(), "ORACLE_MONGO_URI="+mongotest.URI(t), "ORACLE_DB="+db)
	cmd.Stderr = io.Discard
	in, _ := cmd.StdinPipe()
	outPipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Wait() })
	sc := bufio.NewScanner(outPipe)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	d := &dispatcher{t: t, in: in, out: sc}
	var ready struct{ Ready bool }
	d.read(&ready)
	if !ready.Ready {
		t.Fatal("oracle did not start")
	}
	return d
}

func (d *dispatcher) read(v any) {
	if !d.out.Scan() {
		d.t.Fatalf("oracle closed its output: %v", d.out.Err())
	}
	if err := json.Unmarshal(d.out.Bytes(), v); err != nil {
		d.t.Fatalf("oracle output %q: %v", d.out.Text(), err)
	}
}

type dispatchResult struct {
	Processed, Published, Failed int
	WaitedForConnection          int
	Error                        string
}

func (d *dispatcher) dispatch(at time.Time) dispatchResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintf(d.in, `{"cmd":"dispatch","nowMs":%d,"batch":250}`+"\n", at.UnixMilli())
	var r dispatchResult
	d.read(&r)
	if r.Error != "" {
		d.t.Fatalf("dispatcher error: %s", r.Error)
	}
	return r
}

type jobsResult struct {
	Stats struct{ Adds, DuplicatesSuppressed, Thrown int }
	Jobs  [][3]string
}

func (d *dispatcher) jobs() jobsResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintln(d.in, `{"cmd":"jobs"}`)
	var r jobsResult
	d.read(&r)
	return r
}

var queuesOf = map[string][]string{
	"FICTICIO_UMA_FILA":   {"fila-a"},
	"FICTICIO_DUAS_FILAS": {"fila-a", "fila-b"},
	"FICTICIO_SEM_FILA":   {},
}

type planned struct {
	eventID, eventType string
	fail               []bool
}

// oracleCorpus: pending entries with a failure plan in the fictitious payload.
func oracleCorpus(n int, seed int64) []planned {
	rng := rand.New(rand.NewSource(seed))
	types := []string{"FICTICIO_UMA_FILA", "FICTICIO_UMA_FILA", "FICTICIO_DUAS_FILAS", "FICTICIO_DUAS_FILAS", "FICTICIO_SEM_FILA", "TIPO_DESCONHECIDO"}
	out := make([]planned, n)
	for i := range out {
		p := planned{eventID: fmt.Sprintf("evt-%06d", i), eventType: types[rng.Intn(len(types))]}
		// 0 to 12 plan steps, each failing with probability 0.45: covers clean
		// publishes, partial multi-queue publishes and plans that never succeed
		// within five attempts.
		for k := rng.Intn(13); k > 0; k-- {
			p.fail = append(p.fail, rng.Float64() < 0.45)
		}
		out[i] = p
	}
	return out
}

func (p planned) doc(i int) bson.D {
	fail := bson.A{}
	for _, f := range p.fail {
		fail = append(fail, f)
	}
	return bson.D{
		{Key: "_id", Value: bson.NewObjectIDFromTimestamp(now.Add(time.Duration(i) * time.Millisecond))},
		{Key: "eventId", Value: p.eventID},
		{Key: "correlationId", Value: "corr-" + p.eventID},
		{Key: "eventType", Value: p.eventType},
		{Key: "payload", Value: bson.D{{Key: "nome", Value: payloadMarker}, {Key: "fail", Value: fail}}},
		{Key: "aggregateType", Value: "ficticio"},
		{Key: "aggregateId", Value: "agg-" + p.eventID},
		{Key: "status", Value: "pending"},
		{Key: "attempts", Value: int32(0)},
		{Key: "scheduledAt", Value: now.Add(-time.Hour)},
		{Key: "createdAt", Value: now.Add(-time.Hour)},
	}
}

type outcome struct {
	status    string // published | failed
	attempts  int
	exhausted string // reason, "" if not exhausted
	jobs      []string
	dups      int
}

// simulate predicts the final state from the plan alone, independently of
// both implementations. withRecovery=false is the CRM today.
func simulate(p planned, withRecovery bool, maxAttempts int) outcome {
	qs, known := queuesOf[p.eventType]
	if !known {
		o := outcome{status: "failed", attempts: 1}
		if withRecovery {
			o.exhausted = outboxrecovery.ReasonPermanentError
		}
		return o
	}
	var o outcome
	have := map[string]bool{}
	idx := 0
	for {
		ok := true
		for _, q := range qs {
			step := idx < len(p.fail) && p.fail[idx]
			idx++
			if step {
				ok = false
				break
			}
			id := p.eventID + "__" + q
			if have[id] {
				o.dups++
			} else {
				have[id] = true
				o.jobs = append(o.jobs, id)
			}
		}
		if ok {
			o.status = "published"
			return o
		}
		o.attempts++
		o.status = "failed"
		if !withRecovery {
			return o
		}
		if o.attempts >= maxAttempts {
			o.exhausted = outboxrecovery.ReasonAttemptsExhausted
			return o
		}
	}
}

func TestOracleCRMDispatcherWithAndWithoutRecovery(t *testing.T) {
	n := 2000
	if v := os.Getenv("KAS_TEST_ORACLE_EVENTS"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	corpus := oracleCorpus(n, 20261118)
	for _, tc := range []struct {
		name       string
		recovery   bool
		concurrent bool
	}{
		{"CRM today, no recovery", false, false},
		{"CRM dispatcher + KAS recovery in rounds, two recoverers", true, false},
		{"CRM dispatcher and KAS recovery running at the same time", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) { runOracle(t, corpus, tc.recovery, tc.concurrent) })
	}
}

func runOracle(t *testing.T, corpus []planned, recovery, concurrent bool) {
	e := setup(t)
	d := startDispatcher(t, e.db)
	docs := make([]bson.D, len(corpus))
	for i, p := range corpus {
		docs[i] = p.doc(i)
	}
	insert(t, e, docs...)

	cfg := outboxrecovery.DefaultConfig()
	var clock atomic.Int64
	clock.Store(now.UnixMilli())
	clockNow := func() time.Time { return time.UnixMilli(clock.Load()).UTC() }
	newRunner := func() *outboxrecovery.Runner {
		return &outboxrecovery.Runner{Config: cfg, Mode: outboxrecovery.ModeExecutor, Store: New(e.client, e.db, "", cfg), Now: clockNow, MaxPages: 1000}
	}
	var alerts atomic.Int64
	ctx := context.Background()
	rounds := 0

	if !concurrent {
		for idle := 0; idle < 2 && rounds < 500; rounds++ {
			r := d.dispatch(clockNow())
			acted := r.Processed
			if recovery {
				var wg sync.WaitGroup
				var mu sync.Mutex
				for w := 0; w < 2; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						run := newRunner()
						run.Alert = func(context.Context, outboxrecovery.Alert) error { alerts.Add(1); return nil }
						c, err := run.RunCycle(ctx)
						if err != nil {
							t.Error(err)
						}
						mu.Lock()
						acted += c.Recovered + c.Exhausted
						mu.Unlock()
					}()
				}
				wg.Wait()
			}
			if os.Getenv("DBG") != "" {
				t.Logf("round %d processed %d acted %d waited %d", rounds, r.Processed, acted, r.WaitedForConnection)
			}
			clock.Add((20 * time.Second).Milliseconds()) // longer than the CRM's largest backoff (2^4 s)
			if acted == 0 {
				idle++
			} else {
				idle = 0
			}
		}
	} else {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for w := 0; w < 2; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				run := newRunner()
				run.Alert = func(context.Context, outboxrecovery.Alert) error { alerts.Add(1); return nil }
				for {
					select {
					case <-stop:
						return
					default:
					}
					if _, err := run.RunCycle(ctx); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		for idle := 0; idle < 3 && rounds < 2000; rounds++ {
			r := d.dispatch(clockNow())
			clock.Add((5 * time.Second).Milliseconds())
			if r.Processed == 0 {
				idle++
				time.Sleep(50 * time.Millisecond) // let the recoverers catch up
			} else {
				idle = 0
			}
		}
		close(stop)
		wg.Wait()
		// drain: anything the recoverers released after the last dispatch
		for i := 0; i < 50; i++ {
			clock.Add((20 * time.Second).Milliseconds())
			c, err := newRunner().RunCycle(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if d.dispatch(clockNow()).Processed == 0 && c.Recovered == 0 && c.Exhausted == 0 {
				break
			}
		}
	}

	// Compare every entry with the independent prediction.
	got := map[string]bson.M{}
	for _, doc := range all(t, e) {
		got[doc["eventId"].(string)] = doc
	}
	jobs := d.jobs()
	jobSet := map[string]bool{}
	for _, j := range jobs.Jobs {
		if jobSet[j[0]] {
			t.Fatalf("job %s present twice", j[0])
		}
		jobSet[j[0]] = true
	}
	var wantJobs, wantDups, published, exhausted, stuckFailed int
	for _, p := range corpus {
		want := simulate(p, recovery, cfg.MaxAttempts)
		doc := got[p.eventID]
		attempts := 0
		switch a := doc["attempts"].(type) {
		case int32:
			attempts = int(a)
		case int64:
			attempts = int(a)
		case float64:
			attempts = int(a)
		}
		reason := ""
		if kas, ok := doc["kasRecovery"].(bson.M); ok {
			if r, ok := kas["reason"].(string); ok {
				reason = r
			}
		}
		if doc["status"] != want.status || attempts != want.attempts || reason != want.exhausted {
			t.Fatalf("%s (%s, plan %v): got status=%v attempts=%d exhausted=%q; want %+v\ndoc %v\nclock %v", p.eventID, p.eventType, p.fail, doc["status"], attempts, reason, want, doc, clockNow())
		}
		for _, j := range want.jobs {
			if !jobSet[j] {
				t.Fatalf("%s: job %s missing", p.eventID, j)
			}
		}
		wantJobs += len(want.jobs)
		wantDups += want.dups
		switch {
		case want.status == "published":
			published++
		case want.exhausted != "":
			exhausted++
		default:
			stuckFailed++
		}
		if doc["payload"].(bson.M)["nome"] != payloadMarker {
			t.Fatalf("%s: payload changed", p.eventID)
		}
	}
	if len(jobSet) != wantJobs || jobs.Stats.DuplicatesSuppressed != wantDups {
		t.Fatalf("jobs %d (want %d), duplicates suppressed by jobId %d (want %d)", len(jobSet), wantJobs, jobs.Stats.DuplicatesSuppressed, wantDups)
	}
	if recovery && int(alerts.Load()) != exhausted {
		t.Fatalf("%d alerts for %d exhausted entries", alerts.Load(), exhausted)
	}
	if recovery && stuckFailed != 0 {
		t.Fatalf("%d entries left failed without an annotation", stuckFailed)
	}
	pend, _ := e.coll.CountDocuments(ctx, bson.D{{Key: "status", Value: "pending"}})
	if pend != 0 {
		t.Fatalf("%d entries left pending", pend)
	}
	t.Logf("%d entries, %d rounds: published %d, exhausted %d (alerted), failed and forgotten %d; jobs %d; re-adds absorbed by jobId %d",
		len(corpus), rounds, published, exhausted, stuckFailed, len(jobSet), jobs.Stats.DuplicatesSuppressed)
}
