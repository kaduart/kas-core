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

func mkItem(id, ty, action, reason string, attempts, age int64, dest []string, known bool) map[string]any {
	return map[string]any{"id": id, "type": ty, "action": action, "reason": reason, "attempts": attempts, "age_s": age, "destinations": dest, "known": known}
}

// writeReport: one run, a cycle every 5 min for `days`; items(i) gives the items of cycle i.
func writeReport(t *testing.T, readOnly bool, days float64, items func(i int) []map[string]any, mutate func(i int, m map[string]any)) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.jsonl")
	f, _ := os.Create(p)
	defer f.Close()
	enc := json.NewEncoder(f)
	start := map[string]any{"kind": "start", "at": base, "mode": "shadow", "interval": "5m0s", "read_only_required": true}
	if readOnly {
		start["read_only"] = map[string]any{"users": []string{"kas_outbox_shadow@admin"}, "roles": []string{"kasOutboxShadowRead@admin"}, "actions": []string{"find"}}
	}
	_ = enc.Encode(start)
	n := int(days*24*12) + 1
	for i := 0; i < n; i++ {
		m := map[string]any{"kind": "cycle", "mode": "shadow", "started_at": base.Add(time.Duration(i) * 5 * time.Minute),
			"duration_ns": int64(30 * time.Millisecond), "recovered": 0, "exhausted": 0, "lostRaces": 0,
			"census": map[string]any{"byStatus": map[string]any{"published": 100, "failed": 3}, "pendingStale": 0}}
		if items != nil {
			m["items"] = items(i)
			m["scanned"] = len(items(i))
		}
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
	code := run(append([]string{"-json-out", out}, args...), &stdout, &stderr)
	var r result
	b, _ := os.ReadFile(out)
	_ = json.Unmarshal(b, &r)
	return code, stdout.String() + stderr.String(), r
}

var notif = []string{"notification", "patient-projection", "clinical-orchestrator"}

func someFailures(i int) []map[string]any {
	its := []map[string]any{
		mkItem("e1", "APPOINTMENT_CREATED", "recover", "retry", 2, int64(3600*30+i*300), notif, true),
		mkItem("e2", "APPOINTMENT_CONFIRMED", "exhaust", "permanent_error", 1, 100, []string{}, false),
		mkItem("e3", "PATIENT_UPDATED", "none", "backoff_not_elapsed", 1, 50, []string{"patient-projection"}, true),
	}
	if i > 10 { // e3 later becomes recoverable: the last observation counts
		its[2] = mkItem("e3", "PATIENT_UPDATED", "recover", "retry", 1, 600, []string{"patient-projection"}, true)
	}
	return its
}

func TestTabulatesWhatTheRecoveryWouldDo(t *testing.T) {
	code, out, r := summarize(t, "-min-days", "0", writeReport(t, true, 1, someFailures, nil))
	if code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	if r.Entries != 3 || r.ByAction["recover"] != 2 || r.ByAction["exhaust"] != 1 {
		t.Fatalf("entries=%d by_action=%v", r.Entries, r.ByAction)
	}
	if r.RecoverExternal != 1 || r.RecoverByQueue["notification"] != 1 || r.RecoverByQueue["patient-projection"] != 2 {
		t.Fatalf("external=%d by_queue=%v", r.RecoverExternal, r.RecoverByQueue)
	}
	if r.Age["1 a 7 dias"] != 1 || r.Age["até 1 h"] != 1 || r.AgeExternal["1 a 7 dias"] != 1 {
		t.Fatalf("age %v external %v", r.Age, r.AgeExternal)
	}
	if r.Attempts["2"] != 1 || r.Attempts["1"] != 1 {
		t.Fatalf("attempts %v", r.Attempts)
	}
	for _, want := range []string{"APPOINTMENT_CREATED", "sim (WhatsApp/e-mail)", "sem rota (UNKNOWN_EVENT_TYPE)", "Só observação"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Markdown lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "e1") && strings.Contains(out, "| e1") {
		t.Fatal("entry id in the Markdown")
	}
}

func TestWritesOrMissingProofFail(t *testing.T) {
	if code, out, _ := summarize(t, "-min-days", "0", writeReport(t, false, 0.2, someFailures, nil)); code != 1 || !strings.Contains(out, "| O1 Nenhuma escrita pela sombra | FALHA") {
		t.Fatalf("missing read-only proof not caught: %d\n%s", code, out)
	}
	p := writeReport(t, true, 0.2, someFailures, func(i int, m map[string]any) {
		if i == 3 {
			m["recovered"] = 1
		}
	})
	if code, _, r := summarize(t, "-min-days", "0", p); code != 1 || r.Writes != 1 {
		t.Fatalf("a write was not caught: code %d writes %d", code, r.Writes)
	}
}

func TestNoFailuresIsInconclusive(t *testing.T) {
	code, out, r := summarize(t, "-min-days", "0", writeReport(t, true, 1, nil, nil))
	if code != 4 || r.Entries != 0 || !strings.Contains(out, "INCONCLUSIVO") {
		t.Fatalf("code %d entries %d\n%s", code, r.Entries, out)
	}
}

func TestWindowAndCoverage(t *testing.T) {
	p := writeReport(t, true, 10, someFailures, func(i int, m map[string]any) {
		if i < 12*24*4 {
			m["error"] = "warm-up"
		}
	})
	since, until := "2026-11-24T00:00:00-03:00", "2026-11-29T00:00:00-03:00"
	code, out, r := summarize(t, "-min-days", "5", "-since", since, "-until", until, p)
	if code != 0 || r.ErrorCycles != 0 || r.Coverage != 1 || r.Days != 5 {
		t.Fatalf("code %d errors %d coverage %v days %v\n%s", code, r.ErrorCycles, r.Coverage, r.Days, out)
	}
	if code, _, _ := summarize(t, "-min-days", "5", "-since", since, p); code != 2 {
		t.Fatal("-since without -until accepted")
	}
	// Período curto sem falha: código 3.
	if code, _, _ := summarize(t, "-min-days", "5", writeReport(t, true, 1, someFailures, nil)); code != 3 {
		t.Fatalf("short period: code %d", code)
	}
}
