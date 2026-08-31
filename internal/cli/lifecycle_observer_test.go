package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/tunlite/tunlite/internal/configstore"
	"github.com/tunlite/tunlite/internal/core"
	"github.com/tunlite/tunlite/internal/plan"
	"github.com/tunlite/tunlite/internal/service"
	"github.com/tunlite/tunlite/schemas"
)

type transitioningServiceAdapter struct {
	*fakeServiceAdapter
	after      int
	fail       bool
	missing    bool
	inspectErr error
	triggered  map[string]bool
	inspects   map[string]int
}

func newTransitioningServiceAdapter(base *fakeServiceAdapter, after int) *transitioningServiceAdapter {
	return &transitioningServiceAdapter{
		fakeServiceAdapter: base, after: after, triggered: map[string]bool{}, inspects: map[string]int{},
	}
}

func (adapter *transitioningServiceAdapter) markStarting(reference service.Reference, autoStart bool) {
	observation := adapter.observed[reference.Connection]
	if adapter.fail {
		observation.ProcessState = service.ProcessFailed
	} else {
		observation.ProcessState = service.ProcessStarting
	}
	observation.AutoStart = autoStart
	adapter.observed[reference.Connection] = observation
	adapter.triggered[reference.Connection] = true
}

func (adapter *transitioningServiceAdapter) Start(ctx context.Context, reference service.Reference) error {
	if err := adapter.fakeServiceAdapter.Start(ctx, reference); err != nil {
		return err
	}
	adapter.markStarting(reference, false)
	return nil
}

func (adapter *transitioningServiceAdapter) Enable(ctx context.Context, reference service.Reference) error {
	if err := adapter.fakeServiceAdapter.Enable(ctx, reference); err != nil {
		return err
	}
	adapter.markStarting(reference, true)
	return nil
}

func (adapter *transitioningServiceAdapter) Restart(ctx context.Context, reference service.Reference) error {
	if err := adapter.fakeServiceAdapter.Restart(ctx, reference); err != nil {
		return err
	}
	autoStart := adapter.observed[reference.Connection].AutoStart
	adapter.markStarting(reference, autoStart)
	return nil
}

func (adapter *transitioningServiceAdapter) Inspect(ctx context.Context, reference service.Reference) (service.Observation, error) {
	if adapter.triggered[reference.Connection] {
		if adapter.inspectErr != nil {
			return service.Observation{}, adapter.inspectErr
		}
		if adapter.missing {
			return service.Observation{ProcessState: service.ProcessInactive, Platform: map[string]any{}}, nil
		}
		adapter.inspects[reference.Connection]++
		if adapter.inspects[reference.Connection] >= adapter.after {
			observation := adapter.observed[reference.Connection]
			observation.ProcessState = service.ProcessRunning
			adapter.observed[reference.Connection] = observation
		}
	}
	return adapter.fakeServiceAdapter.Inspect(ctx, reference)
}

type lifecycleResponse struct {
	Outcome string              `json:"outcome"`
	Results []serviceItemResult `json:"results"`
}

func runLifecycleCommandForTest(
	t *testing.T,
	adapter service.Adapter,
	budget lifecycleObservationBudget,
	command string,
	args ...string,
) (lifecycleResponse, error) {
	t.Helper()
	var output bytes.Buffer
	opts := options{
		JSON: true, lifecycleBudget: &budget,
		serviceAdapter: func() (service.Adapter, error) { return adapter, nil },
	}
	err := serviceCommand(context.Background(), command, args, IO{Out: &output, Err: &bytes.Buffer{}}, opts)
	validateCLIResponseSchema(t, output.Bytes())
	var response lifecycleResponse
	if decodeErr := json.Unmarshal(output.Bytes(), &response); decodeErr != nil {
		t.Fatalf("decode lifecycle response %q: %v", output.String(), decodeErr)
	}
	return response, err
}

func validateCLIResponseSchema(t *testing.T, raw []byte) {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	seen := map[string]bool{}
	for _, filename := range schemas.Names {
		if seen[filename] {
			continue
		}
		seen[filename] = true
		document, readErr := schemas.Files.ReadFile(filename)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var metadata struct {
			ID string `json:"$id"`
		}
		if decodeErr := json.Unmarshal(document, &metadata); decodeErr != nil || metadata.ID == "" {
			t.Fatalf("schema %s metadata: id=%q err=%v", filename, metadata.ID, decodeErr)
		}
		if addErr := compiler.AddResource(metadata.ID, bytes.NewReader(document)); addErr != nil {
			t.Fatalf("add schema %s: %v", filename, addErr)
		}
	}
	compiled, err := compiler.Compile("https://tunlite.dev/schemas/tunlite-cli-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var response any
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode response for schema validation: %v", err)
	}
	if err := compiled.Validate(response); err != nil {
		t.Fatalf("response does not satisfy CLI schema: %v\n%s", err, raw)
	}
}

