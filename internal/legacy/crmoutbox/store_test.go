package crmoutbox

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/mongotest"
	"kas-core-pilot/internal/outboxrecovery"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return now }

type env struct {
	client *mongo.Client
	db     string
	coll   *mongo.Collection
}

func setup(t *testing.T, opts ...*options.ClientOptions) env {
	t.Helper()
	opts = append([]*options.ClientOptions{options.Client().SetBSONOptions(&options.BSONOptions{DefaultDocumentM: true})}, opts...)
	c := mongotest.Client(t, opts...)
	db := mongotest.Database(t, c)
	return env{client: c, db: db, coll: c.Database(db).Collection(DefaultCollection)}
}

const payloadMarker = "PACIENTE-FICTICIO-NAO-LER"

// entry builds a fictitious outbox document shaped like OutboxModel.js.
func entry(i int, status string, extra ...bson.E) bson.D {
	d := bson.D{
		{Key: "_id", Value: bson.NewObjectIDFromTimestamp(now.Add(time.Duration(i) * time.Millisecond))},
		{Key: "eventId", Value: fmt.Sprintf("evt-%06d", i)},
		{Key: "correlationId", Value: fmt.Sprintf("corr-%06d", i)},
		{Key: "eventType", Value: "FICTICIO_CRIADO"},
		{Key: "payload", Value: bson.D{{Key: "nome", Value: payloadMarker}}},
		{Key: "aggregateType", Value: "ficticio"},
		{Key: "aggregateId", Value: fmt.Sprintf("agg-%d", i)},
		{Key: "status", Value: status},
		{Key: "createdAt", Value: now.Add(-time.Hour)},
	}
	return append(d, extra...)
}

