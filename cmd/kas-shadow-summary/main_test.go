package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

type check struct{ id, pred, obs, verdict, reason string }

func writeReport(t *testing.T, readOnly bool, days float64, perCycle func(i int) []check, mutate func(i int, m map[string]any)) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.jsonl")
	f, _ := os.Create(p)
	defer f.Close()
	enc := json.NewEncoder(f)
	start := map[string]any{"kind": "start", "at": base, "mode": "shadow", "protocol": "v2", "interval": "5m0s", "batch": 100, "host_class": "remote", "read_only_required": true}
	if readOnly {
		start["read_only"] = map[string]any{"users": []string{"kas_reaper_shadow@admin"}, "roles": []string{"kasReaperShadowRead@admin"}, "actions": []string{"find"}}
	}
	_ = enc.Encode(start)
	n := int(days*24*12) + 1
	for i := 0; i < n; i++ {
		var cs []map[string]any
		if perCycle != nil {
			for _, c := range perCycle(i) {
				cs = append(cs, map[string]any{"event_id": c.id, "predicted": c.pred, "observed": c.obs, "verdict": c.verdict, "observed_reason": c.reason})
			}
		}
		m := map[string]any{"kind": "cycle", "mode": "shadow", "started_at": base.Add(time.Duration(i) * 5 * time.Minute), "duration_ns": int64(40 * time.Millisecond),
			"total": 1, "outcomes": []map[string]any{{"result": "predicted"}}, "shadow_checks": cs,
			"proc": map[string]any{"rss_bytes": 20 << 20, "cpu_user_ms": i, "cpu_sys_ms": 0}}
		if mutate != nil {
			mutate(i, m)
		}
		_ = enc.Encode(m)
	}
	return p
}

func summarize(t *testing.T, args ...string) (int, string, result) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "s.json")
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"-json-out", out, "-with-ids"}, args...), &stdout, &stderr)
	var r result
	b, _ := os.ReadFile(out)
	_ = json.Unmarshal(b, &r)
	return code, stdout.String() + stderr.String(), r
}

func healthy(i int) []check {
	switch i % 4 {
	case 1:
		return []check{{"e" + itoa(i), "pending", "pending", "agree", "reaper"}}
	case 2:
		return []check{{"w" + itoa(i), "pending", "processed", "diverge", "none"}} // DV-1
	case 3:
		return []check{{"x" + itoa(i), "failed", "failed", "diverge", "other"}} // worker failed it first
	}
	return nil
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestFiveCleanDaysPass(t *testing.T) {
	code, out, r := summarize(t, writeReport(t, true, 5.01, healthy, nil))
	if code != 0 {
		t.Fatalf("code=%d\n%s", code, out)
	}
	if r.Classes[clsConcludedByWorker] == 0 || r.Classes[clsSameResultOther] == 0 || r.Verdicts["agree"] == 0 {
		t.Fatalf("classes not counted: %+v %+v", r.Classes, r.Verdicts)
	}
	if strings.Contains(out, "e1") || strings.Contains(out, "w2") {
		t.Fatal("the Markdown must not carry event ids")
	}
}

func TestDecisionMismatchFails(t *testing.T) {
	bad := func(i int) []check {
		if i == 100 {
			return []check{{"m", "pending", "failed", "diverge", "reaper"}} // Node reaper sent it to failed, KAS said pending
		}
		return healthy(i)
	}
	code, out, r := summarize(t, writeReport(t, true, 5.01, bad, nil))
	if code != 1 || r.Classes[clsDecisionMismatch] != 1 || !strings.Contains(out, "| C2 ") {
		t.Fatalf("code=%d classes=%v\n%s", code, r.Classes, out)
	}
}

func TestNoReadOnlyProofFails(t *testing.T) {
	code, _, r := summarize(t, writeReport(t, false, 5.01, healthy, nil))
	if code != 1 || r.Criteria[0].Pass {
		t.Fatalf("code=%d criteria=%+v", code, r.Criteria[0])
	}
}

func TestAnyWriteOutcomeFails(t *testing.T) {
	code, _, r := summarize(t, writeReport(t, true, 5.01, healthy, func(i int, m map[string]any) {
		if i == 7 {
			m["outcomes"] = []map[string]any{{"result": "applied"}}
		}
	}))
	if code != 1 || r.WriteOutcomes != 1 {
		t.Fatalf("code=%d writes=%d", code, r.WriteOutcomes)
	}
}

func TestStuckEventIsFlagged(t *testing.T) {
	stuck := func(i int) []check {
		if i >= 10 && i < 16 {
			return []check{{"s", "pending", "processing", "still_processing", "none"}}
		}
		return healthy(i)
	}
	code, _, r := summarize(t, writeReport(t, true, 5.01, stuck, nil))
	if code != 1 || r.Classes[clsNodeNotActing] != 1 {
		t.Fatalf("code=%d classes=%v", code, r.Classes)
	}
}

func TestErrorStreakAndCoverage(t *testing.T) {
	code, _, r := summarize(t, writeReport(t, true, 5.01, healthy, func(i int, m map[string]any) {
		if i >= 50 && i < 55 {
			m["error"] = "candidates: server selection timeout"
		}
	}))
	if code != 1 || r.MaxErrorStreak != 5 {
		t.Fatalf("code=%d streak=%d", code, r.MaxErrorStreak)
	}
	// a gap of 4 hours every day drops coverage below 90%
	p := writeReport(t, true, 5.01, healthy, func(i int, m map[string]any) {
		if i%288 < 48 {
			m["kind"] = "skip-me"
		}
	})
	code, _, r = summarize(t, p)
	if code != 1 || r.Coverage >= 0.9 {
		t.Fatalf("code=%d coverage=%v", code, r.Coverage)
	}
}

func TestShortPeriodIsInsufficientNotFailed(t *testing.T) {
	code, _, _ := summarize(t, writeReport(t, true, 1, healthy, nil))
	if code != 3 {
		t.Fatalf("code=%d, want 3 (insufficient period)", code)
	}
	code, _, _ = summarize(t, "-min-days", "0", writeReport(t, true, 1, healthy, nil))
	if code != 0 {
		t.Fatalf("daily partial summary: code=%d", code)
	}
}

func TestHealthProbe(t *testing.T) {
	hp := filepath.Join(t.TempDir(), "h.jsonl")
	f, _ := os.Create(hp)
	enc := json.NewEncoder(f)
	for i := 0; i < 100; i++ {
		st := 200
		if i < 3 {
			st = 503
		}
		_ = enc.Encode(map[string]any{"at": base.Add(time.Duration(i) * time.Minute), "status": st, "ms": 120})
	}
	f.Close()
	code, _, r := summarize(t, "-health", hp, writeReport(t, true, 5.01, healthy, nil))
	if code != 1 || r.Health["ok_share"] != 0.97 {
		t.Fatalf("code=%d health=%v", code, r.Health)
	}
}
