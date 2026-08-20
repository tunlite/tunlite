package paths

import (
	"path/filepath"
	"testing"
)

func TestConfigFileUsesTunliteHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TUNLITE_HOME", root)
	got, err := ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "config", "connections.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStateDirUsesTunliteHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TUNLITE_HOME", root)
	got, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "state", "core")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLegacyPathsRemainOutsideV1Namespace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TUNLITE_HOME", root)
	config, err := LegacyConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	state, err := LegacyStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if config != filepath.Join(root, "config", "config.json") {
		t.Fatalf("legacy config=%q", config)
	}
	if state != filepath.Join(root, "data") {
		t.Fatalf("legacy state=%q", state)
	}
}
