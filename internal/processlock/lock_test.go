package processlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActiveTracksOnlyTheLiveHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.lock")
	if Active(path) {
		t.Fatal("new lock was active")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only Active created a lock file: %v", err)
	}
	release, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !Active(path) {
		t.Fatal("held lock was not active")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, path); err == nil {
		t.Fatal("second holder acquired a live process lock")
	}
	release()
	if Active(path) {
		t.Fatal("released lock remained active")
	}
}
