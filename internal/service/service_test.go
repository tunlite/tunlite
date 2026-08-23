package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
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

type observationAdapter struct {
	observation Observation
	err         error
}

func (a observationAdapter) Name() string { return "systemd" }
func (a observationAdapter) Capabilities() Capabilities {
	return Capabilities{Registration: true, Background: true, Persistence: true, Logs: true, Discovery: true}
}
func (a observationAdapter) Build(string, plan.ExecutionPlan) (Artifact, error) {
	return Artifact{}, nil
}
func (a observationAdapter) BuildBound(Reference, plan.ExecutionPlan) (Artifact, error) {
	return Artifact{}, nil
}
func (a observationAdapter) Target(name string) Artifact {
	return Artifact{Connection: name, Adapter: a.Name()}
}
func (a observationAdapter) Materialize(context.Context, Artifact) error { return nil }
func (a observationAdapter) Remove(context.Context, Reference) error     { return nil }
func (a observationAdapter) Start(context.Context, Reference) error      { return nil }
func (a observationAdapter) Stop(context.Context, Reference) error       { return nil }
func (a observationAdapter) Restart(context.Context, Reference) error    { return nil }
func (a observationAdapter) Enable(context.Context, Reference) error     { return nil }
func (a observationAdapter) Disable(context.Context, Reference) error    { return nil }
func (a observationAdapter) Inspect(context.Context, Reference) (Observation, error) {
	return a.observation, a.err
}
func (a observationAdapter) Logs(context.Context, Reference, int, bool, Streams) error {
	return nil
}
func (a observationAdapter) Discover(context.Context) ([]Candidate, error) { return []Candidate{}, nil }

func TestSystemdBuildUsesArgvAndEscapesManagerExpansion(t *testing.T) {
	adapter := NewSystemd(t.TempDir(), &fakeRunner{})
	value := testPlan()
	value.Exec.Args = []string{"literal $HOME", "%n", `say"hi`}
	artifact, err := adapter.Build("db", value)
	if err != nil {
		t.Fatal(err)
	}
	content := string(artifact.Content)
	for _, expected := range []string{`ExecStart="/usr/bin/provider"`, `"literal $$HOME"`, `"%%n"`, `"say\"hi"`, embeddedPlanPrefix} {
		if !strings.Contains(content, expected) {
			t.Fatalf("unit missing %q:\n%s", expected, content)
		}
	}
	if strings.Contains(content, "/bin/sh") {
		t.Fatal("systemd unit introduced a shell")
	}
}

func TestSystemdSeparatesMaterializeStartAndEnable(t *testing.T) {
	runner := &fakeRunner{}
	adapter := NewSystemd(t.TempDir(), runner)
	artifact, err := adapter.Build("db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Materialize(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(artifact.Path)
	if err != nil || !bytes.Equal(raw, artifact.Content) {
		t.Fatalf("unit was not materialized exactly: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0].args[1] != "daemon-reload" {
		t.Fatalf("materialize unexpectedly changed runtime state: %#v", runner.calls)
	}
	if err := adapter.Start(context.Background(), artifact.Reference()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(context.Background(), artifact.Reference()); err != nil {
		t.Fatal(err)
	}
	if runner.calls[1].args[1] != "start" || runner.calls[2].args[1] != "enable" || runner.calls[2].args[2] != "--now" {
		t.Fatalf("unexpected lifecycle calls: %#v", runner.calls)
	}
}

func TestSystemdMaterializeRestoresPreviousArtifactOnReloadFailure(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		if len(args) > 1 && args[1] == "daemon-reload" {
			return nil, errors.New("reload failed")
		}
		return nil, nil
	}}
	adapter := NewSystemd(t.TempDir(), runner)
	artifact, _ := adapter.Build("db", testPlan())
	if err := os.WriteFile(artifact.Path, []byte("old unit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Materialize(context.Background(), artifact); err == nil {
		t.Fatal("materialize unexpectedly succeeded")
	}
	raw, _ := os.ReadFile(artifact.Path)
	if string(raw) != "old unit\n" {
		t.Fatalf("rollback content=%q", raw)
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
	observation, err := adapter.Inspect(context.Background(), artifact.Reference())
	if err != nil || !observation.Installed || !observation.AutoStart || observation.ProcessState != ProcessRunning {
		t.Fatalf("observation=%#v err=%v", observation, err)
	}
}

func TestSystemdParsesRecognizableNativeSSHService(t *testing.T) {
	raw := []byte(`[Unit]
Description=existing ssh tunnel
After=network-online.target
[Service]
Type=simple
ExecStart=/usr/bin/ssh -N -T -D 127.0.0.1:1080 -- me@example.com
Restart=on-failure
RestartSec=10
TimeoutStopSec=5
[Install]
WantedBy=default.target
`)
	value, err := parseSystemdExecutionPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if value.Exec.Program != "/usr/bin/ssh" || len(value.Exec.Args) == 0 {
		t.Fatalf("plan=%#v", value)
	}
}

func TestSystemdRejectsScheduledOrWrappedService(t *testing.T) {
	raw := []byte(`[Service]
ExecStartPre=/bin/true
ExecStart=/usr/bin/ssh -N -D 1080 -- me@example.com
Restart=on-failure
`)
	if _, err := parseSystemdExecutionPlan(raw); err == nil {
		t.Fatal("wrapped service was accepted")
	}
}

func TestLaunchdBuildIsValidXMLAndMaterializeDoesNotStart(t *testing.T) {
	runner := &fakeRunner{}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, runner)
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
	if err := adapter.Materialize(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || runner.calls[0].args[0] != "disable" {
		t.Fatalf("materialize unexpectedly loaded the job: %#v", runner.calls)
	}
}

func TestLaunchdBackgroundStartLeavesJobDisabledForNextLogin(t *testing.T) {
	runner := &fakeRunner{}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, runner)
	artifact, _ := adapter.Build("db", testPlan())
	if err := adapter.Start(context.Background(), artifact.Reference()); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		commands = append(commands, call.args[0])
	}
	if strings.Join(commands, ",") != "print-disabled,print,enable,bootstrap,kickstart,disable" {
		t.Fatalf("unexpected launchd background sequence: %v", commands)
	}
}

func TestLaunchdEnableRunningBackgroundJobDoesNotRestartIt(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		switch args[0] {
		case "print-disabled":
			return []byte(`"dev.tunlite.connection.db" => true`), nil
		case "print":
			return []byte("state = running\npid = 42\n"), nil
		default:
			return nil, nil
		}
	}}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, runner)
	artifact, _ := adapter.Build("db", testPlan())
	if err := adapter.Enable(context.Background(), artifact.Reference()); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		commands = append(commands, call.args[0])
	}
	if strings.Join(commands, ",") != "print-disabled,print,enable" {
		t.Fatalf("enable restarted a running background job: %v", commands)
	}
}

