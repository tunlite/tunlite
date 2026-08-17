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
	want := filepath.Join(root, "config", "config.json")
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
	want := filepath.Join(root, "state")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
