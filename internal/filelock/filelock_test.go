package filelock

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestAcquireSerializesAndReleaseRemovesOnlyItsOwnLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resource.lock")
	release, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, path); err == nil {
		t.Fatal("second owner acquired a live lock")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement := []byte(strconv.Itoa(os.Getpid()) + " replacement-token\n")
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	release()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(replacement) {
		t.Fatalf("old owner removed a replacement lock: raw=%q err=%v", raw, err)
	}
}

func TestAcquireReclaimsDeadOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resource.lock")
	if err := os.WriteFile(path, []byte("2147483647 abandoned-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("released lock still exists: %v", err)
	}
}
