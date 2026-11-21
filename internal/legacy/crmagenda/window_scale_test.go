package crmagenda

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Scale check of the window strategy, with ~100k FICTITIOUS appointments in a
// disposable local MongoDB that has only the indexes the CRM model declares
// ({date:-1}, {doctor:1,date:1}, _id). It compares what MongoDB examines for
// the window read against the incremental read (updatedAt > cursor), which has
// no index to use there. No real data is read.
//
// KAS_BENCH=1 KAS_BENCH_WINDOW=1 [KAS_BENCH_OUT=file.json]

type planStats struct {
	Query        string `json:"query"`
	Stage        string `json:"winning_stage"`
	Index        string `json:"index,omitempty"`
	Returned     int64  `json:"returned"`
	DocsExamined int64  `json:"docs_examined"`
	KeysExamined int64  `json:"keys_examined"`
	Millis       int64  `json:"execution_ms"`
}

func explainFind(t *testing.T, db *mongo.Database, name string, filter, sort bson.D, limit int64) planStats {
	t.Helper()
	find := bson.D{{Key: "find", Value: Collection}, {Key: "filter", Value: filter}, {Key: "projection", Value: Projection}}
	if len(sort) > 0 {
		find = append(find, bson.E{Key: "sort", Value: sort})
	}
	if limit > 0 {
		find = append(find, bson.E{Key: "limit", Value: limit})
	}
	var out struct {
		QueryPlanner struct {
			WinningPlan bson.Raw `bson:"winningPlan"`
		} `bson:"queryPlanner"`
		ExecutionStats struct {
			NReturned           int64 `bson:"nReturned"`
			TotalDocsExamined   int64 `bson:"totalDocsExamined"`
			TotalKeysExamined   int64 `bson:"totalKeysExamined"`
			ExecutionTimeMillis int64 `bson:"executionTimeMillis"`
		} `bson:"executionStats"`
	}
	if err := db.RunCommand(context.Background(), bson.D{{Key: "explain", Value: find}, {Key: "verbosity", Value: "executionStats"}}).Decode(&out); err != nil {
		t.Fatalf("explain %s: %v", name, err)
	}
	stage, index := planLeaf(out.QueryPlanner.WinningPlan)
	return planStats{Query: name, Stage: stage, Index: index, Returned: out.ExecutionStats.NReturned,
		DocsExamined: out.ExecutionStats.TotalDocsExamined, KeysExamined: out.ExecutionStats.TotalKeysExamined,
		Millis: out.ExecutionStats.ExecutionTimeMillis}
}

// planLeaf walks inputStage down to the access stage (IXSCAN or COLLSCAN).
func planLeaf(plan bson.Raw) (string, string) {
	for plan != nil {
		if v, err := plan.LookupErr("queryPlan"); err == nil { // slot-based engine wraps the plan
			plan, _ = v.DocumentOK()
			continue
		}
		stage, _ := plan.Lookup("stage").StringValueOK()
		if in, err := plan.LookupErr("inputStage"); err == nil {
			plan, _ = in.DocumentOK()
			continue
		}
		index, _ := plan.Lookup("indexName").StringValueOK()
		return stage, index
	}
	return "", ""
}

func TestWindowScale(t *testing.T) {
	if os.Getenv("KAS_BENCH") != "1" || os.Getenv("KAS_BENCH_WINDOW") != "1" {
		t.Skip("set KAS_BENCH=1 KAS_BENCH_WINDOW=1 to run the window scale check")
	}
	e := newEnv(t)
	ctx := context.Background()
	for len(e.doc) < 10 {
		e.doc = append(e.doc, bson.NewObjectID())
	}
	if _, err := e.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "date", Value: -1}}},
		{Keys: bson.D{{Key: "doctor", Value: 1}, {Key: "date", Value: 1}}},
	}); err != nil {
		t.Fatal(err)
	}
	// 10 doctors × 730 days × 14 slots of 40 min = 102,200 appointments, 2026–2027.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Millisecond)
	var batch []appt
	total := 0
	flush := func() {
		if len(batch) > 0 {
			e.insert(batch...)
			total += len(batch)
			batch = batch[:0]
		}
	}
	t0 := time.Now()
	for d := 0; d < 730; d++ {
		day := start.AddDate(0, 0, d).Format("2006-01-02")
		for doc := 0; doc < 10; doc++ {
			for s := 0; s < 14; s++ {
				m := 8*60 + 40*s
				batch = append(batch, appt{Doctor: doc, Patient: (d + s) % 3, Day: day, Time: fmt.Sprintf("%02d:%02d", m/60, m%60),
					Duration: int32(40), Updated: base.Add(time.Duration(d*140+doc*14+s) * time.Millisecond)})
				if len(batch) == 5000 {
					flush()
				}
			}
		}
	}
	flush()
	seedSec := time.Since(t0).Seconds()

	// "Today" is 2027-03-01 in this fictitious base: window of 7 days back, 120 ahead.
	from := time.Date(2027, 2, 22, 3, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 127)
	windowFilter := bson.D{{Key: "date", Value: bson.D{
		{Key: "$gte", Value: from.Add(-24 * time.Hour)}, {Key: "$lt", Value: to.Add(24 * time.Hour)}}}}
	cursor := base.Add(time.Duration(729*140) * time.Millisecond) // only the last day changed
	incFilter := bson.D{{Key: "updatedAt", Value: bson.D{{Key: "$gte", Value: cursor}}}}
	incSort := bson.D{{Key: "updatedAt", Value: 1}, {Key: "_id", Value: 1}}

	plans := []planStats{
		explainFind(t, e.db, "window_7d_back_120d_ahead", windowFilter, nil, 0),
		explainFind(t, e.db, "incremental_updatedAt_no_index", incFilter, incSort, 500),
	}

	// The window run itself, into the local PostgreSQL mirror.
	im := e.importer(tenantA, keyA, 1000)
	w0 := time.Now()
	rep1, err := im.Window(ctx, from, to, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstSec := time.Since(w0).Seconds()
	w1 := time.Now()
	rep2, err := im.Window(ctx, from, to, 0)
	if err != nil {
		t.Fatal(err)
	}
	againSec := time.Since(w1).Seconds()

	// What the update index would cost (option C), on this copy only.
	if _, err := e.coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "updatedAt", Value: 1}, {Key: "_id", Value: 1}}},
		options.CreateIndexes()); err != nil {
		t.Fatal(err)
	}
	plans = append(plans, explainFind(t, e.db, "incremental_with_updatedAt_index", incFilter, incSort, 500))

	result := map[string]any{
		"appointments": total, "seed_seconds": seedSec, "plans": plans,
		"window_first_run": map[string]any{"read": rep1.Read, "changed": rep1.Changed, "batches": rep1.Batches, "seconds": firstSec},
		"window_next_run":  map[string]any{"read": rep2.Read, "changed": rep2.Changed, "deleted": rep2.Deleted, "seconds": againSec},
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	t.Logf("%s", b)
	if out := os.Getenv("KAS_BENCH_OUT"); out != "" {
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if plans[0].Stage != "IXSCAN" || plans[0].DocsExamined != plans[0].Returned {
		t.Fatalf("window read must use the date index and examine only what it returns: %+v", plans[0])
	}
}
