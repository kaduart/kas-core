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

// Coverage counts only cycles that finished without error or skip, one per 5-minute slot:
// error cycles and duplicated cycles from overlapping runs do not inflate it.
func TestCoverageCountsOnlyCompletedCyclesOncePerSlot(t *testing.T) {
	// 1 day; every 3rd cycle failed -> about 67% covered even though every slot had a cycle line.
	p := writeReport(t, true, 1, nil, func(i int, m map[string]any) {
		if i%3 == 0 {
			m["error"] = "candidates: timeout"
		}
	})
	_, _, r := summarize(t, "-min-days", "0", "-max-error-rate", "1", "-max-error-streak", "999", p)
	if r.Coverage > 0.7 || r.Coverage < 0.6 {
		t.Fatalf("coverage with failed cycles = %v, want ~0.67", r.Coverage)
	}
	// The same report twice (two overlapping runs): coverage stays 100%, never above, and duplicates are reported.
	q := writeReport(t, true, 1, nil, nil)
	_, _, r = summarize(t, "-min-days", "0", q, q)
	if r.Coverage != 1 || r.DuplicateSlots != r.ExpectedCycles || r.CoveredSlots != r.ExpectedCycles {
		t.Fatalf("overlap: coverage=%v covered=%d expected=%d dup=%d", r.Coverage, r.CoveredSlots, r.ExpectedCycles, r.DuplicateSlots)
	}
	// Skipped cycles (e.g. already_running) do not count either.
	s := writeReport(t, true, 1, nil, func(i int, m map[string]any) {
		if i%2 == 0 {
			m["skipped"] = "already_running"
		}
	})
	_, _, r = summarize(t, "-min-days", "0", s)
	if r.Coverage > 0.55 {
		t.Fatalf("coverage with skipped cycles = %v", r.Coverage)
	}
}

// Janela oficial: ciclos antes de -since (aquecimento) não contam, e a cobertura
// é medida contra a janela inteira.
func TestOfficialWindow(t *testing.T) {
	// 10 dias de ciclos a partir de base (20/11 00h BRT); janela 24/11 00h → 29/11 00h BRT.
	p := writeReport(t, true, 10, healthy, func(i int, m map[string]any) {
		if i < 12*24*4 { // aquecimento com erro em todos os ciclos: não pode contar
			m["error"] = "warm-up failure"
		}
	})
	since, until := "2026-11-24T00:00:00-03:00", "2026-11-29T00:00:00-03:00"
	code, out, r := summarize(t, "-min-days", "5", "-since", since, "-until", until, p)
	if code != 0 {
		t.Fatalf("code %d, want 0\n%s", code, out)
	}
	if r.ErrorCycles != 0 || r.Cycles != 5*24*12 {
		t.Fatalf("window not applied: cycles=%d errors=%d", r.Cycles, r.ErrorCycles)
	}
	if r.Days != 5 || r.ExpectedCycles != 5*24*12 || r.Coverage != 1 {
		t.Fatalf("days=%v expected=%d coverage=%v", r.Days, r.ExpectedCycles, r.Coverage)
	}
	if !strings.Contains(out, "Janela oficial") {
		t.Fatal("window not shown in the Markdown")
	}

	// Um dia inteiro sem ciclo dentro da janela: a cobertura cai e C7 falha.
	p2 := writeReport(t, true, 10, healthy, func(i int, m map[string]any) {
		if i >= 12*24*5 && i < 12*24*6 {
			m["error"] = "down"
		}
	})
	code, out, r = summarize(t, "-min-days", "5", "-since", since, "-until", until, p2)
	if code != 1 || r.Coverage > 0.81 {
		t.Fatalf("missing day not caught: code=%d coverage=%v\n%s", code, r.Coverage, out)
	}
}

func TestWindowFlagsValidated(t *testing.T) {
	p := writeReport(t, true, 0.1, healthy, nil)
	for _, args := range [][]string{
		{"-since", "2026-11-24T00:00:00-03:00", p},
		{"-since", "ontem", "-until", "2026-11-29T00:00:00-03:00", p},
		{"-since", "2026-11-29T00:00:00-03:00", "-until", "2026-11-24T00:00:00-03:00", p},
	} {
		if code, _, _ := summarize(t, args...); code != 2 {
			t.Fatalf("%v accepted", args)
		}
	}
}

// Zero candidatos: ciclos saudáveis, mas a comparação com o Node é inconclusiva.
func TestZeroCandidatesIsInconclusive(t *testing.T) {
	p := writeReport(t, true, 1, nil, func(i int, m map[string]any) {
		m["total"] = 0
		m["outcomes"] = []map[string]any{}
	})
	code, out, r := summarize(t, "-min-days", "0", p)
	if code != 4 {
		t.Fatalf("code %d, want 4 (inconclusive)\n%s", code, out)
	}
	if r.Comparison != "inconclusiva" || !strings.Contains(out, "INCONCLUSIVA") || !strings.Contains(out, "| INCONCLUSIVO |") {
		t.Fatalf("inconclusive not reported\n%s", out)
	}
	// Uma falha real continua tendo prioridade sobre o inconclusivo.
	p2 := writeReport(t, false, 1, nil, func(i int, m map[string]any) { m["total"] = 0 })
	if code, _, _ := summarize(t, "-min-days", "0", p2); code != 1 {
		t.Fatalf("failure hidden by inconclusive: code %d", code)
	}
	// Com candidatos e conferências, a comparação é conclusiva.
	if _, _, r := summarize(t, "-min-days", "0", writeReport(t, true, 1, healthy, nil)); r.Comparison != "conclusiva" {
		t.Fatalf("comparison %q", r.Comparison)
	}
}
