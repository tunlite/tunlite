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
	"github.com/yuanyuanzijin/tunlite/internal/filelock"
)

const Kind = "ConnectionList"

var (
	ErrChanged = errors.New("config changed during operation")
	ErrExists  = errors.New("config file already exists")
)

type Document struct {
	ConfigVersion int               `json:"configVersion"`
	Kind          string            `json:"kind"`
	Items         []core.Connection `json:"items"`
}

func (d *Document) UnmarshalJSON(raw []byte) error {
	var value struct {
		ConfigVersion int               `json:"configVersion"`
		Kind          string            `json:"kind"`
		Items         []core.Connection `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := ensureEOF(decoder); err != nil {
		return err
	}
	d.ConfigVersion = value.ConfigVersion
	d.Kind = value.Kind
	d.Items = value.Items
	for index := range d.Items {
		if d.Items[index].ConfigVersion == 0 {
			d.Items[index].ConfigVersion = d.ConfigVersion
		}
	}
	return nil
}

type UnsupportedConfigVersionError struct {
	Found int
}

func (e *UnsupportedConfigVersionError) Error() string {
	return fmt.Sprintf("unsupported configVersion %d; use a compatible Tunlite Core", e.Found)
}

type Store struct {
	path string
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string { return s.path }

func Empty() Document {
	return Document{ConfigVersion: core.ConfigVersion, Kind: Kind, Items: []core.Connection{}}
}

func (s *Store) Load() (Document, error) {
	document, _, err := s.loadWithRaw()
	return document, err
}

func (s *Store) loadWithRaw() (Document, []byte, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Empty(), nil, nil
	}
	if err != nil {
		return Document{}, nil, fmt.Errorf("read config %s: %w", s.path, err)
	}
	var versionProbe struct {
		ConfigVersion *int `json:"configVersion"`
	}
	if err := json.Unmarshal(raw, &versionProbe); err != nil {
		return Document{}, raw, fmt.Errorf("decode config %s: %w", s.path, err)
	}
	if versionProbe.ConfigVersion != nil && *versionProbe.ConfigVersion != core.ConfigVersion {
		return Document{}, raw, &UnsupportedConfigVersionError{Found: *versionProbe.ConfigVersion}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, raw, fmt.Errorf("decode config %s: %w", s.path, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Document{}, raw, fmt.Errorf("decode config %s: %w", s.path, err)
	}
	if err := validateDocument(document); err != nil {
		return Document{}, raw, fmt.Errorf("validate config %s: %w", s.path, err)
	}
	return document, raw, nil
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
	document, _, err := s.loadWithRaw()
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
			return fmt.Errorf("%w: %s", ErrExists, s.path)
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
	release, err := filelock.Acquire(ctx, s.path+".lock")
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, errors.New("config is busy; another Tunlite command may still be running")
		}
		return nil, fmt.Errorf("lock config: %w", err)
	}
	return release, nil
}

func validateDocument(document Document) error {
	if document.ConfigVersion != core.ConfigVersion {
		return fmt.Errorf("unsupported configVersion %d", document.ConfigVersion)
	}
	if document.Kind != Kind {
		return fmt.Errorf("unsupported kind %q", document.Kind)
	}
	if document.Items == nil {
		return errors.New("items must be an array")
	}
	seen := make(map[string]struct{}, len(document.Items))
	for _, connection := range document.Items {
		if connection.ConfigVersion != document.ConfigVersion {
			return fmt.Errorf("connection %q configVersion %d does not match ConnectionList configVersion %d", connection.Metadata.Name, connection.ConfigVersion, document.ConfigVersion)
		}
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
