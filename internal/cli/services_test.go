package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

type fakeServiceAdapter struct {
	root         string
	observed     map[string]service.Observation
	enableCalls  []string
	disableCalls []string
	restartCalls []string
	failEnable   map[string]error
}

func newFakeServiceAdapter(root string) *fakeServiceAdapter {
	return &fakeServiceAdapter{root: root, observed: map[string]service.Observation{}, failEnable: map[string]error{}}
}

func (f *fakeServiceAdapter) Name() string { return "fake-native" }

func (f *fakeServiceAdapter) Target(name string) service.Artifact {
	return service.Artifact{Connection: name, Adapter: f.Name(), UnitName: "fake-" + name, Path: filepath.Join(f.root, name+".service")}
}

func (f *fakeServiceAdapter) Build(name string, value plan.ExecutionPlan) (service.Artifact, error) {
	digest, err := service.PlanDigest(value)
	if err != nil {
		return service.Artifact{}, err
	}
	artifact := f.Target(name)
	artifact.Plan, artifact.PlanDigest = value, digest
	artifact.Content, err = json.Marshal(value)
	return artifact, err
}

func (f *fakeServiceAdapter) Enable(_ context.Context, artifact service.Artifact) error {
	f.enableCalls = append(f.enableCalls, artifact.Connection)
	if err := f.failEnable[artifact.Connection]; err != nil {
		return err
	}
	f.observed[artifact.Connection] = service.Observation{
		Installed: true, Enabled: true, ProcessState: service.ProcessInactive,
		Artifact: append([]byte(nil), artifact.Content...), Platform: map[string]any{"unit": artifact.UnitName},
	}
	return nil
}

func (f *fakeServiceAdapter) Disable(_ context.Context, name string) error {
	f.disableCalls = append(f.disableCalls, name)
	f.observed[name] = service.Observation{ProcessState: service.ProcessInactive, Platform: map[string]any{"unit": "fake-" + name}}
	return nil
}

func (f *fakeServiceAdapter) Restart(_ context.Context, name string) error {
	f.restartCalls = append(f.restartCalls, name)
	return nil
}

func (f *fakeServiceAdapter) Inspect(_ context.Context, name string) (service.Observation, error) {
	if observation, ok := f.observed[name]; ok {
		return observation, nil
	}
	return service.Observation{ProcessState: service.ProcessInactive, Platform: map[string]any{"unit": "fake-" + name}}, nil
}

func (f *fakeServiceAdapter) Logs(_ context.Context, name string, _ int, _ bool, streams service.Streams) error {
	if streams.Out != nil {
		_, _ = io.WriteString(streams.Out, name+": provider log\n")
	}
	return nil
}

func useFakeServiceAdapter(t *testing.T, adapter *fakeServiceAdapter) {
	t.Helper()
	previous := newServiceAdapter
	newServiceAdapter = func() (service.Adapter, error) { return adapter, nil }
	t.Cleanup(func() { newServiceAdapter = previous })
}

