package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const maxStoredLineBytes = 16 << 10

// JSONLStore is a single-process append-only local store. A failed write or
// sync poisons the instance so later records cannot be appended after damage.
type JSONLStore struct {
	mu       sync.Mutex
	file     durableFile
	lock     *os.File
	lockPath string
	poison   error
	closed   bool
	// index holds one stored event per identity so retries are idempotent.
	// It is rebuilt from the whole file on open (memory grows with the file);
	// if historical duplicates exist, the first record is kept.
	index map[eventKey]Event
}

type durableFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

func OpenJSONL(dataDir string) (*JSONLStore, error) {
	if dataDir == "" {
		return nil, errors.New("audit data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create audit data directory: %w", err)
	}
	path := filepath.Join(dataDir, "audit.jsonl")
	lockPath := filepath.Join(dataDir, "audit.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit data directory is already in use or has a stale lock: %w", err)
	}
	if _, err := fmt.Fprintf(lock, "pid=%d\n", os.Getpid()); err != nil {
		_ = lock.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("write audit lock: %w", err)
	}
	if err := lock.Sync(); err != nil {
		_ = lock.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("sync audit lock: %w", err)
	}
	index, err := validateExistingFile(path)
	if err != nil {
		_ = lock.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("existing audit file rejected: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = lock.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	return &JSONLStore{file: file, lock: lock, lockPath: lockPath, index: index}, nil
}

func validateExistingFile(path string) (map[eventKey]Event, error) {
	index := make(map[eventKey]Event)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := file.ReadAt(last, info.Size()-1); err != nil {
			return nil, err
		}
		if last[0] != '\n' {
			return nil, errors.New("last record is incomplete")
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), maxStoredLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		event, ok := parseStoredLine(bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'}))
		if !ok {
			return nil, fmt.Errorf("line %d is invalid", lineNumber)
		}
		if _, exists := index[keyOf(event)]; !exists {
			index[keyOf(event)] = event
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("audit line exceeds size limit or cannot be read: %w", err)
	}
	return index, nil
}

func parseStoredLine(line []byte) (Event, bool) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var event Event
	if decoder.Decode(&event) != nil || ValidateEvent(event) != nil {
		return Event{}, false
	}
	var trailing any
	return event, decoder.Decode(&trailing) == io.EOF
}

func (s *JSONLStore) Append(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateEvent(event); err != nil {
		return err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if len(line) > maxStoredLineBytes {
		return ErrInvalidEvent
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.file == nil {
		return ErrStoreUnavailable
	}
	if s.poison != nil {
		return ErrStoreUnavailable
	}
	if stored, exists := s.index[keyOf(event)]; exists {
		if sameContent(stored, event) {
			return &DuplicateError{Stored: stored}
		}
		return ErrEventConflict
	}
	n, err := s.file.Write(line)
	if err != nil || n != len(line) {
		if err == nil {
			err = io.ErrShortWrite
		}
		s.poison = err
		return err
	}
	if err := s.file.Sync(); err != nil {
		s.poison = err
		return err
	}
	if s.index == nil {
		s.index = make(map[eventKey]Event)
	}
	s.index[keyOf(event)] = event
	return nil
}

// Ready reports whether the store is open and has not been poisoned by a
// failed write or sync.
func (s *JSONLStore) Ready(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.file == nil || s.poison != nil {
		return ErrStoreUnavailable
	}
	return nil
}

func (s *JSONLStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.file == nil {
		return nil
	}
	s.closed = true
	fileErr := s.file.Close()
	var lockErr error
	if s.lock != nil {
		lockErr = s.lock.Close()
	}
	if fileErr != nil || lockErr != nil {
		// Keep the lock file if either descriptor failed to close. Releasing
		// ownership in an uncertain state could let another process append.
		if fileErr != nil {
			return fileErr
		}
		return lockErr
	}
	removeErr := os.Remove(s.lockPath)
	return removeErr
}
