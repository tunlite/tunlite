//go:build !windows

package cli

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tunlite/tunlite/internal/configstore"
	"github.com/tunlite/tunlite/internal/service"
)

// The native manager is external to the CLI. Serialize its test substitute
// and copy observations as a real manager would, so refreshes can run while
// an operation is in flight without sharing mutable observation maps.
type monitorTestAdapter struct {
	*fakeServiceAdapter
	mu sync.Mutex
}

func (a *monitorTestAdapter) Inspect(ctx context.Context, ref service.Reference) (service.Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	observation, err := a.fakeServiceAdapter.Inspect(ctx, ref)
	observation.Platform = maps.Clone(observation.Platform)
	return observation, err
}

func (a *monitorTestAdapter) Restart(ctx context.Context, ref service.Reference) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fakeServiceAdapter.Restart(ctx, ref)
}

func (a *monitorTestAdapter) Start(ctx context.Context, ref service.Reference) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fakeServiceAdapter.Start(ctx, ref)
}

func (a *monitorTestAdapter) Stop(ctx context.Context, ref service.Reference) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fakeServiceAdapter.Stop(ctx, ref)
}

func runTestMonitor(t *testing.T, ctx context.Context, adapter service.Adapter, interval string) (*io.PipeWriter, *lockedBuffer, <-chan error) {
	t.Helper()
	input, writer := io.Pipe()
	output := &lockedBuffer{}
	terminal := true
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer func() {
			if panicValue := recover(); panicValue != nil {
				done <- fmt.Errorf("monitor panicked: %v", panicValue)
			}
		}()
		budget := lifecycleObservationBudget{Overall: time.Millisecond, PerItem: time.Millisecond, PollInterval: time.Millisecond}
		done <- monitor(ctx, []string{"--interval", interval}, IO{In: input, Out: output, Err: output, Terminal: &terminal}, options{
			serviceAdapter: func() (service.Adapter, error) { return adapter, nil }, lifecycleBudget: &budget,
		})
	}()
	t.Cleanup(func() { cancel(); input.Close(); writer.Close(); workers.Wait() })
	waitForMonitorOutput(t, output, 0, "connections 2 total")
	return writer, output, done
}

func TestMonitorConfirmationStaysBoundAcrossRefresh(t *testing.T) {
	for _, change := range []string{"reorder", "disappearance", "empty", "lifecycle", "identity", "identity before refresh"} {
		t.Run(change, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			fake := newFakeServiceAdapter(filepath.Join(home, "native"))
			adapter := &monitorTestAdapter{fakeServiceAdapter: fake}
			invoke := useFakeServiceAdapter(t, fake)
			for _, name := range []string{"db", "other"} {
				for _, args := range [][]string{{"add", name, "-R", "1080:localhost:1080", "me@example.com"}, {"start", name}} {
					if code, out, stderr := invoke(t, args...); code != 0 {
						t.Fatalf("setup %v: %d %s %s", args, code, out, stderr)
					}
				}
			}
			interval := "0.25"
			if change == "identity before refresh" {
				interval = "60"
			}
			writer, output, done := runTestMonitor(t, context.Background(), adapter, interval)
			writer.Write([]byte("r"))
			waitForMonitorOutput(t, output, 0, `Restart "db"?`)
			offset := len(output.String())
			store := configstore.New(filepath.Join(home, "config", "connections.json"))
			if err := store.Update(context.Background(), func(document *configstore.Document) error {
				switch change {
				case "reorder":
					document.Items[1].Metadata.Group = "first"
				case "disappearance":
					document.Items = document.Items[1:]
				case "empty":
					document.Items = document.Items[:0]
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if change == "lifecycle" || change == "identity" || change == "identity before refresh" {
				records, _ := serviceRecordStore()
				record, _, err := records.Load("db")
				if err != nil {
					t.Fatal(err)
				}
				if change == "lifecycle" {
					record.Mode = service.ModePersistent
				} else {
					record.UnitName = "a-different-native-identity"
				}
				if err := records.Write(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			if change == "reorder" {
				waitForMonitorOutput(t, output, offset, "Group first")
				writer.Write([]byte("y"))
				waitForMonitorOutput(t, output, offset, "restarted db")
			} else {
				if change == "identity before refresh" {
					writer.Write([]byte("y"))
				}
				waitForMonitorOutput(t, output, offset, "operation cancelled")
				writer.Write([]byte("y"))
			}
			writer.Write([]byte("q"))
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			adapter.mu.Lock()
			defer adapter.mu.Unlock()
			if change == "reorder" {
				if len(fake.restartCalls) != 1 || fake.restartCalls[0] != "db" {
					t.Fatalf("confirmed db but restarted %v", fake.restartCalls)
				}
			} else if len(fake.restartCalls) != 0 {
				t.Fatalf("invalidated confirmation restarted %v", fake.restartCalls)
			}
		})
	}
}

type blockedMonitorStart struct {
	*monitorTestAdapter
	nativeStarted chan struct{}
	returnNative  chan struct{}
	compensated   chan struct{}
}

func (a *blockedMonitorStart) Start(ctx context.Context, ref service.Reference) error {
	err := a.monitorTestAdapter.Start(ctx, ref)
	close(a.nativeStarted)
	<-a.returnNative // Native success has happened; record commit is next.
	return err
}

func (a *blockedMonitorStart) Stop(ctx context.Context, ref service.Reference) error {
	err := a.monitorTestAdapter.Stop(ctx, ref)
	close(a.compensated)
	return err
}

func TestMonitorExitWaitsForNativeTransactionCompensation(t *testing.T) {
	for _, exit := range []string{"quit", "escape", "context"} {
		t.Run(exit, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			fake := newFakeServiceAdapter(filepath.Join(home, "native"))
			invoke := useFakeServiceAdapter(t, fake)
			for _, name := range []string{"db", "other"} {
				if code, out, stderr := invoke(t, "add", name, "-R", "1080:localhost:1080", "me@example.com"); code != 0 {
					t.Fatalf("setup: %d %s %s", code, out, stderr)
				}
			}
			adapter := &blockedMonitorStart{monitorTestAdapter: &monitorTestAdapter{fakeServiceAdapter: fake},
				nativeStarted: make(chan struct{}), returnNative: make(chan struct{}), compensated: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(adapter.returnNative) }) }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer, output, done := runTestMonitor(t, ctx, adapter, "60")
			t.Cleanup(release)
			writer.Write([]byte("s"))
			select {
			case <-adapter.nativeStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("native start did not run")
			}
			switch exit {
			case "quit":
				writer.Write([]byte("q"))
			case "escape":
				writer.Write([]byte{0x1b})
			case "context":
				cancel()
			}
			returned := false
			select {
			case err := <-done:
				returned = true
				t.Errorf("monitor returned before native transaction settled: %v", err)
			case <-time.After(100 * time.Millisecond): // Includes standalone Escape's 35ms decoding window.
			}
			release()
			if !returned {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("monitor did not finish compensation")
				}
			}
			select {
			case <-adapter.compensated:
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled record commit did not compensate native start")
			}
			records, _ := serviceRecordStore()
			record, _, err := records.Load("db")
			observation, inspectErr := adapter.Inspect(context.Background(), record.Reference())
			if err != nil || inspectErr != nil || record.Mode != service.ModeStopped || observation.ProcessState != service.ProcessInactive {
				t.Fatalf("exit left a partial transaction: record=%+v native=%+v errors=%v/%v", record, observation, err, inspectErr)
			}
			waitForMonitorOutput(t, output, 0, "\x1b[?1049l")
		})
	}
}
