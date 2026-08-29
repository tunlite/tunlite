package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/plan"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

type fakeServiceAdapter struct {
	root             string
	observed         map[string]service.Observation
	artifacts        map[string]service.Artifact
	materializeCalls []string
	startCalls       []string
	stopCalls        []string
	enableCalls      []string
	disableCalls     []string
	restartCalls     []string
	removeCalls      []string
	failures         map[string][]error
	logsStarted      chan struct{}
	logsRelease      chan struct{}
	materializeHook  func(service.Artifact)
	removeHook       func(service.Reference)
	discoverHook     func(int)
	discoverCalls    int
	discoverMu       sync.Mutex
}

func newFakeServiceAdapter(root string) *fakeServiceAdapter {
	return &fakeServiceAdapter{
		root: root, observed: map[string]service.Observation{}, artifacts: map[string]service.Artifact{}, failures: map[string][]error{},
	}
}

func (f *fakeServiceAdapter) fail(operation string) error {
	queued := f.failures[operation]
	if len(queued) == 0 {
		return nil
	}
	err := queued[0]
	f.failures[operation] = queued[1:]
	return err
}

func (f *fakeServiceAdapter) Name() string { return "fake-native" }

func (f *fakeServiceAdapter) Capabilities() service.Capabilities {
	return service.Capabilities{Registration: true, Background: true, Persistence: true, Logs: true, Discovery: true}
}

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

func (f *fakeServiceAdapter) BuildBound(reference service.Reference, value plan.ExecutionPlan) (service.Artifact, error) {
	artifact, err := f.Build(reference.Connection, value)
	if err != nil {
		return service.Artifact{}, err
	}
	if reference.UnitName != "" {
		artifact.UnitName = reference.UnitName
	}
	if reference.ArtifactPath != "" {
		artifact.Path = reference.ArtifactPath
	}
	if len(reference.LogPaths) > 0 {
		artifact.LogPaths = append([]string(nil), reference.LogPaths...)
	}
	return artifact, nil
}

func (f *fakeServiceAdapter) Materialize(_ context.Context, artifact service.Artifact) error {
	f.materializeCalls = append(f.materializeCalls, artifact.Connection)
	if err := f.fail("materialize"); err != nil {
		return err
	}
	f.artifacts[artifact.Connection] = artifact
	observation := f.observed[artifact.Connection]
	observation.Installed = true
	observation.Artifact = append([]byte(nil), artifact.Content...)
	if observation.ProcessState == "" {
		observation.ProcessState = service.ProcessInactive
	}
	observation.Platform = map[string]any{"unit": artifact.UnitName}
	f.observed[artifact.Connection] = observation
	if hook := f.materializeHook; hook != nil {
		f.materializeHook = nil
		hook(artifact)
	}
	return nil
}

func (f *fakeServiceAdapter) Remove(_ context.Context, reference service.Reference) error {
	f.removeCalls = append(f.removeCalls, reference.Connection)
	if err := f.fail("remove"); err != nil {
		return err
	}
	delete(f.artifacts, reference.Connection)
	f.observed[reference.Connection] = service.Observation{ProcessState: service.ProcessInactive, Platform: map[string]any{}}
	if hook := f.removeHook; hook != nil {
		f.removeHook = nil
		hook(reference)
	}
	return nil
}

func (f *fakeServiceAdapter) Start(_ context.Context, reference service.Reference) error {
	f.startCalls = append(f.startCalls, reference.Connection)
	if err := f.fail("start"); err != nil {
		return err
	}
	observation := f.observed[reference.Connection]
	observation.ProcessState, observation.AutoStart = service.ProcessRunning, false
	f.observed[reference.Connection] = observation
	return nil
}

func (f *fakeServiceAdapter) Stop(_ context.Context, reference service.Reference) error {
	f.stopCalls = append(f.stopCalls, reference.Connection)
	if err := f.fail("stop"); err != nil {
		return err
	}
	observation := f.observed[reference.Connection]
	observation.ProcessState, observation.AutoStart = service.ProcessInactive, false
	f.observed[reference.Connection] = observation
	return nil
}

func (f *fakeServiceAdapter) Enable(_ context.Context, reference service.Reference) error {
	f.enableCalls = append(f.enableCalls, reference.Connection)
	if err := f.fail("enable"); err != nil {
		return err
	}
	observation := f.observed[reference.Connection]
	observation.ProcessState, observation.AutoStart = service.ProcessRunning, true
	f.observed[reference.Connection] = observation
	return nil
}

func (f *fakeServiceAdapter) Disable(_ context.Context, reference service.Reference) error {
	f.disableCalls = append(f.disableCalls, reference.Connection)
	if err := f.fail("disable"); err != nil {
		return err
	}
	observation := f.observed[reference.Connection]
	observation.ProcessState, observation.AutoStart = service.ProcessInactive, false
	f.observed[reference.Connection] = observation
	return nil
}

func (f *fakeServiceAdapter) Restart(_ context.Context, reference service.Reference) error {
	f.restartCalls = append(f.restartCalls, reference.Connection)
	return f.fail("restart")
}

func (f *fakeServiceAdapter) Inspect(_ context.Context, reference service.Reference) (service.Observation, error) {
	if observation, ok := f.observed[reference.Connection]; ok {
		return observation, nil
	}
	return service.Observation{ProcessState: service.ProcessInactive, Platform: map[string]any{}}, nil
}

func (f *fakeServiceAdapter) Logs(ctx context.Context, reference service.Reference, _ int, follow bool, streams service.Streams) error {
	if streams.Out != nil {
		_, _ = io.WriteString(streams.Out, reference.Connection+": provider log\n")
	}
	if follow && f.logsStarted != nil {
		f.logsStarted <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.logsRelease:
		}
	}
	return nil
}

func (f *fakeServiceAdapter) Discover(_ context.Context) ([]service.Candidate, error) {
	f.discoverMu.Lock()
	f.discoverCalls++
	discoverCalls := f.discoverCalls
	discoverHook := f.discoverHook
	f.discoverMu.Unlock()
	if discoverHook != nil {
		discoverHook(discoverCalls)
	}
	result := make([]service.Candidate, 0, len(f.artifacts))
	for key, artifact := range f.artifacts {
		result = append(result, service.Candidate{
			Reference: artifact.Reference(), Plan: artifact.Plan, Observation: f.observed[key],
		})
	}
	return result, nil
}

