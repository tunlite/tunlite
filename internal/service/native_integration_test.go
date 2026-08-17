package service

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

func TestRealNativeServiceLifecycle(t *testing.T) {
	if os.Getenv("TUNLITE_REAL_SERVICE") != "1" {
		t.Skip("set TUNLITE_REAL_SERVICE=1 to run the real native-service integration test")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native services are unavailable on this platform")
	}
	adapter, err := NewCurrent(nil)
	if err != nil {
		t.Fatal(err)
	}
	connection := fmt.Sprintf("integration-%d", os.Getpid())
	executionPlan := plan.ExecutionPlan{
		Mode:      plan.ModeDirect,
		Exec:      plan.Exec{Program: "/bin/sleep", Args: []string{"30"}, Environment: map[string]string{}},
		Lifecycle: plan.Lifecycle{Restart: "never", RestartDelaySeconds: 1, StopTimeoutSeconds: 3},
		Endpoints: []plan.Endpoint{}, HealthChecks: []plan.HealthCheck{}, Redactions: []plan.Redaction{},
	}
	artifact, err := adapter.Build(connection, executionPlan)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = adapter.Disable(cleanupCtx, connection)
		for _, path := range artifact.LogPaths {
			_ = os.Remove(path)
		}
	})
	if err := adapter.Enable(ctx, artifact); err != nil {
		t.Fatalf("enable real %s service: %v", adapter.Name(), err)
	}
	waitForProcessState(t, ctx, adapter, connection, ProcessRunning)
	if err := adapter.Restart(ctx, connection); err != nil {
		t.Fatalf("restart real %s service: %v", adapter.Name(), err)
	}
	waitForProcessState(t, ctx, adapter, connection, ProcessRunning)
	if err := adapter.Disable(ctx, connection); err != nil {
		t.Fatalf("disable real %s service: %v", adapter.Name(), err)
	}
	observation, err := adapter.Inspect(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Installed || observation.Enabled || observation.ProcessState == ProcessRunning {
		t.Fatalf("native service was not cleaned up: %#v", observation)
	}
}

func waitForProcessState(t *testing.T, ctx context.Context, adapter Adapter, connection, expected string) {
	t.Helper()
	for {
		observation, err := adapter.Inspect(ctx, connection)
		if err == nil && observation.ProcessState == expected {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: last observation=%#v err=%v", expected, observation, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
