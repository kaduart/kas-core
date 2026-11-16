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
	if err := validateExistingFile(path); err != nil {
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
	return &JSONLStore{file: file, lock: lock, lockPath: lockPath}, nil
}

func validateExistingFile(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := file.ReadAt(last, info.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			return errors.New("last record is incomplete")
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), maxStoredLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if !validStoredLine(bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'})) {
			return fmt.Errorf("line %d is invalid", lineNumber)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("audit line exceeds size limit or cannot be read: %w", err)
	}
	return nil
}

func validStoredLine(line []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var event Event
	if decoder.Decode(&event) != nil || ValidateEvent(event) != nil {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
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