func replaceDirectoryWithBlocker(path string) (func() error, error) {
	backup := path + ".transaction-test-backup"
	if err := os.Rename(path, backup); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte("blocks directory recreation\n"), 0o600); err != nil {
		_ = os.Rename(backup, path)
		return nil, err
	}
	return func() error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Rename(backup, path)
	}, nil
}

func useFakeServiceAdapter(t *testing.T, adapter *fakeServiceAdapter) func(*testing.T, ...string) (int, string, string) {
	t.Helper()
	return func(t *testing.T, args ...string) (int, string, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		budget := lifecycleObservationBudget{Overall: time.Millisecond, PerItem: time.Millisecond, PollInterval: time.Millisecond}
		code := runContextWithOptions(context.Background(), args, IO{Out: &out, Err: &stderr}, options{
			serviceAdapter: func() (service.Adapter, error) { return adapter, nil }, lifecycleBudget: &budget,
		})
		return code, out.String(), stderr.String()
	}
}

func snapshotFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[relative] = string(raw)
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return result
}

func assertFileSnapshot(t *testing.T, want, got map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("file count changed: got=%v want=%v", got, want)
	}
	for path, content := range want {
		if got[path] != content {
			t.Fatalf("file %s changed during dry-run", path)
		}
	}
}

func TestAddMaterializesStoppedNativeService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)

	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if len(adapter.materializeCalls) != 1 || len(adapter.startCalls) != 0 || len(adapter.enableCalls) != 0 {
		t.Fatalf("add calls materialize=%v start=%v enable=%v", adapter.materializeCalls, adapter.startCalls, adapter.enableCalls)
	}
	code, out, stderr := invoke(t, "status", "db", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"serviceState":"registered"`) || !strings.Contains(out, `"mode":"stopped"`) {
		t.Fatalf("status exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestAddRejectsOccupiedNativeServiceIdentityWithoutOverwritingIt(t *testing.T) {
	tests := []struct {
		name        string
		observation service.Observation
	}{
		{name: "enabled residue", observation: service.Observation{AutoStart: true, ProcessState: service.ProcessInactive}},
		{name: "loaded residue", observation: service.Observation{ProcessState: service.ProcessRunning}},
		{name: "unknown residue", observation: service.Observation{ProcessState: service.ProcessUnknown}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
			adapter.observed["db"] = test.observation
			invoke := useFakeServiceAdapter(t, adapter)

			code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com")
			if code != 2 || stderr != "" || !strings.Contains(out, `"code":"already_exists"`) {
				t.Fatalf("add exit=%d stdout=%s stderr=%s", code, out, stderr)
			}
			if len(adapter.materializeCalls) != 0 {
				t.Fatalf("occupied native identity was overwritten: %v", adapter.materializeCalls)
			}
			store, err := connectionStore(options{})
			if err != nil {
				t.Fatal(err)
			}
			document, err := store.Load()
			if err != nil || len(document.Items) != 0 {
				t.Fatalf("failed add changed declarations: items=%v err=%v", document.Items, err)
			}
		})
	}
}

func TestStandardModeTransitionsAreExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, _, stderr := invoke(t, "add", "db", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatal(code, stderr)
	}
	if code, out, stderr := invoke(t, "start", "db", "--json"); code != 0 || !strings.Contains(out, `"mode":"background"`) {
		t.Fatalf("start exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "enable", "db", "--json"); code != 0 || !strings.Contains(out, `"mode":"persistent"`) {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, _ := invoke(t, "stop", "db", "--json"); code != 1 ||
		!strings.Contains(out, `"code":"persistent_connection"`) || !strings.Contains(out, "use disable") {
		t.Fatalf("stop persistent exit=%d out=%s", code, out)
	}
	if code, out, stderr := invoke(t, "disable", "db", "--json"); code != 0 || !strings.Contains(out, `"mode":"stopped"`) {
		t.Fatalf("disable exit=%d out=%s err=%s", code, out, stderr)
	}
}

func TestLifecycleCommandMatrix(t *testing.T) {
	tests := []struct {
		name         string
		initialMode  string
		command      string
		wantCode     int
		wantMode     string
		wantError    string
		wantChanged  bool
		wantRestarts int
	}{
		{name: "stopped start", initialMode: service.ModeStopped, command: "start", wantMode: service.ModeBackground, wantChanged: true},
		{name: "stopped stop is idempotent", initialMode: service.ModeStopped, command: "stop", wantMode: service.ModeStopped},
		{name: "stopped enable", initialMode: service.ModeStopped, command: "enable", wantMode: service.ModePersistent, wantChanged: true},
		{name: "stopped disable is idempotent", initialMode: service.ModeStopped, command: "disable", wantMode: service.ModeStopped},
		{name: "stopped restart is rejected", initialMode: service.ModeStopped, command: "restart", wantCode: 1, wantMode: service.ModeStopped, wantError: "connection_stopped"},
		{name: "background start is idempotent", initialMode: service.ModeBackground, command: "start", wantMode: service.ModeBackground},
		{name: "background stop", initialMode: service.ModeBackground, command: "stop", wantMode: service.ModeStopped, wantChanged: true},
		{name: "background enable without restart", initialMode: service.ModeBackground, command: "enable", wantMode: service.ModePersistent, wantChanged: true},
		{name: "background disable is rejected", initialMode: service.ModeBackground, command: "disable", wantCode: 1, wantMode: service.ModeBackground, wantError: "background_connection"},
		{name: "background restart", initialMode: service.ModeBackground, command: "restart", wantMode: service.ModeBackground, wantChanged: true, wantRestarts: 1},
		{name: "persistent enable is idempotent", initialMode: service.ModePersistent, command: "enable", wantMode: service.ModePersistent},
		{name: "persistent stop is rejected", initialMode: service.ModePersistent, command: "stop", wantCode: 1, wantMode: service.ModePersistent, wantError: "persistent_connection"},
		{name: "persistent disable", initialMode: service.ModePersistent, command: "disable", wantMode: service.ModeStopped, wantChanged: true},
		{name: "persistent restart", initialMode: service.ModePersistent, command: "restart", wantMode: service.ModePersistent, wantChanged: true, wantRestarts: 1},
		{name: "persistent start is rejected", initialMode: service.ModePersistent, command: "start", wantCode: 1, wantMode: service.ModePersistent, wantError: "persistent_connection"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
			invoke := useFakeServiceAdapter(t, adapter)
			if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
				t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
			}
			switch test.initialMode {
			case service.ModeBackground:
				if code, out, stderr := invoke(t, "start", "db", "--json"); code != 0 {
					t.Fatalf("setup start exit=%d out=%s err=%s", code, out, stderr)
				}
			case service.ModePersistent:
				if code, out, stderr := invoke(t, "enable", "db", "--json"); code != 0 {
					t.Fatalf("setup enable exit=%d out=%s err=%s", code, out, stderr)
				}
			}
			adapter.startCalls = nil
			adapter.stopCalls = nil
			adapter.enableCalls = nil
			adapter.disableCalls = nil
			adapter.restartCalls = nil

			code, out, stderr := invoke(t, test.command, "db", "--json")
			if code != test.wantCode || stderr != "" {
				t.Fatalf("%s exit=%d want=%d out=%s err=%s", test.command, code, test.wantCode, out, stderr)
			}
			var response struct {
				Results []serviceItemResult `json:"results"`
			}
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 {
				t.Fatalf("unexpected results: %#v", response.Results)
			}
			result := response.Results[0]
			if result.Changed != test.wantChanged {
				t.Fatalf("changed=%t want=%t result=%#v", result.Changed, test.wantChanged, result)
			}
			if test.wantError != "" {
				if result.Error == nil || result.Error.Code != test.wantError {
					t.Fatalf("error=%#v want code %s", result.Error, test.wantError)
				}
			} else if result.Error != nil || result.State == nil || result.State.Mode != test.wantMode {
				t.Fatalf("unexpected lifecycle result: %#v", result)
			}
			if len(adapter.restartCalls) != test.wantRestarts {
				t.Fatalf("restart calls=%v want=%d", adapter.restartCalls, test.wantRestarts)
			}
			records, err := serviceRecordStore()
			if err != nil {
				t.Fatal(err)
			}
			record, exists, err := records.Load("db")
			if err != nil || !exists || record.Mode != test.wantMode {
				t.Fatalf("final record=%#v exists=%t err=%v want mode=%s", record, exists, err, test.wantMode)
			}
		})
	}
}

func TestAllServiceAndOwnershipDryRunsAreReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	external, err := adapter.Build("external", adapter.artifacts["db"].Plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Materialize(context.Background(), external); err != nil {
		t.Fatal(err)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	reserved := recordForArtifact(adapter.Target("reserved"), service.ModeStopped, filepath.Join(home, "config", "connections.json"))
	if err := records.Write(context.Background(), reserved); err != nil {
		t.Fatal(err)
	}

	baseline := snapshotFiles(t, home)
	mutations := func() int {
		return len(adapter.materializeCalls) + len(adapter.startCalls) + len(adapter.stopCalls) +
			len(adapter.enableCalls) + len(adapter.disableCalls) + len(adapter.restartCalls) + len(adapter.removeCalls)
	}
	baselineMutations := mutations()
	tests := []struct {
		name string
		args []string
		code int
	}{
		{name: "start", args: []string{"start", "db", "--dry-run", "--json"}},
		{name: "stop", args: []string{"stop", "db", "--dry-run", "--json"}},
		{name: "enable", args: []string{"enable", "db", "--dry-run", "--json"}},
		{name: "disable", args: []string{"disable", "db", "--dry-run", "--json"}},
		{name: "restart rejected", args: []string{"restart", "db", "--dry-run", "--json"}, code: 1},
		{name: "remove", args: []string{"remove", "db", "--dry-run", "--json"}},
		{name: "rename", args: []string{"rename", "db", "renamed", "--dry-run", "--json"}},
		{name: "rename record collision", args: []string{"rename", "db", "reserved", "--dry-run", "--json"}, code: 2},
		{name: "unmanage", args: []string{"unmanage", "db", "--dry-run", "--json"}},
		{name: "resolve tunlite", args: []string{"resolve", "db", "--use", "tunlite", "--dry-run", "--json"}},
		{name: "resolve system", args: []string{"resolve", "db", "--use", "system", "--dry-run", "--json"}},
		{name: "manage", args: []string{"manage", "fake-external", "--name", "adopted", "--dry-run", "--json", "--non-interactive"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, out, stderr := invoke(t, test.args...)
			if code != test.code || stderr != "" {
				t.Fatalf("args=%v exit=%d want=%d out=%s err=%s", test.args, code, test.code, out, stderr)
			}
			assertFileSnapshot(t, baseline, snapshotFiles(t, home))
			if got := mutations(); got != baselineMutations {
				t.Fatalf("dry-run called a mutating Adapter method: got=%d want=%d", got, baselineMutations)
			}
		})
	}
}

func TestRemoveRejectsRunningAndDeletesStoppedService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	_, _, _ = invoke(t, "start", "db")
	if code, out, _ := invoke(t, "remove", "db", "--json"); code == 0 || !strings.Contains(out, `"outcome":"failed"`) {
		t.Fatalf("running remove exit=%d out=%s", code, out)
	}
	_, _, _ = invoke(t, "stop", "db")
	if code, out, stderr := invoke(t, "remove", "db", "--json"); code != 0 || !strings.Contains(out, `"operation":"remove"`) {
		t.Fatalf("remove exit=%d out=%s err=%s", code, out, stderr)
	}
	if len(adapter.removeCalls) != 1 {
		t.Fatalf("remove calls=%v", adapter.removeCalls)
	}
}

func TestSetAutomaticallyUpdatesAndRestartsBackgroundService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	_, _, _ = invoke(t, "start", "db")
	if code, out, stderr := invoke(t, "set", "db", "-D", "1081", "--json"); code != 0 {
		t.Fatalf("set exit=%d out=%s err=%s", code, out, stderr)
	}
	if len(adapter.materializeCalls) != 2 || len(adapter.restartCalls) != 1 {
		t.Fatalf("materialize=%v restart=%v", adapter.materializeCalls, adapter.restartCalls)
	}
	if code, out, _ := invoke(t, "sync", "db", "--json"); code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatalf("sync still public exit=%d out=%s", code, out)
	}
}

func TestSetDryRunPreflightsRestartWithoutMutatingServiceOrDeclaration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	_, _, _ = invoke(t, "start", "db")
	materialized, restarted := len(adapter.materializeCalls), len(adapter.restartCalls)

	code, out, stderr := invoke(t, "set", "db", "-D", "1081", "--dry-run", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"code":"connection_restart_required"`) {
		t.Fatalf("set dry-run exit=%d out=%s err=%s", code, out, stderr)
	}
	if len(adapter.materializeCalls) != materialized || len(adapter.restartCalls) != restarted {
		t.Fatalf("set dry-run mutated service: materialize=%v restart=%v", adapter.materializeCalls, adapter.restartCalls)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, connection := configstore.Find(&document, "db")
	if connection == nil || !strings.Contains(string(connection.Spec.Config), "1080") {
		t.Fatalf("set dry-run changed declaration: %#v", connection)
	}
	entries, err := os.ReadDir(filepath.Join(home, "state", "core", "services"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".lock") {
			t.Fatalf("set dry-run created a coordination lock: %s", entry.Name())
		}
	}
}