func TestLaunchdParsesCurrentAndAlternateDisabledOutput(t *testing.T) {
	label := "dev.tunlite.connection.db"
	for _, output := range []string{
		"disabled services = {\n\t\"dev.tunlite.connection.db\" => disabled\n}",
		"disabled services = {\n\t\"dev.tunlite.connection.db\" => true\n}",
	} {
		if !launchdLabelDisabled([]byte(output), label) {
			t.Fatalf("disabled label was not recognized: %s", output)
		}
	}
	if launchdLabelDisabled([]byte(`"dev.tunlite.connection.db" => enabled`), label) {
		t.Fatal("enabled label was reported as disabled")
	}
}

func TestLaunchdInspectDoesNotGuessWhenPersistenceStateIsUnavailable(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		if args[0] == "print-disabled" {
			return nil, errors.New("permission denied")
		}
		return []byte("state = running\npid = 42\n"), nil
	}}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, runner)
	artifact, _ := adapter.Build("db", testPlan())
	if _, err := adapter.Inspect(context.Background(), artifact.Reference()); err == nil || !strings.Contains(err.Error(), "persistence state") {
		t.Fatalf("launchd persistence failure was hidden: %v", err)
	}
}

func TestLaunchdInspectTreatsXPCProxyAsStarting(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		switch args[0] {
		case "print-disabled":
			return []byte(`"dev.tunlite.connection.db" => true`), nil
		case "print":
			return []byte("state = xpcproxy\npid = 42\n"), nil
		default:
			return nil, nil
		}
	}}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, runner)
	artifact, _ := adapter.Build("db", testPlan())
	observation, err := adapter.Inspect(context.Background(), artifact.Reference())
	if err != nil {
		t.Fatal(err)
	}
	if observation.ProcessState != ProcessStarting {
		t.Fatalf("xpcproxy state=%q, want %q", observation.ProcessState, ProcessStarting)
	}
}

func TestLaunchdMissingServiceIsNotReportedAsPersistent(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		switch args[0] {
		case "print-disabled":
			return []byte("disabled services = {}\n"), nil
		case "print":
			return []byte("Could not find service"), errors.New("not found")
		default:
			return nil, nil
		}
	}}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, runner)
	observation, err := adapter.Inspect(context.Background(), adapter.Target("unused").Reference())
	if err != nil {
		t.Fatal(err)
	}
	if observation.AutoStart || observation.ProcessState != ProcessInactive {
		t.Fatalf("missing service observation=%#v", observation)
	}
}

func TestLaunchdStartRollsBackWhenBootstrapFails(t *testing.T) {
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		if args[0] == "print-disabled" {
			return []byte(`"dev.tunlite.connection.db" => true`), nil
		}
		if args[0] == "print" {
			return []byte("Could not find service"), errors.New("not loaded")
		}
		if args[0] == "bootstrap" {
			return nil, errors.New("bootstrap failed")
		}
		return nil, nil
	}}
	adapter := NewLaunchd(t.TempDir(), t.TempDir(), 501, runner)
	artifact, _ := adapter.Build("db", testPlan())
	if err := adapter.Start(context.Background(), artifact.Reference()); err == nil {
		t.Fatal("start unexpectedly succeeded")
	}
	commands := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		commands = append(commands, call.args[0])
	}
	if !strings.HasSuffix(strings.Join(commands, ","), "bootstrap,disable,bootout") {
		t.Fatalf("failed start did not restore stopped state: %v", commands)
	}
}