func insert(t *testing.T, e env, docs ...bson.D) {
	t.Helper()
	if len(docs) == 0 {
		return
	}
	in := make([]any, len(docs))
	for i, d := range docs {
		in[i] = d
	}
	if _, err := e.coll.InsertMany(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func all(t *testing.T, e env) []bson.M {
	t.Helper()
	cur, err := e.coll.Find(context.Background(), bson.D{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		t.Fatal(err)
	}
	var out []bson.M
	if err := cur.All(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func runner(e env, mode outboxrecovery.Mode) *outboxrecovery.Runner {
	cfg := outboxrecovery.DefaultConfig()
	return &outboxrecovery.Runner{Config: cfg, Mode: mode, Store: New(e.client, e.db, "", cfg), Now: fixedNow, MaxPages: 1000, StalePendingAfter: 10 * time.Minute}
}

// corpus: every shape the dispatcher can leave behind, plus malformed values.
func corpus(n int, seed int64) []bson.D {
	rng := rand.New(rand.NewSource(seed))
	attempts := []any{nil, int32(0), int32(1), int32(2), int32(4), int32(5), int32(9), int64(3), 2.0, 2.5, "2", true, missingVal}
	sched := []any{now.Add(-time.Hour), now.Add(-time.Second), now, now.Add(time.Second), now.Add(time.Hour), missingVal, "amanhã", nil}
	errs := []any{"Connection is closed.", "UNKNOWN_EVENT_TYPE: X não está em eventToQueueMap", "ETIMEDOUT", missingVal, int32(7)}
	statuses := []string{"failed", "failed", "failed", "failed", "pending", "published"}
	out := make([]bson.D, n)
	for i := range out {
		var extra []bson.E
		if a := attempts[rng.Intn(len(attempts))]; a != missingVal {
			extra = append(extra, bson.E{Key: "attempts", Value: a})
		}
		if s := sched[rng.Intn(len(sched))]; s != missingVal {
			extra = append(extra, bson.E{Key: "scheduledAt", Value: s})
		}
		if m := errs[rng.Intn(len(errs))]; m != missingVal {
			extra = append(extra, bson.E{Key: "lastError", Value: m})
		}
		switch rng.Intn(8) {
		case 0:
			extra = append(extra, bson.E{Key: "kasRecovery", Value: bson.D{{Key: "count", Value: int64(rng.Intn(7))}}})
		case 1:
			extra = append(extra, bson.E{Key: "kasRecovery", Value: bson.D{{Key: "count", Value: int64(1)}, {Key: "exhaustedAt", Value: now.Add(-time.Minute)}, {Key: "reason", Value: "attempts_exhausted"}}})
		}
		out[i] = entry(i, statuses[rng.Intn(len(statuses))], extra...)
	}
	return out
}

type missing struct{}

var missingVal = missing{}

func TestShadowWritesNothingAndNeverReadsPayload(t *testing.T) {
	var mu sync.Mutex
	var cmds []bson.Raw
	mon := &event.CommandMonitor{Started: func(_ context.Context, ev *event.CommandStartedEvent) {
		mu.Lock()
		cmds = append(cmds, append(bson.Raw(nil), ev.Command...))
		mu.Unlock()
	}}
	e := setup(t, options.Client().SetMonitor(mon))
	insert(t, e, corpus(600, 1)...)
	before := all(t, e)

	mu.Lock()
	cmds = nil
	mu.Unlock()
	c, err := runner(e, outboxrecovery.ModeShadow).RunCycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	seen := cmds
	mu.Unlock()
	if !reflect.DeepEqual(before, all(t, e)) {
		t.Fatal("shadow mode changed the collection")
	}
	if c.Recovered != 0 || c.Exhausted != 0 || len(c.WouldDo["recover"]) == 0 || len(c.WouldDo["exhaust"]) == 0 {
		t.Fatalf("unexpected shadow cycle: %+v", c)
	}
	if strings.Contains(fmt.Sprintf("%+v", c), payloadMarker) {
		t.Fatal("payload leaked into the cycle report")
	}
	finds := 0
	for _, cmd := range seen {
		name := cmd.Index(0).Key()
		switch name {
		case "find":
			finds++
			proj, err := cmd.LookupErr("projection")
			if err != nil {
				t.Fatalf("find without projection: %s", cmd)
			}
			for _, k := range []string{"payload", "correlationId", "aggregateId", "aggregateType", "eventId"} {
				if _, err := proj.Document().LookupErr(k); err == nil {
					t.Fatalf("projection includes %s", k)
				}
			}
			if !reflect.DeepEqual(proj.Document(), mustRaw(t, ScanProjection())) {
				t.Fatalf("find projection differs from ScanProjection: %s", proj)
			}
		case "aggregate", "count":
		case "update", "delete", "insert", "findAndModify":
			t.Fatalf("shadow issued a %s", name)
		}
	}
	if finds == 0 {
		t.Fatal("no find observed")
	}
}

func mustRaw(t *testing.T, d bson.D) bson.Raw {
	b, err := bson.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// expected applies the policy by hand to a document, independently of Scan.
func expectedAction(t *testing.T, d bson.M) string {
	status, _ := d["status"].(string)
	if status != "failed" {
		return "none"
	}
	kas, _ := d["kasRecovery"].(bson.M)
	if kas != nil {
		if _, ok := kas["exhaustedAt"]; ok {
			return "none"
		}
	}
	var att int64
	switch a := d["attempts"].(type) {
	case nil:
		if _, present := d["attempts"]; present {
			return "none" // null: malformed
		}
	case int32:
		att = int64(a)
	case int64:
		att = a
	case float64:
		if a != float64(int64(a)) {
			return "none"
		}
		att = int64(a)
	default:
		return "none"
	}
	if s, ok := d["lastError"].(string); ok && strings.HasPrefix(s, "UNKNOWN_EVENT_TYPE") {
		return "exhaust"
	}
	if att >= 5 {
		return "exhaust"
	}
	if kas != nil {
		if c, ok := kas["count"].(int64); ok && c >= 5 {
			return "exhaust"
		}
	}
	if s, ok := d["scheduledAt"].(bson.DateTime); ok && s.Time().After(now) {
		return "none"
	}
	return "recover"
}

func TestExecutorMatchesAnIndependentReading(t *testing.T) {
	e := setup(t)
	insert(t, e, corpus(3000, 2)...)
	want := map[string]string{}
	var nRec, nExh int
	for _, d := range all(t, e) {
		id := d["_id"].(bson.ObjectID).Hex()
		want[id] = expectedAction(t, d)
		switch want[id] {
		case "recover":
			nRec++
		case "exhaust":
			nExh++
		}
	}
	before := map[string]bson.M{}
	for _, d := range all(t, e) {
		before[d["_id"].(bson.ObjectID).Hex()] = d
	}
	c, err := runner(e, outboxrecovery.ModeExecutor).RunCycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Recovered != nRec || c.Exhausted != nExh || c.LostRaces != 0 {
		t.Fatalf("cycle %+v, want recovered %d exhausted %d", c, nRec, nExh)
	}
	for _, d := range all(t, e) {
		id := d["_id"].(bson.ObjectID).Hex()
		b := before[id]
		if d["payload"].(bson.M)["nome"] != payloadMarker {
			t.Fatalf("%s: payload changed", id)
		}
		switch want[id] {
		case "none":
			if !reflect.DeepEqual(b, d) {
				t.Fatalf("%s changed although no action was expected:\n%v\n%v", id, b, d)
			}
		case "recover":
			kas := d["kasRecovery"].(bson.M)
			if d["status"] != "pending" || !d["scheduledAt"].(bson.DateTime).Time().Equal(now) || !kas["lastRecoveredAt"].(bson.DateTime).Time().Equal(now) {
				t.Fatalf("%s not recovered: %v", id, d)
			}
			prev := int64(0)
			if bk, ok := b["kasRecovery"].(bson.M); ok {
				prev = bk["count"].(int64)
			}
			if kas["count"].(int64) != prev+1 {
				t.Fatalf("%s: count %v, before %d", id, kas["count"], prev)
			}
		case "exhaust":
			kas := d["kasRecovery"].(bson.M)
			if d["status"] != "failed" || kas["exhaustedAt"] == nil || kas["reason"] == "" {
				t.Fatalf("%s not exhausted: %v", id, d)
			}
			if !reflect.DeepEqual(b["attempts"], d["attempts"]) || !reflect.DeepEqual(b["scheduledAt"], d["scheduledAt"]) {
				t.Fatalf("%s: exhaust touched dispatcher fields", id)
			}
		}
	}

	// Repetition: a second cycle has nothing left to do.
	c2, err := runner(e, outboxrecovery.ModeExecutor).RunCycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c2.Recovered != 0 || c2.Exhausted != 0 {
		t.Fatalf("second cycle acted again: %+v", c2)
	}
}

func TestConcurrentRecoverersApplyEachFailureOnce(t *testing.T) {
	e := setup(t)
	const n = 2000
	docs := make([]bson.D, n)
	for i := range docs {
		docs[i] = entry(i, "failed", bson.E{Key: "attempts", Value: int32(i % 7)}, bson.E{Key: "scheduledAt", Value: now.Add(-time.Minute)})
	}
	insert(t, e, docs...)
	var rec, exh, lost atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := runner(e, outboxrecovery.ModeExecutor)
			r.Config.BatchSize = 37
			c, err := r.RunCycle(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			rec.Add(int64(c.Recovered))
			exh.Add(int64(c.Exhausted))
			lost.Add(int64(c.LostRaces))
		}()
	}
	wg.Wait()
	var wantRec, wantExh int64
	for i := 0; i < n; i++ {
		if i%7 >= 5 {
			wantExh++
		} else {
			wantRec++
		}
	}
	if rec.Load() != wantRec || exh.Load() != wantExh {
		t.Fatalf("recovered %d exhausted %d (lost races %d); want %d and %d", rec.Load(), exh.Load(), lost.Load(), wantRec, wantExh)
	}
	for _, d := range all(t, e) {
		kas := d["kasRecovery"].(bson.M)
		if c, ok := kas["count"]; ok && c.(int64) != 1 {
			t.Fatalf("entry recovered %v times", c)
		}
	}
	t.Logf("6 concurrent recoverers: %d recovered, %d exhausted, %d lost races (expected, harmless)", rec.Load(), exh.Load(), lost.Load())
}

// racingStore lets a fake dispatcher change an entry between Scan and the write.
type racingStore struct {
	*Store
	before func(outboxrecovery.Entry)
}

func (r racingStore) Recover(ctx context.Context, e outboxrecovery.Entry, now time.Time) (bool, error) {
	r.before(e)
	return r.Store.Recover(ctx, e, now)
}

func TestLosesTheRaceAgainstTheDispatcher(t *testing.T) {
	e := setup(t)
	insert(t, e,
		entry(1, "failed", bson.E{Key: "attempts", Value: int32(1)}, bson.E{Key: "scheduledAt", Value: now.Add(-time.Minute)}),
		entry(2, "failed", bson.E{Key: "attempts", Value: int32(1)}, bson.E{Key: "scheduledAt", Value: now.Add(-time.Minute)}),
		entry(3, "failed", bson.E{Key: "attempts", Value: int32(1)}, bson.E{Key: "scheduledAt", Value: now.Add(-time.Minute)}),
	)
	ids := all(t, e)
	r := runner(e, outboxrecovery.ModeExecutor)
	base := r.Store.(*Store)
	r.Store = racingStore{Store: base, before: func(en outboxrecovery.Entry) {
		oid, _ := bson.ObjectIDFromHex(en.ID)
		switch oid {
		case ids[0]["_id"]:
			// another dispatcher failed it again in the meantime (as the CRM does:
			// $inc attempts, new scheduledAt)
			_, _ = e.coll.UpdateOne(context.Background(), bson.D{{Key: "_id", Value: oid}}, bson.D{{Key: "$inc", Value: bson.D{{Key: "attempts", Value: 1}}}, {Key: "$set", Value: bson.D{{Key: "scheduledAt", Value: now.Add(4 * time.Second)}}}})
		case ids[1]["_id"]:
			// someone (a human, another recoverer) already put it back to pending
			_, _ = e.coll.UpdateOne(context.Background(), bson.D{{Key: "_id", Value: oid}}, bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "pending"}}}})
		}
	}}
	c, err := r.RunCycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Recovered != 1 || c.LostRaces != 2 {
		t.Fatalf("got %+v, want 1 recovered and 2 lost races", c)
	}
	got := all(t, e)
	if got[0]["status"] != "failed" || got[0]["attempts"] != int32(2) || got[0]["kasRecovery"] != nil {
		t.Fatalf("lost race still wrote: %v", got[0])
	}
	if got[1]["kasRecovery"] != nil {
		t.Fatalf("lost race still wrote: %v", got[1])
	}
}

// crashingStore fails after k successful writes, like a process killed mid-batch.
type crashingStore struct {
	*Store
	left *atomic.Int64
}

var errCrash = errors.New("simulated crash")

func (c crashingStore) Recover(ctx context.Context, e outboxrecovery.Entry, now time.Time) (bool, error) {
	if c.left.Add(-1) < 0 {
		return false, errCrash
	}
	return c.Store.Recover(ctx, e, now)
}

func (c crashingStore) Exhaust(ctx context.Context, e outboxrecovery.Entry, r string, now time.Time) (bool, error) {
	if c.left.Add(-1) < 0 {
		return false, errCrash
	}
	return c.Store.Exhaust(ctx, e, r, now)
}

func TestRestartAfterACrashConverges(t *testing.T) {
	ref, crash := setup(t), setup(t)
	docs := corpus(1500, 3)
	insert(t, ref, docs...)
	insert(t, crash, docs...)
	if _, err := runner(ref, outboxrecovery.ModeExecutor).RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []int64{0, 1, 97, 250} {
		left := &atomic.Int64{}
		left.Store(k)
		r := runner(crash, outboxrecovery.ModeExecutor)
		r.Store = crashingStore{Store: r.Store.(*Store), left: left}
		if _, err := r.RunCycle(context.Background()); !errors.Is(err, errCrash) {
			t.Fatalf("expected the simulated crash, got %v", err)
		}
	}
	if _, err := runner(crash, outboxrecovery.ModeExecutor).RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all(t, ref), all(t, crash)) {
		t.Fatal("restarted run diverges from the uninterrupted run")
	}
}

