package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
)

const Kind = "ConnectionList"

var ErrChanged = errors.New("config changed during operation")

type Document struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Items      []core.Connection `json:"items"`
}

type LegacyConfigError struct {
	Path string
}

func (e *LegacyConfigError) Error() string {
	return fmt.Sprintf("legacy Tunlite 0.x config detected at %s; migrate it before using Go v1", e.Path)
}

type Store struct {
	path string
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string { return s.path }

func Empty() Document {
	return Document{APIVersion: core.APIVersion, Kind: Kind, Items: []core.Connection{}}
}

func (s *Store) Load() (Document, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Empty(), nil
	}
	if err != nil {
		return Document{}, fmt.Errorf("read config %s: %w", s.path, err)
	}
	var probe struct {
		Version json.RawMessage   `json:"version"`
		Kind    string            `json:"kind"`
		Tunnels []json.RawMessage `json:"tunnels"`
	}
	if json.Unmarshal(raw, &probe) == nil && probe.Kind == "" && (probe.Version != nil || probe.Tunnels != nil) {
		return Document{}, &LegacyConfigError{Path: s.path}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decode config %s: %w", s.path, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Document{}, fmt.Errorf("decode config %s: %w", s.path, err)
	}
	if err := validateDocument(document); err != nil {
		return Document{}, fmt.Errorf("validate config %s: %w", s.path, err)
	}
	return document, nil
}

func (s *Store) Update(ctx context.Context, change func(*Document) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err := s.acquire(lockCtx)
	if err != nil {
		return err
	}
	defer release()
	document, err := s.Load()
	if err != nil {
		return err
	}
	if err := change(&document); err != nil {
		return err
	}
	return s.save(document)
}

func (s *Store) Write(ctx context.Context, document Document, overwrite bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err := s.acquire(lockCtx)
	if err != nil {
		return err
	}
	defer release()
	if !overwrite {
		if _, err := os.Stat(s.path); err == nil {
			return fmt.Errorf("config file already exists: %s", s.path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.save(document)
}

func (s *Store) save(document Document) error {
	raw, err := Encode(document)
	if err != nil {
		return err
	}
	return s.saveRaw(raw)
}

func Encode(document Document) ([]byte, error) {
	if err := validateDocument(document); err != nil {
		return nil, err
	}
	items := make([]core.Connection, len(document.Items))
	copy(items, document.Items)
	document.Items = items
	sort.Slice(document.Items, func(i, j int) bool {
		return document.Items[i].Metadata.Name < document.Items[j].Metadata.Name
	})
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return buffer.Bytes(), nil
}

func (s *Store) ReplaceRawIfUnchanged(ctx context.Context, expected, replacement []byte, prepare func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err := s.acquire(lockCtx)
	if err != nil {
		return err
	}
	defer release()
	current, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", s.path, err)
	}
	if !bytes.Equal(current, expected) {
		return ErrChanged
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			return err
		}
	}
	return s.saveRaw(replacement)
}

func (s *Store) saveRaw(raw []byte) error {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	cleaned := false
	defer func() {
		_ = temporary.Close()
		if !cleaned {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary config: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	cleaned = true
	return nil
}

func (s *Store) acquire(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	lockPath := s.path + ".lock"
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock config: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("config is busy; another Tunlite command may still be running")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func validateDocument(document Document) error {
	if document.APIVersion != core.APIVersion {
		return fmt.Errorf("unsupported apiVersion %q", document.APIVersion)
	}
	if document.Kind != Kind {
		return fmt.Errorf("unsupported kind %q", document.Kind)
	}
	if document.Items == nil {
		return errors.New("items must be an array")
	}
	seen := make(map[string]struct{}, len(document.Items))
	for _, connection := range document.Items {
		if err := core.ValidateConnection(connection); err != nil {
			return err
		}
		name := connection.Metadata.Name
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate connection name %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("config contains more than one JSON value")
}

func Find(document *Document, name string) (int, *core.Connection) {
	for i := range document.Items {
		if document.Items[i].Metadata.Name == name {
			return i, &document.Items[i]
		}
	}
	return -1, nil
}