func TestSystemdRejectsDropInsAndManagerExpansion(t *testing.T) {
	raw := []byte(`[Service]
ExecStart=/usr/bin/ssh -N -D 1080 -- user@%H
Restart=on-failure
[Install]
WantedBy=default.target
`)
	if _, err := parseSystemdExecutionPlan(raw); err == nil {
		t.Fatal("systemd specifier expansion was accepted")
	}

	unitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unitDir, "native.service"), []byte(`[Service]
ExecStart=/usr/bin/ssh -N -D 1080 -- user@example.com
Restart=on-failure
[Install]
WantedBy=default.target
`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{run: func(_ string, args []string) ([]byte, error) {
		switch args[1] {
		case "is-enabled":
			return []byte("disabled\n"), errors.New("disabled")
		case "show":
			return []byte("LoadState=loaded\nActiveState=inactive\nDropInPaths=[/tmp/override.conf]\n"), nil
		}
		return nil, nil
	}}
	candidates, err := NewSystemd(unitDir, runner).Discover(context.Background())
	if err != nil || len(candidates) != 0 {
		t.Fatalf("drop-in service was discoverable: candidates=%v err=%v", candidates, err)
	}
}

func TestLaunchdParsesRecognizableNativeSSHService(t *testing.T) {
	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>example.ssh.db</string>
<key>ProgramArguments</key><array><string>/usr/bin/ssh</string><string>-N</string><string>-D</string><string>1080</string><string>--</string><string>me@example.com</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>StandardOutPath</key><string>/tmp/example.out</string>
<key>StandardErrorPath</key><string>/tmp/example.err</string>
</dict></plist>`)
	value, label, logs, err := parseLaunchdExecutionPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if value.Exec.Program != "/usr/bin/ssh" || label != "example.ssh.db" || len(logs) != 2 {
		t.Fatalf("plan=%#v label=%q logs=%v", value, label, logs)
	}
}

func TestServiceStoreRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	record := Record{RecordRevision: CurrentRecordRevision, Connection: "db", ServiceKey: "db", Adapter: "systemd", Mode: ModeStopped, UnitName: "tunlite-db.service"}
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

func TestServiceStoreRejectsUnknownPreviewRecord(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	unknown := []byte(`{"recordRevision":1,"connection":"db","serviceKey":"db","adapter":"systemd","mode":"stopped","unitName":"tunlite-db.service","unknown":true}`)
	if err := os.WriteFile(path, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(root)
	if _, _, err := store.Load("db"); err == nil {
		t.Fatal("unknown preview service record was accepted")
	}
}

func TestEvaluateReportsFiveIndependentDimensions(t *testing.T) {
	expected := Artifact{Connection: "db", Adapter: "systemd", Content: []byte("expected"), PlanDigest: "plan", Plan: testPlan()}
	record := Record{
		RecordRevision: CurrentRecordRevision, Connection: "db", Adapter: "systemd", Mode: ModePersistent,
		PlanDigest: "plan", BaselineDigest: ContentDigest(expected.Content),
	}
	state := Evaluate(context.Background(), observationAdapter{observation: Observation{
		Installed: true, AutoStart: true, ProcessState: ProcessRunning, Artifact: expected.Content,
	}}, record, &expected)
	if state.ServiceState != ServiceRegistered || state.Mode != ModePersistent || state.ProcessState != ProcessRunning || state.ConsistencyState != ConsistencyConsistent {
		t.Fatalf("state=%#v", state)
	}
	drifted := Evaluate(context.Background(), observationAdapter{observation: Observation{
		Installed: true, AutoStart: true, ProcessState: ProcessRunning, Artifact: []byte("changed"),
	}}, record, &expected)
	if drifted.ConsistencyState != ConsistencyDrifted {
		t.Fatalf("drifted=%#v", drifted)
	}
}

func TestEvaluateBlocksRecordsWithoutConsistencyBaseline(t *testing.T) {
	expected := Artifact{Connection: "db", Adapter: "systemd", Content: []byte("expected"), PlanDigest: "plan", Plan: testPlan()}
	record := Record{RecordRevision: CurrentRecordRevision, Connection: "db", Adapter: "systemd", Mode: ModeStopped, PlanDigest: "plan"}
	state := Evaluate(context.Background(), observationAdapter{observation: Observation{
		Installed: true, ProcessState: ProcessInactive, Artifact: expected.Content,
	}}, record, &expected)
	if state.ConsistencyState != ConsistencyBlocked || len(state.Conditions) == 0 || state.Conditions[0].Code != "service_baseline_missing" {
		t.Fatalf("state=%#v", state)
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