func TestNativeServiceLifecycleUsesFourDimensionalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	useFakeServiceAdapter(t, adapter)

	if code, out, stderr := invoke(t, "add", "db", "--to", "me@example.com", "-D", "1080", "--json"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr := invoke(t, "enable", "db", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("enable exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(out, `"desiredState":"enabled"`) || !strings.Contains(out, `"syncState":"synced"`) {
		t.Fatalf("enable did not return four-dimensional state: %s", out)
	}
	code, out, stderr = invoke(t, "status", "db", "--json")
	if code != 0 || !strings.Contains(out, `"processState":"inactive"`) || !strings.Contains(out, `"health":"unknown"`) {
		t.Fatalf("status exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = invoke(t, "logs", "db", "--json")
	if code != 0 || !strings.Contains(out, "provider log") {
		t.Fatalf("logs exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = invoke(t, "rm", "db", "--json")
	if code != 2 || !strings.Contains(out, `"code":"already_exists"`) {
		t.Fatalf("enabled rm exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = invoke(t, "disable", "db", "--json")
	if code != 0 || !strings.Contains(out, `"desiredState":"disabled"`) || !strings.Contains(out, `"syncState":"synced"`) {
		t.Fatalf("disable exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if len(adapter.enableCalls) != 1 || len(adapter.disableCalls) != 1 {
		t.Fatalf("enable=%v disable=%v", adapter.enableCalls, adapter.disableCalls)
	}
}

func TestEnableRequiresExplicitApprovalForPublish(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "publish", "--to", "me@example.com", "-R", "19000:localhost:3000", "--json"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, _ := invoke(t, "enable", "publish", "--json", "--non-interactive")
	if code != 1 || !strings.Contains(out, `"requiresApproval":true`) || len(adapter.enableCalls) != 0 {
		t.Fatalf("unapproved enable exit=%d stdout=%s calls=%v", code, out, adapter.enableCalls)
	}
	code, out, stderr := invoke(t, "enable", "publish", "--approve", "--json", "--non-interactive")
	if code != 0 || len(adapter.enableCalls) != 1 {
		t.Fatalf("approved enable exit=%d stdout=%s stderr=%s calls=%v", code, out, stderr, adapter.enableCalls)
	}
}

func TestEnableIsIdempotentWhenServiceIsRunningAndSynced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	useFakeServiceAdapter(t, adapter)
	if code, _, stderr := invoke(t, "add", "db", "--to", "me@example.com", "-D", "1080"); code != 0 {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := invoke(t, "enable", "db"); code != 0 {
		t.Fatal(code, stderr)
	}
	observation := adapter.observed["db"]
	observation.ProcessState = service.ProcessRunning
	adapter.observed["db"] = observation
	code, out, stderr := invoke(t, "enable", "db", "--json")
	if code != 0 || !strings.Contains(out, `"changed":false`) || len(adapter.enableCalls) != 1 {
		t.Fatalf("idempotent enable exit=%d stdout=%s stderr=%s calls=%v", code, out, stderr, adapter.enableCalls)
	}
}

func TestSyncRepairsExecutionPlanDrift(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	useFakeServiceAdapter(t, adapter)
	if code, _, stderr := invoke(t, "add", "db", "--to", "me@example.com", "-D", "1080"); code != 0 {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := invoke(t, "enable", "db"); code != 0 {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := invoke(t, "set", "db", "-D", "1081"); code != 0 {
		t.Fatal(code, stderr)
	}
	code, out, stderr := invoke(t, "status", "db", "--json")
	if code != 0 || !strings.Contains(out, `"syncState":"drifted"`) {
		t.Fatalf("status exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = invoke(t, "sync", "db", "--dry-run", "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"planned"`) || len(adapter.enableCalls) != 1 {
		t.Fatalf("dry sync exit=%d stdout=%s stderr=%s calls=%v", code, out, stderr, adapter.enableCalls)
	}
	code, out, stderr = invoke(t, "sync", "db", "--json")
	if code != 0 || !strings.Contains(out, `"syncState":"synced"`) || len(adapter.enableCalls) != 2 {
		t.Fatalf("sync exit=%d stdout=%s stderr=%s calls=%v", code, out, stderr, adapter.enableCalls)
	}
}

func TestBatchEnableReturnsPartialResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	adapter.failEnable["b"] = errors.New("native manager rejected unit")
	useFakeServiceAdapter(t, adapter)
	for _, name := range []string{"a", "b"} {
		if code, _, stderr := invoke(t, "add", name, "--to", "me@example.com", "-D", map[string]string{"a": "1080", "b": "1081"}[name]); code != 0 {
			t.Fatal(code, stderr)
		}
	}
	code, out, stderr := invoke(t, "enable", "all", "--json")
	if code != exitPartial || !strings.Contains(out, `"outcome":"partial"`) || !strings.Contains(out, `"connection":"b"`) {
		t.Fatalf("batch exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestSyncRequiresFreshApprovalWhenPlanBecomesPublish(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	useFakeServiceAdapter(t, adapter)
	if code, _, stderr := invoke(t, "add", "edge", "--to", "me@example.com", "-D", "1080"); code != 0 {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := invoke(t, "enable", "edge"); code != 0 {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := invoke(t, "set", "edge", "-R", "19000:localhost:3000"); code != 0 {
		t.Fatal(code, stderr)
	}
	code, out, stderr := invoke(t, "sync", "edge", "--dry-run", "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"operation":"sync"`) {
		t.Fatalf("dry sync approval action exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, _ = invoke(t, "sync", "edge", "--json", "--non-interactive")
	if code != 1 || !strings.Contains(out, `"requiresApproval":true`) || len(adapter.enableCalls) != 1 {
		t.Fatalf("unapproved sync exit=%d stdout=%s calls=%v", code, out, adapter.enableCalls)
	}
	code, out, stderr = invoke(t, "sync", "edge", "--approve", "--json", "--non-interactive")
	if code != 0 || !strings.Contains(out, `"syncState":"synced"`) || len(adapter.enableCalls) != 2 {
		t.Fatalf("approved sync exit=%d stdout=%s stderr=%s calls=%v", code, out, stderr, adapter.enableCalls)
	}
}

func TestNativeServiceRecordPreventsCrossDeclarationNameCollision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	t.Setenv("TUNLITE_SSH", "/usr/bin/ssh")
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	useFakeServiceAdapter(t, adapter)
	first := filepath.Join(home, "project-a.json")
	second := filepath.Join(home, "project-b.json")
	if code, _, stderr := invoke(t, "add", "db", "--to", "me@a", "-D", "1080", "--config", first); code != 0 {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := invoke(t, "add", "db", "--to", "me@b", "-D", "1081", "--config", second); code != 0 {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := invoke(t, "enable", "db", "--config", first); code != 0 {
		t.Fatal(code, stderr)
	}
	code, out, _ := invoke(t, "status", "db", "--config", second, "--json")
	if code != 1 || !strings.Contains(out, `"code":"declaration_conflict"`) {
		t.Fatalf("cross-declaration status exit=%d stdout=%s", code, out)
	}
}
