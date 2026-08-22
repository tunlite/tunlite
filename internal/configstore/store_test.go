package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/core"
)

func TestStoreSerializesConcurrentUpdates(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "config", "config.json"))
	const count = 8
	var group sync.WaitGroup
	errorsFound := make(chan error, count)
	for i := 0; i < count; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			err := store.Update(context.Background(), func(document *Document) error {
				document.Items = append(document.Items, testConnection(string(rune('a'+i))))
				return nil
			})
			if err != nil {
				errorsFound <- err
			}
		}(i)
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Items) != count {
		t.Fatalf("got %d connections, want %d", len(document.Items), count)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions are too broad: %o", info.Mode().Perm())
	}
}

func TestConnectionListWritesConfigVersionOnlyAtDocumentRoot(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "config.json"))
	if err := store.Update(context.Background(), func(document *Document) error {
		document.Items = append(document.Items, testConnection("one"), testConnection("two"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if count := bytes.Count(raw, []byte(`"configVersion"`)); count != 1 {
		t.Fatalf("configVersion was written %d times, want root only:\n%s", count, raw)
	}
}

func TestStoreRejectsLegacyConfigWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"version":1,"tunnels":[]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(path)
	_, err := store.Load()
	var legacy *LegacyConfigError
	if !errors.As(err, &legacy) {
		t.Fatalf("got %v, want LegacyConfigError", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("legacy config was changed")
	}
}

func TestPreviewEnvelopeIsReadOnlyUntilFirstSuccessfulWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"apiVersion":"tunlite.dev/v1","kind":"ConnectionList","items":[{"apiVersion":"tunlite.dev/v1","kind":"Connection","metadata":{"name":"db"},"spec":{"driver":"example.test/fake","configVersion":1,"config":{"value":"old"}}}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(path)
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if document.ConfigVersion != core.ConfigVersion || len(document.Items) != 1 {
		t.Fatalf("preview was not converted in memory: %#v", document)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, raw) {
		t.Fatalf("read-only load changed preview data: raw=%q err=%v", unchanged, err)
	}
	wantFailure := errors.New("do not apply")
	if err := store.Update(context.Background(), func(document *Document) error {
		document.Items[0].Metadata.Name = "not-saved"
		return wantFailure
	}); !errors.Is(err, wantFailure) {
		t.Fatalf("failed preview update returned %v", err)
	}
	unchanged, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, raw) {
		t.Fatalf("failed update changed preview data: raw=%q err=%v", unchanged, err)
	}
	if backups, err := filepath.Glob(path + ".go-preview-sha256-*.bak"); err != nil || len(backups) != 0 {
		t.Fatalf("failed update created preview backups: %v err=%v", backups, err)
	}
	if err := store.Update(context.Background(), func(document *Document) error {
		document.Items[0].Metadata.Group = "migrated"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	migrated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(migrated, []byte(`"configVersion": 1`)) || bytes.Contains(migrated, []byte(`"apiVersion"`)) || bytes.Contains(migrated, []byte(`"spec": {\n        "driver": "example.test/fake",\n        "configVersion"`)) {
		t.Fatalf("unexpected migrated envelope:\n%s", migrated)
	}
	backups, err := filepath.Glob(path + ".go-preview-sha256-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("preview backup count=%d err=%v", len(backups), err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(backup, raw) {
		t.Fatalf("preview backup was not byte-for-byte: err=%v", err)
	}
}

func TestStoreRejectsHigherAndMixedConfigVersionsBeforeUse(t *testing.T) {
	root := t.TempDir()
	higherPath := filepath.Join(root, "higher.json")
	if err := os.WriteFile(higherPath, []byte(`{"configVersion":2,"kind":"ConnectionList","items":"not interpreted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(higherPath).Load()
	var unsupported *UnsupportedConfigVersionError
	if !errors.As(err, &unsupported) || unsupported.Found != 2 {
		t.Fatalf("higher version error=%v", err)
	}
	higherRaw, readErr := os.ReadFile(higherPath)
	if readErr != nil || !bytes.Equal(higherRaw, []byte(`{"configVersion":2,"kind":"ConnectionList","items":"not interpreted"}`)) {
		t.Fatalf("higher version input changed: raw=%q err=%v", higherRaw, readErr)
	}

	mixedPath := filepath.Join(root, "mixed.json")
	mixed := []byte(`{"configVersion":1,"kind":"ConnectionList","items":[{"apiVersion":"tunlite.dev/v1","kind":"Connection","metadata":{"name":"db"},"spec":{"driver":"example.test/fake","configVersion":1,"config":{}}}]}`)
	if err := os.WriteFile(mixedPath, mixed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(mixedPath).Load(); err == nil {
		t.Fatal("mixed config envelopes were accepted")
	}
}

func TestFailedUpdateLeavesConfigUntouched(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "config.json"))
	if err := store.Update(context.Background(), func(document *Document) error {
		document.Items = append(document.Items, testConnection("keep"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("stop")
	if err := store.Update(context.Background(), func(document *Document) error {
		document.Items = nil
		return want
	}); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed update changed the config")
	}
}

func testConnection(name string) core.Connection {
	config, _ := json.Marshal(map[string]any{"host": "me@example.com", "forwards": []any{map[string]any{"type": "dynamic", "srcPort": 1080}}})
	return core.Connection{
		ConfigVersion: core.ConfigVersion,
		Kind:          core.Kind,
		Metadata:      core.Metadata{Name: name},
		Spec:          core.ConnectionSpec{Driver: "example.test/fake", Config: config},
	}
}
