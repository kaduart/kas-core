package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func countLines(t *testing.T, dir string) int {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(contents), "\n")
}

func TestJSONLIdenticalResendIsDuplicateWithoutNewLine(t *testing.T) {
	dir := testDataDir(t)
	store, err := OpenJSONL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testEvent("evt.dup-01")
	if err := store.Append(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	retry := first
	retry.ReceivedAt = "2026-11-17T09:00:00Z" // server time differs on each attempt
	var duplicate *DuplicateError
	if err := store.Append(context.Background(), retry); !errors.As(err, &duplicate) {
		t.Fatalf("identical resend error = %v, want DuplicateError", err)
	}
	if duplicate.Stored != first {
		t.Fatalf("duplicate must carry the original event, got %+v", duplicate.Stored)
	}
	if n := countLines(t, dir); n != 1 {
		t.Fatalf("lines on disk = %d, want 1", n)
	}
}

func TestJSONLConflictingContentIsRejectedWithoutWrite(t *testing.T) {
	dir := testDataDir(t)
	store, err := OpenJSONL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append(context.Background(), testEvent("evt.conflict-01")); err != nil {
		t.Fatal(err)
	}
	changed := testEvent("evt.conflict-01")
	changed.Result = "failure"
	if err := store.Append(context.Background(), changed); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting content error = %v, want ErrEventConflict", err)
	}
	if n := countLines(t, dir); n != 1 {
		t.Fatalf("lines on disk = %d, want 1", n)
	}
	// A conflict must not poison the store.
	if err := store.Append(context.Background(), testEvent("evt.conflict-02")); err != nil {
		t.Fatalf("store unusable after a conflict: %v", err)
	}
}

func TestEventIdentityIsPerTenantAndService(t *testing.T) {
	store, err := OpenJSONL(testDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := testEvent("evt.shared-id")
	otherTenant := base
	otherTenant.TenantID = "tenant-demo-b"
	otherService := base
	otherService.ServiceID = "demo-node-b"
	for _, e := range []Event{base, otherTenant, otherService} {
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("same event_id under another tenant/service must be allowed: %v", err)
		}
	}
}

func TestJSONLIdempotencySurvivesRestartAndToleratesHistoricalDuplicates(t *testing.T) {
	dir := testDataDir(t)
	store, err := OpenJSONL(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := testEvent("evt.restart-01")
	if err := store.Append(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a file written before uniqueness existed: append a duplicate line.
	path := filepath.Join(dir, "audit.jsonl")
	existing, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(existing, existing...), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJSONL(dir)
	if err != nil {
		t.Fatalf("historical duplicate must not block startup: %v", err)
	}
	defer reopened.Close()
	var duplicate *DuplicateError
	if err := reopened.Append(context.Background(), first); !errors.As(err, &duplicate) {
		t.Fatalf("resend after restart error = %v, want DuplicateError", err)
	}
	if n := countLines(t, dir); n != 2 {
		t.Fatalf("existing history must be preserved untouched: %d lines", n)
	}
}

func TestServiceSubmitReportsDuplicateAndConflict(t *testing.T) {
	store, err := OpenJSONL(testDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store)
	submission := testSubmission("evt.svc-01")
	first, err := service.Submit(context.Background(), "fono-node-local", submission)
	if err != nil || first.Duplicate {
		t.Fatalf("first submit = %+v, %v", first, err)
	}
	again, err := service.Submit(context.Background(), "fono-node-local", submission)
	if err != nil || !again.Duplicate || again.Event != first.Event {
		t.Fatalf("identical resend = %+v, %v; want duplicate returning the original event %+v", again, err, first.Event)
	}
	submission.Action = "settings.deleted"
	if _, err := service.Submit(context.Background(), "fono-node-local", submission); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting submit error = %v", err)
	}
}

func TestJSONLReadiness(t *testing.T) {
	store, err := OpenJSONL(testDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewService(store).Ready(context.Background()); err != nil {
		t.Fatalf("open store must be ready: %v", err)
	}
	poisoned := &JSONLStore{file: &injectedDurableFile{syncErr: errors.New("simulated")}}
	_ = poisoned.Append(context.Background(), testEvent("evt.ready-01"))
	if err := poisoned.Ready(context.Background()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("poisoned store must not be ready: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := NewService(store).Ready(context.Background()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("closed store must not be ready: %v", err)
	}
}

func TestOpenPostgresRejectsPlaintextRemoteBeforeConnecting(t *testing.T) {
	_, err := OpenPostgres(context.Background(), "postgres://kas:segredo-ficticio@db.example.com/x?sslmode=disable")
	if err == nil || !strings.Contains(err.Error(), "TLS is required") {
		t.Fatalf("error = %v, want TLS requirement", err)
	}
	if strings.Contains(err.Error(), "segredo-ficticio") {
		t.Fatalf("error leaks credentials: %v", err)
	}
}
