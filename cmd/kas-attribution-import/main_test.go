package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeKAS answers like the API: rejects negative amounts, replays known batch
// ids, and fails the first attempt of every batch with 503 to exercise retry.
type fakeKAS struct {
	mu       sync.Mutex
	seen     map[string]int
	attempts map[string]int
	sizes    []int
}

func (f *fakeKAS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-KAS-Service") != "escola-sis" || r.Header.Get("Authorization") != "Bearer tok-fictional" ||
		r.URL.Path != "/internal/v1/attribution/receipts:batch" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		BatchID string `json:"batch_id"`
		Items   []struct {
			ExternalID  string `json:"external_id"`
			AmountCents int64  `json:"amount_cents"`
		} `json:"items"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[req.BatchID]++
	if f.attempts[req.BatchID] == 1 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	created, rejected := 0, []map[string]any{}
	for i, it := range req.Items {
		if it.AmountCents <= 0 {
			rejected = append(rejected, map[string]any{"index": i, "external_id": it.ExternalID, "code": "amount_invalid"})
			continue
		}
		created++
	}
	status, dup := http.StatusCreated, false
	if _, ok := f.seen[req.BatchID]; ok {
		status, dup = http.StatusOK, true
	} else {
		f.sizes = append(f.sizes, len(req.Items))
	}
	f.seen[req.BatchID] = created
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"batch_id": req.BatchID, "created": created, "rejected": rejected, "duplicate": dup})
}

func writeCSV(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("external_id,customer_ref,amount_cents,received_at\n")
	for i := 1; i <= 5; i++ {
		amount := "1000"
		if i == 4 {
			amount = "-5"
		}
		b.WriteString("rec-" + string(rune('0'+i)) + ",cli-1," + amount + ",2026-11-10T15:00:00Z\n")
	}
	p := filepath.Join(t.TempDir(), "recebimentos.csv")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImportSplitsRetriesAndReportsLines(t *testing.T) {
	fake := &fakeKAS{seen: map[string]int{}, attempts: map[string]int{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	t.Setenv("FICTIONAL_TOKEN", "tok-fictional")
	file := writeCSV(t)
	args := []string{"-kas", srv.URL, "-service", "escola-sis", "-token-env", "FICTIONAL_TOKEN", "-kind", "receipts",
		"-file", file, "-batch-id", "rec-nov", "-actor", "user.fin-07", "-batch-size", "2"}
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var sum summary
	if err := json.Unmarshal(out.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Batches != 3 || sum.Created != 4 || sum.Rejected != 1 || sum.RejectedLines[0].Line != 5 ||
		sum.RejectedLines[0].ExternalID != "rec-4" || len(fake.sizes) != 3 || fake.sizes[2] != 1 {
		t.Fatalf("summary: %+v sizes %v", sum, fake.sizes)
	}
	// Running again is a replay of every batch.
	out.Reset()
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("rerun exit %d", code)
	}
	_ = json.Unmarshal(out.Bytes(), &sum)
	if sum.Replayed != 3 {
		t.Fatalf("rerun: %+v", sum)
	}
}

func TestConfigValidation(t *testing.T) {
	good := config{base: "https://kas.example", service: "s", token: "t", kind: "receipts", batchID: "b", actor: "user.a", size: 500}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*config){
		"plain http remote": func(c *config) { c.base = "http://kas.example" },
		"no token":          func(c *config) { c.token = "" },
		"bad kind":          func(c *config) { c.kind = "pacientes" },
		"bad actor":         func(c *config) { c.actor = "fin" },
		"big batch":         func(c *config) { c.size = 501 },
		"bad batch id":      func(c *config) { c.batchID = "a b" },
	} {
		c := good
		mut(&c)
		if c.validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	local := good
	local.base = "http://127.0.0.1:8088"
	if err := local.validate(); err != nil {
		t.Fatalf("loopback http: %v", err)
	}
}
