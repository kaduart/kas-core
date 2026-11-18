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

// fictitiousCorpusV2 extends the v1 corpus with every ownership shape the v2
// reaper can meet: lease expired, exactly now, alive, null, wrong type or
// absent; token present, null or absent; recoveryCount in every storage form.
func fictitiousCorpusV2(n int, seed int64) []bson.D {
	rng := rand.New(rand.NewSource(seed))
	base := fictitiousCorpus(n, seed)
	leases := []any{missing, missing, now.Add(-time.Second), now, now.Add(time.Millisecond), now.Add(time.Minute), nil, "2026-11-18"}
	tokens := []any{missing, "tok", nil}
	dec, _ := bson.ParseDecimal128("1")
	recoveries := []any{missing, missing, int32(0), int32(1), int32(2), int32(3), int64(1), 1.5, "1", "", nil, true, dec}
	for i, d := range base {
		if l := leases[rng.Intn(len(leases))]; l != missing {
			d = append(d, bson.E{Key: "leaseExpiresAt", Value: l})
		}
		if tk := tokens[rng.Intn(len(tokens))]; tk != missing {
			if s, ok := tk.(string); ok {
				tk = fmt.Sprintf("%s-%06d", s, i)
			}
			d = append(d, bson.E{Key: "processingToken", Value: tk}, bson.E{Key: "processingOwner", Value: "worker-ficticio"})
		}
		if rc := recoveries[rng.Intn(len(recoveries))]; rc != missing {
			d = append(d, bson.E{Key: "recoveryCount", Value: rc})
		}
		base[i] = d
	}
	return base
}

type legacyCycleV2 struct {
	Reset      int      `json:"reset"`
	Failed     int      `json:"failed"`
	DeadLetter int      `json:"deadLetter"`
	LostRace   int      `json:"lostRace"`
	Total      int      `json:"total"`
	Alerts     []string `json:"alerts"`
}

// TestGoPortV2MatchesTheCRMOwnershipReaper runs the unmodified reaper of the
// CRM branch feat/eventstore-posse and the Go port in protocol v2 on identical
// copies of a fictitious corpus and requires identical final documents.
func TestGoPortV2MatchesTheCRMOwnershipReaper(t *testing.T) {
	dir := os.Getenv("KAS_TEST_ORACLE_V2_DIR")
	if dir == "" {
		t.Skip("KAS_TEST_ORACLE_V2_DIR is not set; skipping the v2 Node oracle comparison")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	n := 10000
	if v := os.Getenv("KAS_TEST_ORACLE_EVENTS"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	corpus := fictitiousCorpusV2(n, 20261118)
	legacy, port := setup(t), setup(t)
	insert(t, legacy, corpus...)
	insert(t, port, corpus...)

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
	var node legacyCycleV2
	nodeCycles := 0
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var c legacyCycleV2
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("oracle output: %v", err)
		}
		node.Reset += c.Reset
		node.Failed += c.Failed
		node.DeadLetter += c.DeadLetter
		node.LostRace += c.LostRace
		node.Alerts = append(node.Alerts, c.Alerts...)
		nodeCycles++
	}
	if node.LostRace != 0 {
		t.Fatalf("oracle reported %d lost races without concurrent writers", node.LostRace)
	}
	if len(node.Alerts) != node.DeadLetter {
		t.Fatalf("oracle: %d dead_letter but %d alerts", node.DeadLetter, len(node.Alerts))
	}

	l := NewLease(port.client, port.db, "", "oracle-v2-test", time.Minute)
	r := &eventreaper.Runner{Config: eventreaper.V2Config(), Mode: eventreaper.ModeExecutor, Store: New(port.client, port.db, "", l), Lease: l, Now: fixedNow}
	var goRep eventreaper.CycleReport
	goCycles := 0
	for {
		rep := r.RunCycle(context.Background())
		if rep.Err != "" || rep.Errors > 0 || rep.LostRace > 0 {
			t.Fatalf("go cycle: %+v", rep)
		}
		goRep.Reset += rep.Reset
		goRep.Failed += rep.Failed
		goRep.DeadLetter += rep.DeadLetter
		goCycles++
		if rep.Total == 0 {
			break
		}
		if goCycles > 1000 {
			t.Fatal("go port does not converge")
		}
	}
	if node.Reset != goRep.Reset || node.Failed != goRep.Failed || node.DeadLetter != goRep.DeadLetter || nodeCycles != goCycles {
		t.Fatalf("summary differs: node reset=%d failed=%d dead=%d cycles=%d, go reset=%d failed=%d dead=%d cycles=%d",
			node.Reset, node.Failed, node.DeadLetter, nodeCycles, goRep.Reset, goRep.Failed, goRep.DeadLetter, goCycles)
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
	t.Logf("%d events, %d cycles: %d reset, %d failed, %d dead_letter (%d alerts), 0 divergences",
		n, goCycles, goRep.Reset, goRep.Failed, goRep.DeadLetter, len(node.Alerts))
}
