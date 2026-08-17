package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

type runnerCall struct {
	command string
	args    []string
}

type fakeRunner struct {
	calls []runnerCall
	run   func(string, []string) ([]byte, error)
}

type observationAdapter struct {
	observation Observation
	err         error
}

func (a observationAdapter) Name() string { return "systemd" }
func (a observationAdapter) Build(string, plan.ExecutionPlan) (Artifact, error) {
	return Artifact{}, nil
}
func (a observationAdapter) Target(name string) Artifact {
	return Artifact{Connection: name, Adapter: a.Name()}
}
func (a observationAdapter) Enable(context.Context, Artifact) error { return nil }
func (a observationAdapter) Disable(context.Context, string) error  { return nil }
func (a observationAdapter) Restart(context.Context, string) error  { return nil }
func (a observationAdapter) Inspect(context.Context, string) (Observation, error) {
	return a.observation, a.err
}
func (a observationAdapter) Logs(context.Context, string, int, bool, Streams) error { return nil }

func (f *fakeRunner) Run(_ context.Context, command string, args []string, streams Streams) ([]byte, error) {
	f.calls = append(f.calls, runnerCall{command: command, args: append([]string(nil), args...)})
	var output []byte
	var err error
	if f.run != nil {
		output, err = f.run(command, args)
	}
	if streams.Out != nil {
		_, _ = streams.Out.Write(output)
	}
	return output, err
}

func TestSystemdBuildUsesArgvAndEscapesManagerExpansion(t *testing.T) {
	adapter := NewSystemd(t.TempDir(), &fakeRunner{})
	value := testPlan()
	value.Exec.Args = []string{"literal $HOME", "%n", `say"hi`}
	artifact, err := adapter.Build("db", value)
	if err != nil {
		t.Fatal(err)
	}
	content := string(artifact.Content)
	for _, expected := range []string{`ExecStart="/usr/bin/provider"`, `"literal $$HOME"`, `"%%n"`, `"say\"hi"`} {
		if !strings.Contains(content, expected) {
			t.Fatalf("unit missing %q:\n%s", expected, content)
		}
	}
	if strings.Contains(content, "/bin/sh") {
		t.Fatal("systemd unit introduced a shell")
	}
}

func TestSystemdEnableInstallsUnitAndCallsNativeManager(t *testing.T) {
	runner := &fakeRunner{}
	adapter := NewSystemd(t.TempDir(), runner)
	artifact, err := adapter.Build("db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(artifact.Path)
	if err != nil || !bytes.Equal(raw, artifact.Content) {
		t.Fatalf("unit was not installed exactly: %v", err)
	}
	want := []string{"daemon-reload", "enable", "restart"}
	if len(runner.calls) != len(want) {
		t.Fatalf("calls=%#v", runner.calls)
	}
	for index, command := range want {
		if runner.calls[index].args[1] != command {
			t.Fatalf("call %d=%#v, want %s", index, runner.calls[index], command)
		}
	}
}

func TestSystemdEnableRestoresPreviousArtifactOnFailure(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		if len(args) > 1 && args[1] == "restart" {
			return nil, errors.New("restart failed")
		}
		return nil, nil
	}}
	adapter := NewSystemd(t.TempDir(), runner)
	artifact, err := adapter.Build("db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact.Path, []byte("old unit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(context.Background(), artifact); err == nil {
		t.Fatal("enable unexpectedly succeeded")
	}
	raw, err := os.ReadFile(artifact.Path)
	if err != nil || string(raw) != "old unit\n" {
		t.Fatalf("rollback content=%q err=%v", raw, err)
	}
}

func TestSystemdFreshEnableDisablesBeforeRemovingFailedUnit(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		if len(args) > 1 && args[1] == "restart" {
			return nil, errors.New("restart failed")
		}
		return nil, nil
	}}
	adapter := NewSystemd(t.TempDir(), runner)
	artifact, err := adapter.Build("db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(context.Background(), artifact); err == nil {
		t.Fatal("enable unexpectedly succeeded")
	}
	if _, err := os.Stat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed fresh unit was not removed: %v", err)
	}
	commands := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		commands = append(commands, call.args[1])
	}
	want := []string{"daemon-reload", "enable", "restart", "disable", "daemon-reload"}
	if strings.Join(commands, ",") != strings.Join(want, ",") {
		t.Fatalf("rollback calls=%v want=%v", commands, want)
	}
}

