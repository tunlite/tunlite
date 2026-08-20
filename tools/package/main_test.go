package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareOutputDirectoryCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dist")
	if err := prepareOutputDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("output directory was not created: info=%#v err=%v", info, err)
	}
}

func TestPrepareOutputDirectoryAcceptsEmptyDirectory(t *testing.T) {
	if err := prepareOutputDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareOutputDirectoryRejectsStaleArtifacts(t *testing.T) {
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "stale.tar.gz"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := prepareOutputDirectory(path)
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("expected stale output rejection, got %v", err)
	}
}

func TestPrepareOutputDirectoryRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dist")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := prepareOutputDirectory(path)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected file output rejection, got %v", err)
	}
}