func prepareLifecycleConnection(t *testing.T, base *fakeServiceAdapter, name string, port int) {
	t.Helper()
	invoke := useFakeServiceAdapter(t, base)
	forward := fmt.Sprintf("%d:localhost:%d", port, port)
	if code, out, stderr := invoke(t, "add", name, "-R", forward, "--json", "me@example.com"); code != 0 {
		t.Fatalf("add %s exit=%d out=%s err=%s", name, code, out, stderr)
	}
}

func TestLifecycleCommandsObserveSuccessfulConvergence(t *testing.T) {
	tests := []struct {
		command string
		setup   func(*testing.T, func(*testing.T, ...string) (int, string, string))
	}{
		{command: "start"},
		{command: "enable"},
		{command: "restart", setup: func(t *testing.T, invoke func(*testing.T, ...string) (int, string, string)) {
			if code, out, stderr := invoke(t, "start", "db", "--json"); code != 0 {
				t.Fatalf("setup start exit=%d out=%s err=%s", code, out, stderr)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			base := newFakeServiceAdapter(filepath.Join(home, "native"))
			prepareLifecycleConnection(t, base, "db", 1080)
			if test.setup != nil {
				test.setup(t, useFakeServiceAdapter(t, base))
			}
			adapter := newTransitioningServiceAdapter(base, 3)
			response, err := runLifecycleCommandForTest(t, adapter, lifecycleObservationBudget{
				Overall: time.Second, PerItem: 500 * time.Millisecond, PollInterval: time.Millisecond,
			}, test.command, "db")
			if err != nil || response.Outcome != "succeeded" || len(response.Results) != 1 {
				t.Fatalf("response=%#v err=%v", response, err)
			}
			result := response.Results[0]
			if result.Outcome != "applied" || result.State == nil ||
				result.State.ProcessState != service.ProcessRunning || result.State.Health != service.HealthUnknown {
				t.Fatalf("operation did not converge: %#v", result)
			}
		})
	}
}

func TestLifecycleCommandReportsStartingWhenObservationTimesOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	base := newFakeServiceAdapter(filepath.Join(home, "native"))
	prepareLifecycleConnection(t, base, "db", 1080)
	adapter := newTransitioningServiceAdapter(base, 1_000_000)

	response, err := runLifecycleCommandForTest(t, adapter, lifecycleObservationBudget{
		// This test exercises the per-item convergence timeout. Keep the shared
		// batch budget generous so setup/locking time under -race cannot consume
		// the observation window before the first Inspect call.
		Overall: 5 * time.Second, PerItem: 30 * time.Millisecond, PollInterval: time.Millisecond,
	}, "start", "db")
	if err != nil || response.Outcome != "starting" || len(response.Results) != 1 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	result := response.Results[0]
	if result.Outcome != "starting" || result.State == nil ||
		result.State.ProcessState != service.ProcessStarting || result.State.Health != service.HealthUnknown {
		t.Fatalf("timeout was not represented as starting: %#v", result)
	}
	if len(result.State.Conditions) != 1 || result.State.Conditions[0].Code != "lifecycle_convergence_pending" {
		t.Fatalf("missing pending condition: %#v", result.State.Conditions)
	}
}

