package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

const nativeServiceProviderHelperEnvironment = "TUNLITE_NATIVE_SERVICE_PROVIDER_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(nativeServiceProviderHelperEnvironment) == "1" {
		fmt.Fprintln(os.Stdout, "tunlite-native-stdout")
		fmt.Fprintln(os.Stderr, "tunlite-native-stderr")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

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
	connection := "it-native-v3"
	executionPlan := plan.ExecutionPlan{
		Mode:      plan.ModeDirect,
		Exec:      nativeServiceProviderExec(t),
		Lifecycle: plan.Lifecycle{Restart: "always", RestartDelaySeconds: 1, StopTimeoutSeconds: 3},
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
		_ = adapter.Remove(cleanupCtx, artifact.Reference())
		for _, path := range artifact.LogPaths {
			_ = os.Truncate(path, 0)
		}
	})
	for _, path := range artifact.LogPaths {
		_ = os.Truncate(path, 0)
	}
	if err := adapter.Materialize(ctx, artifact); err != nil {
		t.Fatalf("materialize real %s service: %v", adapter.Name(), err)
	}
	if err := adapter.Start(ctx, artifact.Reference()); err != nil {
		t.Fatalf("start real %s service: %v", adapter.Name(), err)
	}
	background := waitForProcessState(t, ctx, adapter, artifact.Reference(), ProcessRunning)
	if background.AutoStart {
		t.Fatalf("background service unexpectedly enabled autostart: %#v", background)
	}
	firstPID := observationPID(t, background)
	waitForNativeServiceLogs(t, ctx, adapter, artifact.Reference())
	if err := killNativeProvider(ctx, adapter, artifact.Reference()); err != nil {
		t.Fatalf("kill real %s provider: %v", adapter.Name(), err)
	}
	restarted := waitForDifferentPID(t, ctx, adapter, artifact.Reference(), firstPID)
	if restarted.AutoStart {
		t.Fatalf("failure recovery changed background persistence: %#v", restarted)
	}
	if err := adapter.Enable(ctx, artifact.Reference()); err != nil {
		t.Fatalf("enable running real %s service: %v", adapter.Name(), err)
	}
	persistent := waitForProcessState(t, ctx, adapter, artifact.Reference(), ProcessRunning)
	if !persistent.AutoStart || observationPID(t, persistent) != observationPID(t, restarted) {
		t.Fatalf("enable did not preserve the running process while making it persistent: before=%#v after=%#v", restarted, persistent)
	}
	if err := adapter.Restart(ctx, artifact.Reference()); err != nil {
		t.Fatalf("restart real %s service: %v", adapter.Name(), err)
	}
	waitForDifferentPID(t, ctx, adapter, artifact.Reference(), observationPID(t, persistent))
	if err := adapter.Disable(ctx, artifact.Reference()); err != nil {
		t.Fatalf("disable real %s service: %v", adapter.Name(), err)
	}
	observation, err := adapter.Inspect(ctx, artifact.Reference())
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Installed || observation.AutoStart || observation.ProcessState == ProcessRunning {
		t.Fatalf("native service did not return to stopped mode: %#v", observation)
	}
	if err := adapter.Remove(ctx, artifact.Reference()); err != nil {
		t.Fatalf("remove real %s service: %v", adapter.Name(), err)
	}
}

func nativeServiceProviderExec(t *testing.T) plan.Exec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	executable = filepath.Clean(executable)
	info, err := os.Stat(executable)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("native test provider requires executable %s: info=%v err=%v", executable, info, err)
	}
	// Use the already-running native test executable as the provider. A cache
	// shebang script can leave launchd's xpcproxy unable to exec even though
	// launchctl already exposes the proxy PID.
	return plan.Exec{
		Program: executable,
		Args: []string{
			"-test.run=^TestNativeServiceProviderProcess$",
			"-test.count=1",
		},
		Environment: map[string]string{nativeServiceProviderHelperEnvironment: "1"},
	}
}

func waitForProcessState(t *testing.T, ctx context.Context, adapter Adapter, reference Reference, expected string) Observation {
	t.Helper()
	for {
		observation, err := adapter.Inspect(ctx, reference)
		if err == nil && observation.ProcessState == expected {
			return observation
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: last observation=%#v err=%v", expected, observation, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func waitForDifferentPID(t *testing.T, ctx context.Context, adapter Adapter, reference Reference, previous int) Observation {
	t.Helper()
	for {
		observation, err := adapter.Inspect(ctx, reference)
		if err == nil && observation.ProcessState == ProcessRunning {
			if pid, parseErr := parseObservationPID(observation); parseErr == nil && pid != previous {
				return observation
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for provider restart after pid %d: last observation=%#v err=%v\n%s", previous, observation, err, nativeServiceDiagnostics(adapter, reference))
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func observationPID(t *testing.T, observation Observation) int {
	t.Helper()
	pid, err := parseObservationPID(observation)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func parseObservationPID(observation Observation) (int, error) {
	value, ok := observation.Platform["pid"].(string)
	if !ok || value == "" {
		return 0, fmt.Errorf("native observation has no pid: %#v", observation.Platform)
	}
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("native observation has invalid pid %q", value)
	}
	return pid, nil
}

func killNativeProvider(ctx context.Context, adapter Adapter, reference Reference) error {
	switch current := adapter.(type) {
	case *Launchd:
		target := current.resolve(reference)
		_, err := current.run(ctx, "kill", "SIGKILL", current.domainTarget(target.Scope, target.UnitName))
		return err
	case *Systemd:
		target := current.resolve(reference)
		_, err := current.run(ctx, "kill", "--signal=KILL", target.UnitName)
		return err
	default:
		return fmt.Errorf("unsupported native adapter %T", adapter)
	}
}

func waitForNativeServiceLogs(t *testing.T, ctx context.Context, adapter Adapter, reference Reference) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var stdout, stderr bytes.Buffer
		err := adapter.Logs(ctx, reference, 50, false, Streams{Out: &stdout, Err: &stderr})
		combined := stdout.String() + stderr.String()
		if err == nil && strings.Contains(combined, "tunlite-native-stdout") && strings.Contains(combined, "tunlite-native-stderr") {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for native service logs: stdout=%q stderr=%q err=%v\n%s", stdout.String(), stderr.String(), err, nativeServiceDiagnostics(adapter, reference))
		case <-deadline.C:
			t.Fatalf("waiting for native service logs: stdout=%q stderr=%q err=%v\n%s", stdout.String(), stderr.String(), err, nativeServiceDiagnostics(adapter, reference))
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func nativeServiceDiagnostics(adapter Adapter, reference Reference) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var output strings.Builder
	switch current := adapter.(type) {
	case *Launchd:
		target := current.resolve(reference)
		raw, readErr := os.ReadFile(target.Path)
		fmt.Fprintf(&output, "plist %s: %s (%v)\n", target.Path, raw, readErr)
		job, jobErr := current.run(ctx, "print", current.domainTarget(target.Scope, target.UnitName))
		fmt.Fprintf(&output, "launchctl: %s (%v)\n", job, jobErr)
		for _, path := range target.LogPaths {
			info, statErr := os.Stat(path)
			fmt.Fprintf(&output, "log %s: %#v (%v)\n", path, info, statErr)
		}
	case *Systemd:
		target := current.resolve(reference)
		raw, readErr := os.ReadFile(target.Path)
		fmt.Fprintf(&output, "unit %s: %s (%v)\n", target.Path, raw, readErr)
	}
	return output.String()
}
