package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

type delayedInspectAdapter struct {
	*fakeServiceAdapter
	delay  time.Duration
	active atomic.Int32
	max    atomic.Int32
}

func (a *delayedInspectAdapter) Inspect(ctx context.Context, reference service.Reference) (service.Observation, error) {
	active := a.active.Add(1)
	defer a.active.Add(-1)
	for {
		maximum := a.max.Load()
		if active <= maximum || a.max.CompareAndSwap(maximum, active) {
			break
		}
	}
	timer := time.NewTimer(a.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return service.Observation{}, ctx.Err()
	case <-timer.C:
		return a.fakeServiceAdapter.Inspect(ctx, reference)
	}
}

func statusTestConnections(count int) []core.Connection {
	connections := make([]core.Connection, 0, count)
	for index := 0; index < count; index++ {
		connections = append(connections, core.Connection{
			ConfigVersion: core.ConfigVersion,
			Kind:          core.Kind,
			Metadata:      core.Metadata{Name: fmt.Sprintf("connection-%03d", index)},
			Spec: core.ConnectionSpec{
				Driver: "io.tunlite.ssh",
				Config: json.RawMessage(fmt.Sprintf(
					`{"host":"example.com","forwards":[{"type":"dynamic","bind":"127.0.0.1","srcPort":%d}]}`,
					10000+index,
				)),
			},
		})
	}
	return connections
}

func TestStatusObserverUsesBoundedConcurrencyForOneHundredConnections(t *testing.T) {
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	root := t.TempDir()
	adapter := &delayedInspectAdapter{
		fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(root, "native")),
		delay:              2 * time.Millisecond,
	}
	connections := statusTestConnections(100)
	results := collectStatusResults(
		context.Background(), adapter, service.NewStore(filepath.Join(root, "records")),
		connections, filepath.Join(root, "connections.json"),
		statusObservationBudget{Overall: 2 * time.Second, PerItem: 200 * time.Millisecond, Concurrent: 8},
	)
	if len(results) != 100 {
		t.Fatalf("result count=%d", len(results))
	}
	for _, result := range results {
		if result.Error != nil || result.State == nil {
			t.Fatalf("unexpected status result: %#v", result)
		}
	}
	if maximum := adapter.max.Load(); maximum < 2 || maximum > 8 {
		t.Fatalf("maximum concurrent observations=%d, want 2..8", maximum)
	}
}

func TestStatusObserverReturnsBlockedUnknownWhenBudgetExpires(t *testing.T) {
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	root := t.TempDir()
	adapter := &delayedInspectAdapter{
		fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(root, "native")),
		delay:              time.Second,
	}
	connections := statusTestConnections(10)
	started := time.Now()
	results := collectStatusResults(
		context.Background(), adapter, service.NewStore(filepath.Join(root, "records")),
		connections, filepath.Join(root, "connections.json"),
		statusObservationBudget{Overall: 80 * time.Millisecond, PerItem: 30 * time.Millisecond, Concurrent: 2},
	)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("observer exceeded its overall budget: %s", elapsed)
	}
	for _, result := range results {
		if result.State == nil || result.State.ServiceState != service.ServiceUnknown ||
			result.State.ProcessState != service.ProcessUnknown ||
			result.State.ConsistencyState != service.ConsistencyBlocked {
			t.Fatalf("timeout was not represented as blocked unknown: %#v", result)
		}
		if len(result.State.Conditions) != 1 || result.State.Conditions[0].Code != "status_observation_timeout" {
			t.Fatalf("missing timeout condition: %#v", result.State.Conditions)
		}
	}
}
