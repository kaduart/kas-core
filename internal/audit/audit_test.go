package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testSubmission(id string) Submission {
	return Submission{
		ContractVersion: ContractVersion,
		EventID:         id,
		TenantID:        "tenant-demo-a",
		ServiceID:       "fono-node-local",
		Actor:           "user.demo-01",
		Action:          "settings.updated",
		Resource:        "setting.max_meta_keys",
		Result:          "success",
		CorrelationID:   "corr.demo-01",
		OccurredAt:      "2026-11-16T12:00:00Z",
	}
}

func testDataDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join("..", "..", ".tools")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "audit-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestServiceBindsAuthenticatedServiceAndSeparatesTimes(t *testing.T) {
	store, err := OpenJSONL(testDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store)
	submission := testSubmission("evt.demo-01")
	if _, err := service.Record(context.Background(), "other-service", submission); !errors.Is(err, ErrServiceMismatch) {
		t.Fatalf("service mismatch error = %v", err)
	}
	event, err := service.Record(context.Background(), "fono-node-local", submission)
	if err != nil {
		t.Fatal(err)
	}
	if event.ServiceID != "fono-node-local" || event.OccurredAt != submission.OccurredAt || event.ReceivedAt == "" || event.ReceivedAt == event.OccurredAt {
		t.Fatalf("unexpected event identity or timestamps: %+v", event)
	}
}

func TestValidationRejectsFreeTextAndInvalidFields(t *testing.T) {
	valid := testSubmission("evt.demo-02")
	cases := map[string]func(*Submission){
		"bad contract":       func(s *Submission) { s.ContractVersion = "v2" },
		"empty actor":        func(s *Submission) { s.Actor = "" },
		"free text actor":    func(s *Submission) { s.Actor = "Jane Doe" },
		"token shaped actor": func(s *Submission) { s.Actor = "Bearer.abc" },
		"bad action":         func(s *Submission) { s.Action = "Action with details" },
		"bad result":         func(s *Submission) { s.Result = "maybe" },
		"bad timestamp":      func(s *Submission) { s.OccurredAt = "yesterday" },
		"empty correlation":  func(s *Submission) { s.CorrelationID = "" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			submission := valid
			change(&submission)
			if err := ValidateSubmission(submission); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestConcurrentAppendAndRestartPreservesEveryRecord(t *testing.T) {
	dir := testDataDir(t)
	store, err := OpenJSONL(dir)
	if err != nil {
		t.Fatal(err)
	}
	const count = 64
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := Event{
				ContractVersion: ContractVersion,
				EventID:         fmt.Sprintf("evt.%03d", i),
				TenantID:        "tenant-demo-a",
				ServiceID:       "fono-node-local",
				Actor:           "user.demo-01",
				Action:          "settings.updated",
				Resource:        "setting.max_meta_keys",
				Result:          "success",
				CorrelationID:   "corr.concurrent",
				OccurredAt:      "2026-11-16T12:00:00Z",
				ReceivedAt:      time.Now().UTC().Format(time.RFC3339Nano),
			}
			errCh <- store.Append(context.Background(), event)
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "audit.jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(contents), "\n"); lines != count {
		t.Fatalf("record count on disk = %d, want %d", lines, count)
	}
	reopened, err := OpenJSONL(dir)
	if err != nil {
		t.Fatalf("restart rejected valid records: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Append(context.Background(), Event{
		ContractVersion: ContractVersion, EventID: "evt.after-restart", TenantID: "tenant-demo-a",
		ServiceID: "fono-node-local", Actor: "system.demo", Action: "settings.checked",
		Resource: "setting.max_meta_keys", Result: "success", CorrelationID: "corr.restart",
		OccurredAt: "2026-11-16T12:00:00Z", ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("append after restart failed: %v", err)
	}
	contents, err = os.ReadFile(path)
	if err != nil || strings.Count(string(contents), "\n") != count+1 {
		t.Fatalf("records were not preserved after restart; err=%v", err)
	}
}

func TestOpenRejectsIncompleteOrInvalidExistingFile(t *testing.T) {
	for name, content := range map[string]string{
		"incomplete": `{"contract_version":"v1"}`,
		"invalid":    "{not-json}\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := testDataDir(t)
			if err := os.WriteFile(filepath.Join(dir, "audit.jsonl"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if store, err := OpenJSONL(dir); err == nil {
				_ = store.Close()
				t.Fatal("expected existing audit file to be rejected")
			}
			if _, err := os.Stat(filepath.Join(dir, "audit.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("startup failure should release the lock it acquired, stat error = %v", err)
			}
		})
	}
}

func TestOnlyOneStoreCanOwnDataDirectory(t *testing.T) {
	dir := testDataDir(t)
	first, err := OpenJSONL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := OpenJSONL(dir); err == nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("second store unexpectedly acquired the same data directory")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := OpenJSONL(dir)
	if err != nil {
		t.Fatalf("store could not be reopened after owner closed: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

type injectedDurableFile struct {
	writeErr error
	syncErr  error
}

func (f *injectedDurableFile) Write(data []byte) (int, error) {
	if f.writeErr != nil {
		return len(data) / 2, f.writeErr
	}
	return len(data), nil
}

func (f *injectedDurableFile) Sync() error  { return f.syncErr }
func (f *injectedDurableFile) Close() error { return nil }

func TestPartialWriteAndSyncFailureNeverReportSuccess(t *testing.T) {
	for name, file := range map[string]*injectedDurableFile{
		"partial write": {writeErr: io.ErrShortWrite},
		"sync failure":  {syncErr: errors.New("simulated sync failure")},
	} {
		t.Run(name, func(t *testing.T) {
			store := &JSONLStore{file: file}
			submission := testSubmission("evt.failure-01")
			event := Event{
				ContractVersion: submission.ContractVersion, EventID: submission.EventID, TenantID: submission.TenantID,
				ServiceID: submission.ServiceID, Actor: submission.Actor, Action: submission.Action, Resource: submission.Resource,
				Result: submission.Result, CorrelationID: submission.CorrelationID, OccurredAt: submission.OccurredAt,
				ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
			if err := store.Append(context.Background(), event); err == nil {
				t.Fatal("write or sync failure was reported as success")
			}
			event.EventID = "evt.failure-02"
			if err := store.Append(context.Background(), event); !errors.Is(err, ErrStoreUnavailable) {
				t.Fatalf("poisoned store accepted another write: %v", err)
			}
		})
	}
}

func TestCloseFailureKeepsLockFile(t *testing.T) {
	dir := testDataDir(t)
	store, err := OpenJSONL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err == nil {
		t.Fatal("expected an error closing the already-closed data file")
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.lock")); err != nil {
		t.Fatalf("lock should remain after uncertain file close: %v", err)
	}
}

func TestExistingLockIsPreservedAndBlocksOpen(t *testing.T) {
	dir := testDataDir(t)
	lockPath := filepath.Join(dir, "audit.lock")
	if err := os.WriteFile(lockPath, []byte("stale test lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenJSONL(dir); err == nil {
		_ = store.Close()
		t.Fatal("open unexpectedly accepted an existing lock")
	}
	contents, err := os.ReadFile(lockPath)
	if err != nil || string(contents) != "stale test lock\n" {
		t.Fatalf("existing lock was modified or removed; contents=%q err=%v", contents, err)
	}
}