func TestLifecyclePendingStatePreservesLastObservation(t *testing.T) {
	last := service.State{
		Connection: "db", Adapter: "fake-native", ServiceState: service.ServiceRegistered,
		Mode: service.ModeBackground, ProcessState: service.ProcessRunning, Health: service.HealthUnhealthy,
		ConsistencyState: service.ConsistencyConsistent, ObservedAt: time.Now().UTC(),
		Endpoints: []plan.Endpoint{{Name: "forward-1", Direction: "access", Protocol: "tcp", Address: "127.0.0.1:1080"}},
		Conditions: []core.Condition{{
			Code: "endpoint_unhealthy", Severity: "error", Message: "endpoint is not accepting connections",
			Connection: "db", Actions: []core.Action{},
		}},
		Platform: map[string]any{"pid": "42"},
	}
	state := lifecyclePendingState(newFakeServiceAdapter(t.TempDir()), service.Record{
		Connection: "db", Mode: service.ModeBackground,
	}, service.Artifact{Plan: plan.ExecutionPlan{Endpoints: last.Endpoints}}, "start", &last)
	if state.ServiceState != service.ServiceRegistered || state.ProcessState != service.ProcessRunning ||
		state.Health != service.HealthUnhealthy || state.ConsistencyState != service.ConsistencyConsistent ||
		state.Platform["pid"] != "42" {
		t.Fatalf("last observation was overwritten: %#v", state)
	}
	if len(state.Conditions) != 2 || state.Conditions[0].Code != "endpoint_unhealthy" ||
		state.Conditions[1].Code != "lifecycle_convergence_pending" {
		t.Fatalf("last conditions were not preserved: %#v", state.Conditions)
	}
	state.Platform["pid"] = "99"
	if last.Platform["pid"] != "42" {
		t.Fatal("pending state aliases the previous platform map")
	}
}

func TestLifecycleObserverWaitsForEndpointHealth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", fmt.Sprint(port), "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists {
		t.Fatalf("load record exists=%t err=%v", exists, err)
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
	artifact, err := buildBoundServiceArtifact(context.Background(), adapter, *connection, &record)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background(), record.Reference()); err != nil {
		t.Fatal(err)
	}
	record.Mode = service.ModeBackground
	if err := records.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	listenerReady := make(chan net.Listener, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		listener, listenErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if listenErr != nil {
			listenerReady <- nil
			return
		}
		listenerReady <- listener
	}()
	state, outcome := newLifecycleObserver(lifecycleObservationBudget{
		Overall: time.Second, PerItem: 500 * time.Millisecond, PollInterval: 5 * time.Millisecond,
	}).observe(context.Background(), adapter, record, artifact, "start")
	listener := <-listenerReady
	if listener == nil {
		t.Fatal("delayed health endpoint could not listen")
	}
	defer listener.Close()
	if outcome != lifecycleConverged || state.ProcessState != service.ProcessRunning || state.Health != service.HealthHealthy {
		t.Fatalf("endpoint did not converge: outcome=%v state=%#v", outcome, state)
	}
}

func TestLifecycleOperationFailuresRemainFailures(t *testing.T) {
	tests := []struct {
		command string
		code    string
		setup   bool
	}{
		{command: "start", code: "service_start_failed"},
		{command: "enable", code: "service_enable_failed"},
		{command: "restart", code: "service_restart_failed", setup: true},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
			prepareLifecycleConnection(t, adapter, "db", 1080)
			if test.setup {
				if code, out, stderr := useFakeServiceAdapter(t, adapter)(t, "start", "db", "--json"); code != 0 {
					t.Fatalf("setup start exit=%d out=%s err=%s", code, out, stderr)
				}
			}
			adapter.failures[test.command] = []error{fmt.Errorf("injected %s failure", test.command)}
			response, err := runLifecycleCommandForTest(t, adapter, lifecycleObservationBudget{
				Overall: time.Second, PerItem: time.Second, PollInterval: time.Millisecond,
			}, test.command, "db")
			if err == nil || response.Outcome != "failed" || len(response.Results) != 1 ||
				response.Results[0].Error == nil || response.Results[0].Error.Code != test.code {
				t.Fatalf("response=%#v err=%v", response, err)
			}
		})
	}
}

func TestLifecycleCommandsReportObservedProcessFailure(t *testing.T) {
	tests := []struct {
		command string
		code    string
		setup   bool
	}{
		{command: "start", code: "service_start_failed"},
		{command: "enable", code: "service_enable_failed"},
		{command: "restart", code: "service_restart_failed", setup: true},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			base := newFakeServiceAdapter(filepath.Join(home, "native"))
			prepareLifecycleConnection(t, base, "db", 1080)
			if test.setup {
				if code, out, stderr := useFakeServiceAdapter(t, base)(t, "start", "db", "--json"); code != 0 {
					t.Fatalf("setup start exit=%d out=%s err=%s", code, out, stderr)
				}
			}
			adapter := newTransitioningServiceAdapter(base, 1_000_000)
			adapter.fail = true
			response, err := runLifecycleCommandForTest(t, adapter, lifecycleObservationBudget{
				Overall: time.Second, PerItem: 500 * time.Millisecond, PollInterval: time.Millisecond,
			}, test.command, "db")
			if err == nil || response.Outcome != "failed" || len(response.Results) != 1 {
				t.Fatalf("response=%#v err=%v", response, err)
			}
			result := response.Results[0]
			if result.Error == nil || result.Error.Code != test.code || result.State == nil ||
				result.State.ProcessState != service.ProcessFailed || !result.Changed {
				t.Fatalf("observed failure was not preserved: %#v", result)
			}
		})
	}
}