func TestRunningPublishChangeWarnsAndApplies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "edge", "-D", "1080", "me@example.com")
	_, _, _ = invoke(t, "start", "edge")
	code, out, stderr := invoke(t, "set", "edge", "-R", "19000:localhost:3000", "--json", "--non-interactive")
	if code != 0 || !strings.Contains(out, `"code":"network_exposure"`) || len(adapter.restartCalls) != 1 {
		t.Fatalf("set exit=%d out=%s err=%s restarts=%v", code, out, stderr, adapter.restartCalls)
	}
	code, out, _ = invoke(t, "set", "edge", "--approve", "--json")
	if code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatalf("retired --approve exit=%d out=%s", code, out)
	}
}

func TestStartPublishNeedsNoApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "publish", "-R", "19000:localhost:3000", "me@example.com")
	code, out, _ := invoke(t, "start", "publish", "--json", "--non-interactive")
	if code != 0 || strings.Contains(out, `"code":"approval_required"`) || len(adapter.startCalls) != 1 {
		t.Fatalf("start exit=%d out=%s starts=%v", code, out, adapter.startCalls)
	}
	code, out, _ = invoke(t, "start", "publish", "--approve", "--json", "--non-interactive")
	if code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) || len(adapter.startCalls) != 1 {
		t.Fatalf("retired --approve exit=%d out=%s starts=%v", code, out, adapter.startCalls)
	}
}

func TestForegroundOnlyStatusIsExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	invoke := func(t *testing.T, args ...string) (int, string, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := runContext(context.Background(), args, IO{Out: &out, Err: &stderr}, func() (service.Adapter, error) {
			return nil, errors.New("no native manager")
		})
		return code, out.String(), stderr.String()
	}
	if code, out, stderr := invoke(t, "add", "local", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	code, out, stderr := invoke(t, "status", "local", "--json")
	if code != 0 || !strings.Contains(out, `"serviceState":"unsupported"`) || !strings.Contains(out, `"code":"foreground_only"`) {
		t.Fatalf("status exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "remove", "local", "--json"); code != 0 {
		t.Fatalf("foreground-only remove exit=%d out=%s err=%s", code, out, stderr)
	}
}

func TestFollowingLogsStopsCleanly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	adapter.logsStarted = make(chan struct{}, 1)
	adapter.logsRelease = make(chan struct{})
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	done := make(chan int, 1)
	go func() {
		done <- runContext(context.Background(), []string{"logs", "db", "--follow"}, IO{Out: io.Discard, Err: io.Discard}, func() (service.Adapter, error) { return adapter, nil })
	}()
	select {
	case <-adapter.logsStarted:
	case <-time.After(time.Second):
		t.Fatal("log follower did not start")
	}
	close(adapter.logsRelease)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("logs exit=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("log follower did not stop")
	}
}

