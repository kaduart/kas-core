package crmeventstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"kas-core-pilot/internal/eventreaper"
	"kas-core-pilot/internal/mongotest"
)

// fictitiousCorpus builds n legacy-shaped documents with every variation the
// reaper can meet: all statuses, ages around the threshold, every storage
// form of attempts and previous error shapes. Seeded, so runs are repeatable.
func fictitiousCorpus(n int, seed int64) []bson.D {
	rng := rand.New(rand.NewSource(seed))
	statuses := []string{"processing", "processing", "processing", "pending", "processed", "failed", "dead_letter"}
	cut := now.Add(-15 * time.Minute)
	ages := []func() time.Time{
		func() time.Time { return cut },                        // exact boundary
		func() time.Time { return cut.Add(-time.Millisecond) }, // just past
		func() time.Time { return cut.Add(time.Millisecond) },  // just before
		func() time.Time { return now.Add(-time.Duration(rng.Int63n(int64(3 * time.Hour)))) },
		func() time.Time { return now.Add(-time.Duration(rng.Int63n(int64(10 * time.Minute)))) },
	}
	dec, _ := bson.ParseDecimal128("2")
	attempts := []any{int32(0), int32(1), int32(2), int32(3), int32(4), int64(2), int64(5), 2.5, 3.0, nil, missing, "2", "7", "", "abc", true, false, dec, now.Add(-time.Hour), bson.D{{Key: "n", Value: 1}}}
	errs := []any{missing, bson.D{{Key: "message", Value: "falha anterior"}, {Key: "stack", Value: "trace"}, {Key: "code", Value: "E_FICTICIO"}}, "MAX_RETRIES_EXCEEDED: 3 tentativas falharam"}
	out := make([]bson.D, n)
	for i := 0; i < n; i++ {
		d := legacyDoc(fmt.Sprintf("evt-%06d", i), statuses[rng.Intn(len(statuses))], ages[rng.Intn(len(ages))]().Truncate(time.Millisecond), attempts[rng.Intn(len(attempts))])
		if e := errs[rng.Intn(len(errs))]; e != missing {
			d = append(d, bson.E{Key: "error", Value: e})
		}
		out[i] = d
	}
	return out
}

func snapshot(t *testing.T, e env) map[string]any {
	t.Helper()
	cur, err := e.coll.Find(context.Background(), bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close(context.Background())
	out := map[string]any{}
	for cur.Next(context.Background()) {
		js, err := bson.MarshalExtJSON(cur.Current, true, false)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(js, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "_id")
		out[m["eventId"].(string)] = m
	}
	return out
}

type legacyCycle struct {
	Reset  int `json:"reset"`
	Failed int `json:"failed"`
	Total  int `json:"total"`
}

// TestGoPortMatchesTheUnmodifiedNodeReaper runs the real legacy function and
// the Go port on identical copies of a 10,000-event fictitious corpus and
// requires the final documents to be identical, event by event.
func TestGoPortMatchesTheUnmodifiedNodeReaper(t *testing.T) {
	dir := os.Getenv("KAS_TEST_ORACLE_DIR")
	if dir == "" {
		t.Skip("KAS_TEST_ORACLE_DIR is not set; skipping the Node oracle comparison")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	n := 10000
	if v := os.Getenv("KAS_TEST_ORACLE_EVENTS"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	corpus := fictitiousCorpus(n, 20261118)
	legacy, port := setup(t), setup(t)
	insert(t, legacy, corpus...)
	insert(t, port, corpus...)

	// Node side: the unmodified legacy reaper, cycle after cycle.
	cmd := exec.Command("node", filepath.Join(dir, "oracle.js"))
	cmd.Env = append(os.Environ(),
		"ORACLE_MONGO_URI="+mongotest.URI(t),
		"ORACLE_DB="+legacy.db,
		"ORACLE_NOW_MS="+strconv.FormatInt(now.UnixMilli(), 10),
		"ORACLE_UNTIL_EMPTY=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle failed: %v\n%s", err, stderr.String())
	}
	var nodeReset, nodeFailed, nodeCycles int
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var c legacyCycle
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("oracle output: %v", err)
		}
		nodeReset += c.Reset
		nodeFailed += c.Failed
		nodeCycles++
	}

	// Go side: the executor with its lease, cycle after cycle.
	l := NewLease(port.client, port.db, "", "oracle-test", time.Minute)
	r := &eventreaper.Runner{Config: eventreaper.LegacyConfig(), Mode: eventreaper.ModeExecutor, Store: New(port.client, port.db, "", l), Lease: l, Now: fixedNow}
	var goReset, goFailed, goCycles int
	for {
		rep := r.RunCycle(context.Background())
		if rep.Err != "" || rep.Errors > 0 || rep.LostRace > 0 {
			t.Fatalf("go cycle: %+v", rep)
		}
		goReset += rep.Reset
		goFailed += rep.Failed
		goCycles++
		if rep.Total == 0 {
			break
		}
	}
	if nodeReset != goReset || nodeFailed != goFailed || nodeCycles != goCycles {
		t.Fatalf("summary differs: node reset=%d failed=%d cycles=%d, go reset=%d failed=%d cycles=%d",
			nodeReset, nodeFailed, nodeCycles, goReset, goFailed, goCycles)
	}
	a, b := snapshot(t, legacy), snapshot(t, port)
	if len(a) != n || len(b) != n {
		t.Fatalf("document counts %d / %d", len(a), len(b))
	}
	diverged := 0
	for id, doc := range a {
		if !reflect.DeepEqual(doc, b[id]) {
			if diverged < 5 {
				na, _ := json.Marshal(doc)
				nb, _ := json.Marshal(b[id])
				t.Errorf("%s differs\n node: %s\n   go: %s", id, na, nb)
			}
			diverged++
		}
	}
	if diverged > 0 {
		t.Fatalf("%d of %d documents differ", diverged, n)
	}
	t.Logf("%d events, %d cycles: %d reset, %d failed, 0 divergences", n, goCycles, goReset, goFailed)
}