func TestLifecycleCommandsPreserveTerminalObservationFailures(t *testing.T) {
	tests := []struct {
		name            string
		configure       func(*transitioningServiceAdapter)
		wantService     string
		wantConsistency string
		wantCondition   string
	}{
		{
			name: "service disappeared", configure: func(adapter *transitioningServiceAdapter) { adapter.missing = true },
			wantService: service.ServiceAbsent, wantConsistency: service.ConsistencyMissing, wantCondition: "service_artifact_missing",
		},
		{
			name: "service manager unavailable", configure: func(adapter *transitioningServiceAdapter) {
				adapter.inspectErr = errors.New("injected inspect failure")
			},
			wantService: service.ServiceUnknown, wantConsistency: service.ConsistencyBlocked, wantCondition: "service_manager_unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			base := newFakeServiceAdapter(filepath.Join(home, "native"))
			prepareLifecycleConnection(t, base, "db", 1080)
			adapter := newTransitioningServiceAdapter(base, 1_000_000)
			test.configure(adapter)
			response, err := runLifecycleCommandForTest(t, adapter, lifecycleObservationBudget{
				Overall: time.Second, PerItem: 500 * time.Millisecond, PollInterval: time.Millisecond,
			}, "start", "db")
			if err == nil || response.Outcome != "failed" || len(response.Results) != 1 {
				t.Fatalf("response=%#v err=%v", response, err)
			}
			result := response.Results[0]
			if result.State == nil || result.State.ServiceState != test.wantService ||
				result.State.ConsistencyState != test.wantConsistency || len(result.State.Conditions) == 0 ||
				result.State.Conditions[0].Code != test.wantCondition {
				t.Fatalf("terminal observation was not preserved: %#v", result)
			}
		})
	}
}

func TestLifecycleBatchSharesOverallObservationBudget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	base := newFakeServiceAdapter(filepath.Join(home, "native"))
	for index := 0; index < 4; index++ {
		prepareLifecycleConnection(t, base, fmt.Sprintf("db-%d", index), 1080+index)
	}
	adapter := newTransitioningServiceAdapter(base, 1_000_000)
	response, err := runLifecycleCommandForTest(t, adapter, lifecycleObservationBudget{
		Overall: 80 * time.Millisecond, PerItem: 60 * time.Millisecond, PollInterval: time.Millisecond,
	}, "start", "--all")
	if err != nil || response.Outcome != "starting" || len(response.Results) != 4 || len(base.startCalls) != 4 {
		t.Fatalf("response=%#v starts=%v err=%v", response, base.startCalls, err)
	}
	unobserved := 0
	for _, result := range response.Results {
		if result.Outcome != "starting" || result.State == nil || len(result.State.Conditions) == 0 ||
			result.State.Conditions[len(result.State.Conditions)-1].Code != "lifecycle_convergence_pending" {
			t.Fatalf("unexpected batch result: %#v", result)
		}
		if result.State.ProcessState == service.ProcessUnknown {
			unobserved++
			if result.State.ServiceState != service.ServiceUnknown || result.State.ConsistencyState != service.ConsistencyBlocked {
				t.Fatalf("unobserved batch item was fabricated as known: %#v", result.State)
			}
		}
	}
	// The first item consumes most of the 80 ms overall budget. At least one
	// later item must therefore skip Inspect entirely. This directly verifies
	// the shared observer deadline without timing unrelated command work such
	// as config I/O, response encoding, or JSON Schema compilation.
	if unobserved == 0 {
		t.Fatalf("each batch item received a fresh observation budget: %#v", response.Results)
	}
}

