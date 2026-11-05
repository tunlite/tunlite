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

func TestSimultaneousStaleReapersSerializeWithOwnerCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resource.lock")
	if err := os.WriteFile(path, []byte("2147483647 abandoned-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Hold the independent OS guard so both contenders encounter the same
	// stale generation. Neither may inspect/remove/create an owner until it
	// holds this guard; a compare/unlink scheme alone enters prematurely.
	unlock := holdTestGuard(t, path+".guard")
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		release func()
		err     error
	}
	acquired := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			release, err := Acquire(ctx, path)
			acquired <- result{release, err}
		}()
	}
	close(start)
	select {
	case got := <-acquired:
		if got.release != nil {
			got.release()
		}
		cancel()
		other := <-acquired
		if other.release != nil {
			other.release()
		}
		t.Fatalf("stale reaper crossed a locked ownership guard: %v", got.err)
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	first := <-acquired
	if first.err != nil {
		t.Fatal(first.err)
	}
	defer first.release()
	owner, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-acquired:
		if second.release != nil {
			second.release()
		}
		t.Fatalf("second stale reaper removed a live owner: %v", second.err)
	case <-time.After(50 * time.Millisecond):
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(owner) {
		t.Fatalf("live owner changed: %q -> %q, %v", owner, current, err)
	}
	first.release()
	second := <-acquired
	if second.err != nil {
		t.Fatal(second.err)
	}
	second.release()
}