func TestFollowingLogsJSONEmitsNDJSONRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	code, out, stderr := invoke(t, "logs", "db", "--follow", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("logs exit=%d out=%s err=%s", code, out, stderr)
	}
	var record struct {
		ProtocolVersion int    `json:"protocolVersion"`
		Outcome         string `json:"outcome"`
		Connection      string `json:"connection"`
		Stream          string `json:"stream"`
		Line            string `json:"line"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &record); err != nil {
		t.Fatalf("invalid NDJSON record: %v\n%s", err, out)
	}
	if record.ProtocolVersion != ProtocolVersion || record.Outcome != "log" || record.Connection != "db" || record.Stream != "stdout" || !strings.Contains(record.Line, "provider log") {
		t.Fatalf("unexpected log record: %#v", record)
	}
}

func TestRenameKeepsNativeServiceIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "old", "-D", "1080", "me@example.com")
	if code, out, stderr := invoke(t, "rename", "old", "new", "--json"); code != 0 {
		t.Fatalf("rename exit=%d out=%s err=%s", code, out, stderr)
	}
	if len(adapter.materializeCalls) != 1 || len(adapter.restartCalls) != 0 {
		t.Fatalf("rename changed native service: materialize=%v restart=%v", adapter.materializeCalls, adapter.restartCalls)
	}
	if code, out, stderr := invoke(t, "start", "new", "--json"); code != 0 || !strings.Contains(out, `"mode":"background"`) {
		t.Fatalf("renamed start exit=%d out=%s err=%s", code, out, stderr)
	}
	if len(adapter.startCalls) != 1 || adapter.startCalls[0] != "old" {
		t.Fatalf("renamed connection lost service binding: %v", adapter.startCalls)
	}
}

func TestUnmanagePreservesServiceAndManageImportsItAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	_, _, _ = invoke(t, "start", "db")
	if code, out, stderr := invoke(t, "unmanage", "db", "--json"); code != 0 {
		t.Fatalf("unmanage exit=%d out=%s err=%s", code, out, stderr)
	}
	if len(adapter.stopCalls) != 0 || len(adapter.removeCalls) != 0 {
		t.Fatalf("unmanage changed service: stop=%v remove=%v", adapter.stopCalls, adapter.removeCalls)
	}
	if code, out, stderr := invoke(t, "manage", "fake-db", "--name", "db", "--json", "--non-interactive"); code != 0 {
		t.Fatalf("manage exit=%d out=%s err=%s", code, out, stderr)
	}
}

func TestUnmanageRejectsDriftAndKeepsManagementState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	observation := adapter.observed["db"]
	observation.Artifact = append(observation.Artifact, []byte("\nexternal-change")...)
	adapter.observed["db"] = observation

	if code, out, _ := invoke(t, "unmanage", "db", "--json"); code == 0 || !strings.Contains(out, "resolve it before unmanaging") {
		t.Fatalf("drifted unmanage exit=%d out=%s", code, out)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, connection := configstore.Find(&document, "db"); connection == nil {
		t.Fatal("drifted unmanage removed the declaration")
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := records.Load("db"); err != nil || !exists {
		t.Fatalf("drifted unmanage removed the service binding: exists=%t err=%v", exists, err)
	}
}

func TestRunRejectsAStoppedRecordWhoseNativeServiceDrifted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	observation := adapter.observed["db"]
	observation.AutoStart = true
	adapter.observed["db"] = observation
	if code, out, _ := invoke(t, "run", "db", "--dry-run", "--json"); code == 0 || !strings.Contains(out, "not provably stopped and consistent") {
		t.Fatalf("drifted foreground run exit=%d out=%s", code, out)
	}
}

func TestManageRejectsASecondOwnerForTheSameNativeService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	code, out, _ := invoke(t, "manage", "fake-db", "--name", "alias", "--json", "--non-interactive")
	if code == 0 || !strings.Contains(out, `"code":"already_exists"`) || !strings.Contains(out, "already managed") {
		t.Fatalf("duplicate manage exit=%d out=%s", code, out)
	}
}

func TestManagePickerFiltersAlreadyManagedServices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	candidates, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := filterManageCandidates(candidates, records, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 0 {
		t.Fatalf("already managed candidates leaked into picker: %#v", filtered)
	}
}

func TestConcurrentManageAllowsOnlyOneOwner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "native", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "unmanage", "native", "--json"); code != 0 {
		t.Fatalf("unmanage exit=%d out=%s err=%s", code, out, stderr)
	}

	start := make(chan struct{})
	results := make(chan int, 2)
	for _, name := range []string{"first", "second"} {
		name := name
		go func() {
			<-start
			var out, stderr bytes.Buffer
			results <- runContext(context.Background(), []string{
				"manage", "fake-native", "--name", name, "--json", "--non-interactive",
			}, IO{Out: &out, Err: &stderr}, func() (service.Adapter, error) { return adapter, nil })
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if <-results == 0 {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent manage successes=%d, want exactly one", successes)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, name := range []string{"first", "second"} {
		if _, exists, err := records.Load(name); err != nil {
			t.Fatal(err)
		} else if exists {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("native service has %d ownership records, want one", owners)
	}
}

func TestManagedNativeServiceKeepsItsOriginalIdentityWhenUpdated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "native", "-D", "1080", "me@example.com")
	_, _, _ = invoke(t, "unmanage", "native")
	if code, out, stderr := invoke(t, "manage", "fake-native", "--name", "alias", "--json", "--non-interactive"); code != 0 {
		t.Fatalf("manage exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "set", "alias", "-D", "1081", "--json"); code != 0 {
		t.Fatalf("set exit=%d out=%s err=%s", code, out, stderr)
	}
	if adapter.materializeCalls[len(adapter.materializeCalls)-1] != "native" {
		t.Fatalf("update created a second service instead of updating the bound one: %v", adapter.materializeCalls)
	}
	if _, exists := adapter.artifacts["alias"]; exists {
		t.Fatal("update created an artifact under the Tunlite alias")
	}
}

func TestManagedServiceRollbackRestoresOriginalNativeDefinition(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "native", "-D", "1080", "me@example.com")
	_, _, _ = invoke(t, "unmanage", "native")
	original := []byte("original native service bytes\n")
	artifact := adapter.artifacts["native"]
	artifact.Content = append([]byte(nil), original...)
	adapter.artifacts["native"] = artifact
	observation := adapter.observed["native"]
	observation.Artifact = append([]byte(nil), original...)
	adapter.observed["native"] = observation
	if code, out, stderr := invoke(t, "manage", "fake-native", "--name", "alias", "--json", "--non-interactive"); code != 0 {
		t.Fatalf("manage exit=%d out=%s err=%s", code, out, stderr)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, current := configstore.Find(&document, "alias")
	if current == nil {
		t.Fatal("managed connection is missing")
	}
	before := cloneConnection(*current)
	values, err := parseConnectionFlags("set", []string{"-D", "1081"}, true)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := sshdriver.ParseSetArguments(values.driverArg)
	if err != nil {
		t.Fatal(err)
	}
	after, err := applyConnectionFlags(before, values, patch)
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := stageUpdatedConnectionService(context.Background(), before, after, store.Path(), options{
		serviceAdapter: func() (service.Adapter, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.finish()
	if err := mutation.rollback(); err != nil {
		t.Fatal(err)
	}
	if got := adapter.observed["native"].Artifact; string(got) != string(original) {
		t.Fatalf("rollback rewrote the native definition: %q", got)
	}
}

func TestSetRestoresServiceDefinitionWhenRestartFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "start", "db", "--json"); code != 0 {
		t.Fatalf("start exit=%d out=%s err=%s", code, out, stderr)
	}
	original := append([]byte(nil), adapter.observed["db"].Artifact...)
	adapter.failures["restart"] = []error{errors.New("injected restart failure")}
	code, out, _ := invoke(t, "set", "db", "-D", "1081", "--json")
	if code == 0 || !strings.Contains(out, "injected restart failure") {
		t.Fatalf("set exit=%d out=%s", code, out)
	}
	if got := adapter.observed["db"].Artifact; !bytes.Equal(got, original) {
		t.Fatalf("failed restart did not restore original definition: %q", got)
	}
	if len(adapter.restartCalls) != 2 {
		t.Fatalf("expected failed restart plus recovery restart, got %v", adapter.restartCalls)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, connection := configstore.Find(&document, "db")
	if connection == nil || !strings.Contains(string(connection.Spec.Config), "1080") {
		t.Fatalf("failed set changed the declaration: %#v", connection)
	}
}

func TestAddRollsBackNativeStateWhenConfigWriteFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	adapter.materializeHook = func(service.Artifact) {
		_ = os.WriteFile(filepath.Join(home, "config"), []byte("blocks the config directory\n"), 0o600)
	}
	invoke := useFakeServiceAdapter(t, adapter)
	code, out, _ := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com")
	if code == 0 || !strings.Contains(out, `"outcome":"failed"`) {
		t.Fatalf("add unexpectedly succeeded: exit=%d out=%s", code, out)
	}
	if _, exists := adapter.artifacts["db"]; exists {
		t.Fatal("failed add left the native service materialized")
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := records.Load("db"); err != nil || exists {
		t.Fatalf("failed add left a service record: exists=%t err=%v", exists, err)
	}
}

func TestSetRollsBackNativeStateWhenConfigWriteFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	originalArtifact := append([]byte(nil), adapter.observed["db"].Artifact...)
	var restore func() error
	var hookErr error
	adapter.materializeHook = func(service.Artifact) {
		restore, hookErr = replaceDirectoryWithBlocker(filepath.Join(home, "config"))
	}
	code, out, _ := invoke(t, "set", "db", "-D", "1081", "--json")
	if hookErr != nil || restore == nil {
		t.Fatalf("config failure was not injected: %v", hookErr)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if code == 0 || !strings.Contains(out, `"outcome":"failed"`) {
		t.Fatalf("set unexpectedly succeeded: exit=%d out=%s", code, out)
	}
	if got := adapter.observed["db"].Artifact; !bytes.Equal(got, originalArtifact) {
		t.Fatalf("failed set did not restore the native definition: %q", got)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, connection := configstore.Find(&document, "db")
	if connection == nil || !strings.Contains(string(connection.Spec.Config), "1080") {
		t.Fatalf("failed set changed the declaration: %#v", connection)
	}
}

func TestRemoveRestoresNativeStateWhenConfigWriteFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	originalArtifact := append([]byte(nil), adapter.observed["db"].Artifact...)
	var restore func() error
	var hookErr error
	adapter.removeHook = func(service.Reference) {
		restore, hookErr = replaceDirectoryWithBlocker(filepath.Join(home, "config"))
	}
	code, out, _ := invoke(t, "remove", "db", "--json")
	if hookErr != nil || restore == nil {
		t.Fatalf("config failure was not injected: %v", hookErr)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if code == 0 || !strings.Contains(out, `"outcome":"failed"`) {
		t.Fatalf("remove unexpectedly succeeded: exit=%d out=%s", code, out)
	}
	if got := adapter.observed["db"].Artifact; !bytes.Equal(got, originalArtifact) {
		t.Fatalf("failed remove did not restore the native definition: %q", got)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := records.Load("db"); err != nil || !exists {
		t.Fatalf("failed remove did not restore the service record: exists=%t err=%v", exists, err)
	}
}

func TestImportRollsBackItemWhenConfigWriteFails(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	source := filepath.Join(root, "source.json")
	document := configstore.Empty()
	document.Items = append(document.Items, core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: "db"},
		Spec: core.ConnectionSpec{Driver: "io.tunlite.ssh", Config: json.RawMessage(`{"host":"me@example.com","port":22,"forwards":[{"type":"dynamic","bind":"127.0.0.1","srcPort":1080}]}`)},
	})
	if err := configstore.New(source).Write(context.Background(), document, false); err != nil {
		t.Fatal(err)
	}
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	adapter.materializeHook = func(service.Artifact) {
		_ = os.WriteFile(filepath.Join(home, "config"), []byte("blocks the config directory\n"), 0o600)
	}
	invoke := useFakeServiceAdapter(t, adapter)
	code, out, _ := invoke(t, "import", source, "--json")
	if code != 1 || !strings.Contains(out, `"outcome":"failed"`) || !strings.Contains(out, `"code":"import_apply_failed"`) {
		t.Fatalf("import exit=%d out=%s", code, out)
	}
	if _, exists := adapter.artifacts["db"]; exists {
		t.Fatal("failed import left the native service materialized")
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := records.Load("db"); err != nil || exists {
		t.Fatalf("failed import left a service record: exists=%t err=%v", exists, err)
	}
}

func TestManageRemovesBindingWhenConfigWriteFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "native", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "unmanage", "native", "--json"); code != 0 {
		t.Fatalf("unmanage exit=%d out=%s err=%s", code, out, stderr)
	}
	var restore func() error
	var hookErr error
	adapter.discoverHook = func(call int) {
		if call == 2 {
			restore, hookErr = replaceDirectoryWithBlocker(filepath.Join(home, "config"))
		}
	}
	code, out, _ := invoke(t, "manage", "fake-native", "--name", "managed", "--json", "--non-interactive")
	if hookErr != nil || restore == nil {
		t.Fatalf("config failure was not injected: calls=%d err=%v", adapter.discoverCalls, hookErr)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if code == 0 || !strings.Contains(out, `"outcome":"failed"`) {
		t.Fatalf("manage unexpectedly succeeded: exit=%d out=%s", code, out)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := records.Load("managed"); err != nil || exists {
		t.Fatalf("failed manage left a service binding: exists=%t err=%v", exists, err)
	}
	if _, exists := adapter.artifacts["native"]; !exists {
		t.Fatal("failed manage changed the native service")
	}
}

func TestUnmanageRestoresDeclarationWhenRecordRemovalFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	config, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	index, connection := configstore.Find(&document, "db")
	removed := cloneConnection(*connection)
	if err := config.Update(context.Background(), func(document *configstore.Document) error {
		currentIndex, _ := configstore.Find(document, "db")
		document.Items = append(document.Items[:currentIndex], document.Items[currentIndex+1:]...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected record removal failure")
	err = completeUnmanage(context.Background(), config, "db", index, removed, func(string) error { return injected })
	if !errors.Is(err, injected) {
		t.Fatalf("unmanage completion returned %v", err)
	}
	restored, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, current := configstore.Find(&restored, "db"); current == nil || !connectionsEqual(*current, removed) {
		t.Fatalf("declaration was not restored: %#v", restored.Items)
	}
}

func TestResolveSystemRestoresDeclarationWhenRecordWriteFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	config, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, current := configstore.Find(&document, "db")
	previous := cloneConnection(*current)
	updated := cloneConnection(previous)
	updated.Spec.Config = bytes.ReplaceAll(updated.Spec.Config, []byte("1080"), []byte("1081"))
	if err := config.Update(context.Background(), func(document *configstore.Document) error {
		_, current := configstore.Find(document, "db")
		*current = updated
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists {
		t.Fatalf("record exists=%t err=%v", exists, err)
	}
	injected := errors.New("injected record write failure")
	err = writeResolvedRecord(context.Background(), config, "db", previous, updated, record,
		func(context.Context, service.Record) error { return injected })
	if !errors.Is(err, injected) {
		t.Fatalf("resolve completion returned %v", err)
	}
	restored, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, current := configstore.Find(&restored, "db"); current == nil || !connectionsEqual(*current, previous) {
		t.Fatalf("declaration was not restored: %#v", restored.Items)
	}
}

func TestResolveSystemReportsBlockedRecoveryAfterConcurrentChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	config, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, current := configstore.Find(&document, "db")
	previous := cloneConnection(*current)
	updated := cloneConnection(previous)
	updated.Spec.Config = bytes.ReplaceAll(updated.Spec.Config, []byte("1080"), []byte("1081"))
	if err := config.Update(context.Background(), func(document *configstore.Document) error {
		_, current := configstore.Find(document, "db")
		*current = updated
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := records.Load("db")
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected record write failure")
	err = writeResolvedRecord(context.Background(), config, "db", previous, updated, record,
		func(ctx context.Context, _ service.Record) error {
			if updateErr := config.Update(ctx, func(document *configstore.Document) error {
				_, current := configstore.Find(document, "db")
				current.Metadata.Group = "concurrent"
				return nil
			}); updateErr != nil {
				return updateErr
			}
			return injected
		})
	var typed *exitError
	if !errors.As(err, &typed) || typed.kind != "recovery_blocked" || typed.riskLevel != "high" || len(typed.actions) == 0 {
		t.Fatalf("blocked recovery was not structured: %#v err=%v", typed, err)
	}
}

func TestRenameBindingRollbackRestoresOriginalRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "old", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	config, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := stageRenamedConnectionService(context.Background(), "old", "new", config.Path(), options{
		serviceAdapter: func() (service.Adapter, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.finish()
	if err := mutation.commit(); err != nil {
		t.Fatal(err)
	}
	if err := mutation.rollback(); err != nil {
		t.Fatal(err)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := records.Load("old"); err != nil || !exists {
		t.Fatalf("old record was not restored: exists=%t err=%v", exists, err)
	}
	if _, exists, err := records.Load("new"); err != nil || exists {
		t.Fatalf("new record survived rollback: exists=%t err=%v", exists, err)
	}
}

func TestLifecycleStateWriteFailureReportsRollbackFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, connection := configstore.Find(&document, "db")
	if connection == nil {
		t.Fatal("connection is missing")
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	adapter.failures["stop"] = []error{errors.New("injected rollback stop failure")}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result := startService(canceled, adapter, records, *connection, store.Path(), options{})
	if result.Error == nil || !strings.Contains(result.Error.Message, context.Canceled.Error()) ||
		!strings.Contains(result.Error.Message, "injected rollback stop failure") {
		t.Fatalf("rollback errors were not preserved: %#v", result.Error)
	}
	if result.Error.Code != "recovery_blocked" || result.Error.RiskLevel != "high" || len(result.Error.Actions) == 0 {
		t.Fatalf("failed recovery was not a structured blocked result: %#v", result.Error)
	}
}

func TestLifecycleStateWriteFailureRestoresPreviousMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "start", "db", "--json"); code != 0 {
		t.Fatalf("start exit=%d out=%s err=%s", code, out, stderr)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, connection := configstore.Find(&document, "db")
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result := enableService(canceled, adapter, records, *connection, store.Path(), options{})
	if result.Error == nil || result.Error.Code != "service_state_write_failed" {
		t.Fatalf("enable unexpectedly succeeded: %#v", result)
	}
	observation := adapter.observed["db"]
	if observation.ProcessState != service.ProcessRunning || observation.AutoStart {
		t.Fatalf("failed enable did not restore background mode: %#v", observation)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists || record.Mode != service.ModeBackground {
		t.Fatalf("background record was not preserved: record=%#v exists=%v err=%v", record, exists, err)
	}

	if code, out, stderr := invoke(t, "stop", "db", "--json"); code != 0 {
		t.Fatalf("stop exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "enable", "db", "--json"); code != 0 {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	canceled, cancel = context.WithCancel(context.Background())
	cancel()
	result = disableService(canceled, adapter, records, *connection, store.Path(), options{})
	if result.Error == nil || result.Error.Code != "service_state_write_failed" {
		t.Fatalf("disable unexpectedly succeeded: %#v", result)
	}
	observation = adapter.observed["db"]
	if observation.ProcessState != service.ProcessRunning || !observation.AutoStart {
		t.Fatalf("failed disable did not restore persistent mode: %#v", observation)
	}
	record, exists, err = records.Load("db")
	if err != nil || !exists || record.Mode != service.ModePersistent {
		t.Fatalf("persistent record was not preserved: record=%#v exists=%v err=%v", record, exists, err)
	}
}

func TestLifecycleOperationFailureRestoresPreviousMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}

	adapter.failures["start"] = []error{errors.New("injected start failure")}
	if code, out, _ := invoke(t, "start", "db", "--json"); code == 0 || !strings.Contains(out, "injected start failure") {
		t.Fatalf("start exit=%d out=%s", code, out)
	}
	if observation := adapter.observed["db"]; observation.ProcessState != service.ProcessInactive || observation.AutoStart {
		t.Fatalf("failed start did not restore stopped mode: %#v", observation)
	}

	if code, out, stderr := invoke(t, "start", "db", "--json"); code != 0 {
		t.Fatalf("start exit=%d out=%s err=%s", code, out, stderr)
	}
	adapter.failures["stop"] = []error{errors.New("injected stop failure")}
	if code, out, _ := invoke(t, "stop", "db", "--json"); code == 0 || !strings.Contains(out, "injected stop failure") {
		t.Fatalf("stop exit=%d out=%s", code, out)
	}
	if observation := adapter.observed["db"]; observation.ProcessState != service.ProcessRunning || observation.AutoStart {
		t.Fatalf("failed stop did not restore background mode: %#v", observation)
	}

	if code, out, stderr := invoke(t, "stop", "db", "--json"); code != 0 {
		t.Fatalf("stop exit=%d out=%s err=%s", code, out, stderr)
	}
	adapter.failures["enable"] = []error{errors.New("injected enable failure")}
	if code, out, _ := invoke(t, "enable", "db", "--json"); code == 0 || !strings.Contains(out, "injected enable failure") {
		t.Fatalf("enable exit=%d out=%s", code, out)
	}
	if observation := adapter.observed["db"]; observation.ProcessState != service.ProcessInactive || observation.AutoStart {
		t.Fatalf("failed enable did not restore stopped mode: %#v", observation)
	}

	if code, out, stderr := invoke(t, "enable", "db", "--json"); code != 0 {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	adapter.failures["disable"] = []error{errors.New("injected disable failure")}
	if code, out, _ := invoke(t, "disable", "db", "--json"); code == 0 || !strings.Contains(out, "injected disable failure") {
		t.Fatalf("disable exit=%d out=%s", code, out)
	}
	if observation := adapter.observed["db"]; observation.ProcessState != service.ProcessRunning || !observation.AutoStart {
		t.Fatalf("failed disable did not restore persistent mode: %#v", observation)
	}
}

func TestRemoveFailureRestoresNativeDefinitionAndKeepsDeclaration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	original := append([]byte(nil), adapter.observed["db"].Artifact...)
	adapter.failures["remove"] = []error{errors.New("injected remove failure")}
	if code, out, _ := invoke(t, "remove", "db", "--json"); code == 0 || !strings.Contains(out, "injected remove failure") {
		t.Fatalf("remove exit=%d out=%s", code, out)
	}
	if got := adapter.observed["db"].Artifact; !bytes.Equal(got, original) {
		t.Fatalf("failed remove did not restore native definition: %q", got)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, connection := configstore.Find(&document, "db"); connection == nil {
		t.Fatal("failed remove deleted the declaration")
	}
}

func TestStatusReportsAnUnboundNativeIdentityInsteadOfCallingItAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Remove("db"); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := invoke(t, "status", "db", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"serviceState":"registered"`) ||
		!strings.Contains(out, `"consistencyState":"blocked"`) || !strings.Contains(out, `"code":"service_binding_missing"`) ||
		!strings.Contains(out, `"type":"resolve_connection"`) {
		t.Fatalf("status exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, _ := invoke(t, "remove", "db", "--json"); code == 0 || !strings.Contains(out, "management binding is missing") {
		t.Fatalf("remove bypassed the missing binding: exit=%d out=%s", code, out)
	}
	if code, out, stderr := invoke(t, "resolve", "db", "--use", "tunlite", "--json"); code != 0 {
		t.Fatalf("resolve exit=%d out=%s err=%s", code, out, stderr)
	}
	if record, exists, err := records.Load("db"); err != nil || !exists || record.BaselineDigest == "" {
		t.Fatalf("resolve did not recreate the binding: record=%#v exists=%v err=%v", record, exists, err)
	}
	if code, out, stderr := invoke(t, "status", "db", "--json"); code != 0 || stderr != "" || !strings.Contains(out, `"consistencyState":"consistent"`) {
		t.Fatalf("resolved status exit=%d out=%s err=%s", code, out, stderr)
	}
}

