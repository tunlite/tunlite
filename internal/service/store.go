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
	"time"
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
		return Record{APIVersion: RecordVersion, Connection: connection, DesiredState: DesiredDisabled}, false, nil
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
	if record.APIVersion == "" {
		record.APIVersion = RecordVersion
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

func (s *Store) Lock(ctx context.Context, connection string) (func(), error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	path := s.path(connection) + ".lock"
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Close()
			return func() {
				_ = os.Remove(path)
				cancel()
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			cancel()
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		select {
		case <-lockCtx.Done():
			cancel()
			return nil, errors.New("connection service is busy; another Tunlite command may still be running")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *Store) path(connection string) string {
	return filepath.Join(s.root, connection+".json")
}

func validateRecord(record Record, connection string) error {
	if record.APIVersion != RecordVersion {
		return fmt.Errorf("unsupported service state apiVersion %q", record.APIVersion)
	}
	if record.Connection == "" || record.Connection != connection {
		return errors.New("service state connection does not match its file")
	}
	if record.DesiredState != DesiredEnabled && record.DesiredState != DesiredDisabled {
		return fmt.Errorf("unsupported desired state %q", record.DesiredState)
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
