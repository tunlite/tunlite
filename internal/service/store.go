package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/filelock"
	"github.com/yuanyuanzijin/tunlite/internal/processlock"
)

type Store struct {
	root string
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) Load(connection string) (Record, bool, error) {
	raw, err := os.ReadFile(s.path(connection))
	if errors.Is(err, os.ErrNotExist) {
		return Record{RecordRevision: CurrentRecordRevision, Connection: connection, Mode: ModeStopped}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, false, fmt.Errorf("decode service state for %s: %w", connection, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("more than one JSON value")
		}
		return Record{}, false, fmt.Errorf("decode service state for %s: %w", connection, err)
	}
	if err := validateRecord(record, connection); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func (s *Store) Write(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.RecordRevision == 0 {
		record.RecordRevision = CurrentRecordRevision
	}
	if err := validateRecord(record, record.Connection); err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create service state directory: %w", err)
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return replaceFile(s.path(record.Connection), raw, 0o600)
}

func (s *Store) Remove(connection string) error {
	err := os.Remove(s.path(connection))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// FindBinding reports whether another managed Connection already controls the
// same native service. A native service must have exactly one Tunlite owner.
func (s *Store) FindBinding(reference Reference, exceptConnection string) (Record, bool, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		connection := strings.TrimSuffix(entry.Name(), ".json")
		if connection == exceptConnection {
			continue
		}
		record, exists, err := s.Load(connection)
		if err != nil {
			return Record{}, false, err
		}
		if exists && sameNativeService(record.Reference(), reference) {
			return record, true, nil
		}
	}
	return Record{}, false, nil
}

func sameNativeService(left, right Reference) bool {
	if left.Adapter != right.Adapter {
		return false
	}
	if left.UnitName != "" && right.UnitName != "" {
		return left.UnitName == right.UnitName
	}
	return left.ArtifactPath != "" && right.ArtifactPath != "" && filepath.Clean(left.ArtifactPath) == filepath.Clean(right.ArtifactPath)
}

func (s *Store) Lock(ctx context.Context, connection string) (func(), error) {
	return s.lock(ctx, s.path(connection)+".lock", "connection service is busy; another Tunlite command may still be running")
}

// LockBindings serializes changes to the native-service ownership index. It
// is a short-lived file lock used only by mutating CLI commands; it is not a
// daemon or a runtime dependency of any Connection.
func (s *Store) LockBindings(ctx context.Context) (func(), error) {
	return s.lock(ctx, filepath.Join(s.root, ".bindings.lock"), "native service bindings are busy; another Tunlite command may still be running")
}

func (s *Store) lock(ctx context.Context, path, busyMessage string) (func(), error) {
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	release, err := filelock.Acquire(lockCtx, path)
	if err != nil {
		cancel()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, errors.New(busyMessage)
		}
		return nil, err
	}
	return func() {
		release()
		cancel()
	}, nil
}

func (s *Store) LockRun(ctx context.Context, connection string) (func(), error) {
	release, err := processlock.Acquire(ctx, s.runLockPath(connection))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, errors.New("connection is already running in the foreground")
		}
		return nil, err
	}
	return release, nil
}

func (s *Store) RunActive(connection string) bool {
	return processlock.Active(s.runLockPath(connection))
}

func (s *Store) path(connection string) string {
	return filepath.Join(s.root, connection+".json")
}

func (s *Store) runLockPath(connection string) string {
	return filepath.Join(s.root, connection+".run.lock")
}

func validateRecord(record Record, connection string) error {
	if record.RecordRevision != CurrentRecordRevision {
		return fmt.Errorf("unsupported service record revision %d", record.RecordRevision)
	}
	if record.Connection == "" || record.Connection != connection {
		return errors.New("service state connection does not match its file")
	}
	if record.Mode != ModeStopped && record.Mode != ModeBackground && record.Mode != ModePersistent {
		return fmt.Errorf("unsupported service mode %q", record.Mode)
	}
	return nil
}

func replaceFile(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tunlite-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleaned := false
	defer func() {
		_ = temporary.Close()
		if !cleaned {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	cleaned = true
	return nil
}