func TestResolveMissingBindingCanUseSystemDefinition(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Remove("db"); err != nil {
		t.Fatal(err)
	}
	artifact := adapter.artifacts["db"]
	for index, argument := range artifact.Plan.Exec.Args {
		artifact.Plan.Exec.Args[index] = strings.ReplaceAll(argument, "1080", "1081")
	}
	artifact.Content, err = json.Marshal(artifact.Plan)
	if err != nil {
		t.Fatal(err)
	}
	adapter.artifacts["db"] = artifact
	observation := adapter.observed["db"]
	observation.Artifact = append([]byte(nil), artifact.Content...)
	adapter.observed["db"] = observation

	if code, out, stderr := invoke(t, "resolve", "db", "--use", "system", "--json"); code != 0 {
		t.Fatalf("resolve exit=%d out=%s err=%s", code, out, stderr)
	}
	store, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, connection := configstore.Find(&document, "db")
	if connection == nil || !strings.Contains(string(connection.Spec.Config), "1081") {
		t.Fatalf("system definition was not adopted: %#v", connection)
	}
	if code, out, stderr := invoke(t, "status", "db", "--json"); code != 0 || stderr != "" || !strings.Contains(out, `"consistencyState":"consistent"`) {
		t.Fatalf("resolved status exit=%d out=%s err=%s", code, out, stderr)
	}
}