func TestUnmarkReversesExhaustOnly(t *testing.T) {
	e := setup(t)
	insert(t, e,
		entry(1, "failed", bson.E{Key: "attempts", Value: int32(5)}),
		entry(2, "failed", bson.E{Key: "attempts", Value: int32(0)}, bson.E{Key: "lastError", Value: "UNKNOWN_EVENT_TYPE: X"}),
		entry(3, "failed", bson.E{Key: "attempts", Value: int32(1)}),
	)
	orig := all(t, e)
	if _, err := runner(e, outboxrecovery.ModeExecutor).RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := New(e.client, e.db, "", outboxrecovery.DefaultConfig())
	n, err := s.Unmark(context.Background(), outboxrecovery.ReasonPermanentError)
	if err != nil || n != 1 {
		t.Fatalf("unmark by reason: %d %v", n, err)
	}
	n, err = s.Unmark(context.Background(), "")
	if err != nil || n != 1 {
		t.Fatalf("unmark all: %d %v", n, err)
	}
	got := all(t, e)
	for i := 0; i < 2; i++ {
		kas := got[i]["kasRecovery"].(bson.M)
		if len(kas) != 0 {
			t.Fatalf("annotation left: %v", kas)
		}
		delete(got[i], "kasRecovery")
		if !reflect.DeepEqual(orig[i], got[i]) {
			t.Fatalf("unmark did not restore the dispatcher fields: %v", got[i])
		}
	}
	if got[2]["status"] != "pending" {
		t.Fatal("unmark must not touch a recovered entry")
	}
}

func TestCensusMatchesDirectCounts(t *testing.T) {
	e := setup(t)
	insert(t, e, corpus(2000, 4)...)
	cen, err := New(e.client, e.db, "", outboxrecovery.DefaultConfig()).Census(context.Background(), now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, st := range []string{"failed", "pending", "published"} {
		n, _ := e.coll.CountDocuments(ctx, bson.D{{Key: "status", Value: st}})
		if cen.ByStatus[st] != n {
			t.Fatalf("%s: census %d, direct %d", st, cen.ByStatus[st], n)
		}
	}
	n, _ := e.coll.CountDocuments(ctx, bson.D{{Key: "status", Value: "failed"}, {Key: "kasRecovery.exhaustedAt", Value: bson.D{{Key: "$exists", Value: true}}}})
	if cen.FailedExhausted != n {
		t.Fatalf("exhausted: census %d, direct %d", cen.FailedExhausted, n)
	}
}