func TestSystemdInspectMapsNativeState(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		switch args[1] {
		case "is-enabled":
			return []byte("enabled\n"), nil
		case "show":
			return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nResult=success\n"), nil
		}
		return nil, nil
	}}
	adapter := NewSystemd(t.TempDir(), runner)
	artifact, _ := adapter.Build("db", testPlan())
	if err := os.WriteFile(artifact.Path, artifact.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := adapter.Inspect(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Installed || !observation.Enabled || observation.ProcessState != ProcessRunning {
		t.Fatalf("unexpected observation: %#v", observation)
	}
}

func TestLaunchdBuildIsValidXMLAndUsesProgramArguments(t *testing.T) {
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, &fakeRunner{})
	value := testPlan()
	value.Exec.Args = []string{"a&b", "<literal>"}
	artifact, err := adapter.Build("db", value)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(artifact.Content))
	for {
		if _, err := decoder.Token(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("invalid plist XML: %v\n%s", err, artifact.Content)
		}
	}
	content := string(artifact.Content)
	if !strings.Contains(content, "<key>ProgramArguments</key>") || !strings.Contains(content, "a&amp;b") || strings.Contains(content, "/bin/sh") {
		t.Fatalf("unexpected plist:\n%s", content)
	}
}

func TestLaunchdBuildAcceptsEveryValidConnectionNameShape(t *testing.T) {
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, &fakeRunner{})
	artifact, err := adapter.Build("a--b", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(artifact.Content))
	for {
		if _, err := decoder.Token(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("valid connection name produced invalid plist: %v\n%s", err, artifact.Content)
		}
	}
}

func TestLaunchdBuildRejectsXMLIncompatibleArguments(t *testing.T) {
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, &fakeRunner{})
	value := testPlan()
	value.Exec.Args = []string{"bad\x01argument"}
	if _, err := adapter.Build("db", value); err == nil {
		t.Fatal("launchd accepted an XML-incompatible argument")
	}
}

func TestLaunchdMapsActiveStateToRunning(t *testing.T) {
	properties := parseLaunchdProperties([]byte("state = active\npid = 123\n"))
	if properties["state"] != "active" {
		t.Fatalf("unexpected properties: %#v", properties)
	}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		if args[0] == "print" {
			return []byte("state = active\npid = 123\n"), nil
		}
		return nil, nil
	}})
	observation, err := adapter.Inspect(context.Background(), "db")
	if err != nil || observation.ProcessState != ProcessRunning {
		t.Fatalf("observation=%#v err=%v", observation, err)
	}
}

func TestLaunchdPlistPassesNativeValidation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native plutil validation is only available on macOS")
	}
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil is unavailable")
	}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, &fakeRunner{})
	artifact, err := adapter.Build("db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact.Path, artifact.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(plutil, "-lint", "--", artifact.Path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("plutil rejected generated plist: %v\n%s", err, output)
	}
}

func TestServiceStoreRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	record := Record{APIVersion: RecordVersion, Connection: "db", Adapter: "systemd", DesiredState: DesiredEnabled, UnitName: "tunlite-db.service"}
	if err := store.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := store.Load("db")
	if err != nil || !exists || loaded.UnitName != record.UnitName {
		t.Fatalf("loaded=%#v exists=%t err=%v", loaded, exists, err)
	}
	info, err := os.Stat(filepath.Join(store.root, "db.json"))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state permissions=%v err=%v", info.Mode(), err)
	}
}

func TestEvaluateDistinguishesMissingDriftedAndBlocked(t *testing.T) {
	expected := Artifact{Connection: "db", Adapter: "systemd", Content: []byte("expected"), Plan: testPlan()}
	record := Record{APIVersion: RecordVersion, Connection: "db", Adapter: "systemd", DesiredState: DesiredEnabled}

	missing := Evaluate(context.Background(), observationAdapter{observation: Observation{ProcessState: ProcessInactive}}, record, &expected)
	if missing.SyncState != SyncMissing || missing.DesiredState != DesiredEnabled {
		t.Fatalf("missing state=%#v", missing)
	}
	drifted := Evaluate(context.Background(), observationAdapter{observation: Observation{
		Installed: true, Enabled: true, ProcessState: ProcessRunning, Artifact: []byte("changed"),
	}}, record, &expected)
	if drifted.SyncState != SyncDrifted {
		t.Fatalf("drifted state=%#v", drifted)
	}
	blocked := Evaluate(context.Background(), observationAdapter{err: errors.New("manager unavailable")}, record, &expected)
	if blocked.SyncState != SyncBlocked || blocked.ProcessState != ProcessUnknown {
		t.Fatalf("blocked state=%#v", blocked)
	}
}

func TestServiceStoreSerializesConnectionOperations(t *testing.T) {
	store := NewStore(t.TempDir())
	release, err := store.Lock(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := store.Lock(ctx, "db"); err == nil {
		t.Fatal("second service operation acquired the same connection lock")
	}
}

func testPlan() plan.ExecutionPlan {
	return plan.ExecutionPlan{
		Mode:         plan.ModeDirect,
		Exec:         plan.Exec{Program: "/usr/bin/provider", Args: []string{"--flag", "value"}, Environment: map[string]string{}},
		Lifecycle:    plan.Lifecycle{Restart: "on-failure", RestartDelaySeconds: 5, StopTimeoutSeconds: 3},
		Endpoints:    []plan.Endpoint{{Name: "default", Direction: "access", Protocol: "tcp", Address: "127.0.0.1:12345"}},
		HealthChecks: []plan.HealthCheck{}, Redactions: []plan.Redaction{},
	}
}
