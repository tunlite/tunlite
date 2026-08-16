package configstore

import (
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
		APIVersion: core.APIVersion,
		Kind:       core.Kind,
		Metadata:   core.Metadata{Name: name},
		Spec:       core.ConnectionSpec{Driver: "example.test/fake", Lifecycle: "persistent", ConfigVersion: 1, Config: config},
	}
}