func TestLifecycleBatchPendingAndFailureReturnsPartialExitCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	base := newFakeServiceAdapter(filepath.Join(home, "native"))
	prepareLifecycleConnection(t, base, "db-failed", 1080)
	prepareLifecycleConnection(t, base, "db-pending", 1081)
	adapter := newTransitioningServiceAdapter(base, 1_000_000)
	base.failures["start"] = []error{errors.New("injected batch start failure")}
	budget := lifecycleObservationBudget{Overall: 80 * time.Millisecond, PerItem: 60 * time.Millisecond, PollInterval: time.Millisecond}
	var output, stderr bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"start", "--all", "--json"}, IO{Out: &output, Err: &stderr}, options{
		serviceAdapter: func() (service.Adapter, error) { return adapter, nil }, lifecycleBudget: &budget,
	})
	if code != exitPartial || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%s output=%s", code, stderr.String(), output.String())
	}
	validateCLIResponseSchema(t, output.Bytes())
	var response lifecycleResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome != "partial" || len(response.Results) != 2 {
		t.Fatalf("unexpected partial response: %#v", response)
	}
	failed, pending := 0, 0
	for _, result := range response.Results {
		switch result.Outcome {
		case "failed":
			failed++
		case "starting":
			pending++
		}
	}
	if failed != 1 || pending != 1 {
		t.Fatalf("failed=%d pending=%d results=%#v", failed, pending, response.Results)
	}
}

func TestMonitorKeepsTransitionWhenLifecycleObservationIsPending(t *testing.T) {
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
	row := fixtureStatusRow(t)
	row.state.Mode = service.ModeBackground
	budget := lifecycleObservationBudget{Overall: time.Millisecond, PerItem: time.Millisecond, PollInterval: time.Millisecond}
	result := executeMonitorMutation(context.Background(), "restart", row, options{
		Language: languageEnglish, serviceAdapter: func() (service.Adapter, error) { return adapter, nil }, lifecycleBudget: &budget,
	})
	if !result.pending || result.completedMessage != "restarted db" || result.message != "restart accepted; still restarting db" {
		t.Fatalf("pending monitor result=%#v", result)
	}
	row.state.ServiceState = service.ServiceRegistered
	row.state.ConsistencyState = service.ConsistencyConsistent
	row.state.ProcessState = service.ProcessRunning
	row.state.Health = service.HealthUnhealthy
	if monitorTransitionConverged(row) {
		t.Fatal("unhealthy endpoint prematurely completed the monitor transition")
	}
	row.state.Health = service.HealthHealthy
	if !monitorTransitionConverged(row) {
		t.Fatal("healthy running service did not complete the monitor transition")
	}
}

func TestMonitorTransitionResolutionClearsTerminalStatesAndRefreshErrors(t *testing.T) {
	transition := &monitorTransition{name: "db", label: "restarting", accepted: true, completedMessage: "restarted db"}
	tests := []struct {
		name       string
		state      service.State
		refreshErr error
		wantText   string
	}{
		{
			name: "service missing",
			state: service.State{
				Connection: "db", ServiceState: service.ServiceAbsent, ConsistencyState: service.ConsistencyMissing,
				ProcessState: service.ProcessInactive, Conditions: []core.Condition{{Message: "native service is missing"}},
			},
			wantText: "native service is missing",
		},
		{
			name: "observation blocked",
			state: service.State{
				Connection: "db", ServiceState: service.ServiceUnknown, ConsistencyState: service.ConsistencyBlocked,
				ProcessState: service.ProcessUnknown, Conditions: []core.Condition{{Message: "status observation exceeded its time budget"}},
			},
			wantText: "status observation exceeded",
		},
		{name: "collection error", refreshErr: errors.New("service manager unavailable"), wantText: "status refresh failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := []statusRow(nil)
			if test.refreshErr == nil {
				rows = []statusRow{{connection: core.Connection{Metadata: core.Metadata{Name: "db"}}, state: test.state}}
			}
			resolved, message := monitorTransitionResolution(transition, rows, test.refreshErr, languageEnglish)
			if !resolved || !strings.Contains(message, test.wantText) {
				t.Fatalf("resolved=%t message=%q", resolved, message)
			}
		})
	}

	pending := service.State{
		Connection: "db", ServiceState: service.ServiceRegistered, ConsistencyState: service.ConsistencyConsistent,
		ProcessState: service.ProcessStarting, Health: service.HealthUnknown,
	}
	resolved, message := monitorTransitionResolution(transition, []statusRow{{
		connection: core.Connection{Metadata: core.Metadata{Name: "db"}}, state: pending,
	}}, nil, languageEnglish)
	if resolved || message != "" {
		t.Fatalf("pending transition resolved prematurely: resolved=%t message=%q", resolved, message)
	}
}
