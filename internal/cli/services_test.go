package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
	inspectCalls     []service.Reference
}

type scopedFakeServiceAdapter struct {
	*fakeServiceAdapter
	preflightCalls int
	stageCalls     int
	failPreflight  int
	preflightErr   error
	failStage      int
	privilegeError bool
	stageErr       error
	readOnlyChecks int
	failReadOnly   int
}

type fakePrivilegeAuthorizer struct {
	calls   int
	err     error
	request privilegeRequest
}

func (authorizer *fakePrivilegeAuthorizer) Validate(_ context.Context, _ IO, _ language, request privilegeRequest) error {
	authorizer.calls++
	authorizer.request = request
	return authorizer.err
}

type systemOnlyFakeServiceAdapter struct{ *scopedFakeServiceAdapter }

func (f *systemOnlyFakeServiceAdapter) Capabilities() service.Capabilities {
	capabilities := f.scopedFakeServiceAdapter.Capabilities()
	capabilities.UserScope = false
	return capabilities
}

func (f *scopedFakeServiceAdapter) CheckScopeInstall(_ context.Context, target service.Artifact) error {
	f.readOnlyChecks++
	if f.failReadOnly > 0 && f.readOnlyChecks == f.failReadOnly {
		return errors.New("injected read-only scope check failure")
	}
	if current, exists := f.artifacts[target.Connection]; exists && service.NormalizeScope(current.Scope) == service.NormalizeScope(target.Scope) {
		return &service.ScopeConflictError{UnitName: target.UnitName}
	}
	return nil
}

func (f *scopedFakeServiceAdapter) CheckScopeTransition(ctx context.Context, _ service.Reference, target service.Artifact) error {
	return f.CheckScopeInstall(ctx, target)
}

type codedTestError struct {
	code    string
	message string
}

func (e codedTestError) Error() string { return e.message }
func (e codedTestError) Code() string  { return e.code }

type fakeScopeTransaction struct {
	adapter     *fakeServiceAdapter
	connection  string
	artifact    service.Artifact
	observation service.Observation
	hadArtifact bool
	hadObserve  bool
	committed   bool
}

func (f *scopedFakeServiceAdapter) Capabilities() service.Capabilities {
	return service.Capabilities{
		Registration: true, Background: true, Persistence: true, Logs: true, Discovery: true,
		UserScope: true, SystemScope: true,
	}
}

func (f *scopedFakeServiceAdapter) TargetScoped(scope service.Scope, name string) (service.Artifact, error) {
	artifact := f.Target(name)
	artifact.Scope = service.NormalizeScope(scope)
	if artifact.Scope == service.ScopeSystem {
		artifact.Path = filepath.Join(f.root, name+".system.service")
	}
	return artifact, nil
}

func (f *scopedFakeServiceAdapter) BuildScoped(scope service.Scope, name string, value plan.ExecutionPlan) (service.Artifact, error) {
	artifact, err := f.TargetScoped(scope, name)
	if err != nil {
		return service.Artifact{}, err
	}
	digest, err := service.PlanDigest(value)
	if err != nil {
		return service.Artifact{}, err
	}
	artifact.Plan, artifact.PlanDigest = value, digest
	artifact.Content, err = json.Marshal(value)
	return artifact, err
}

func (f *scopedFakeServiceAdapter) BuildBound(reference service.Reference, value plan.ExecutionPlan) (service.Artifact, error) {
	artifact, err := f.BuildScoped(reference.Scope, reference.Connection, value)
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

func (f *scopedFakeServiceAdapter) preflight() error {
	f.preflightCalls++
	if f.privilegeError {
		return &service.PrivilegeError{Operation: "test system scope", Cause: errors.New("authorization unavailable")}
	}
	if f.failPreflight > 0 && f.preflightCalls == f.failPreflight {
		if f.preflightErr != nil {
			return f.preflightErr
		}
		return &service.ScopeConflictError{UnitName: "fake-conflict"}
	}
	return nil
}

func (f *scopedFakeServiceAdapter) PreflightScopeInstall(context.Context, service.Artifact) error {
	return f.preflight()
}

func (f *scopedFakeServiceAdapter) PreflightScopeTransition(context.Context, service.Reference, service.Artifact) error {
	return f.preflight()
}

func (f *scopedFakeServiceAdapter) PreflightDisable(context.Context, service.Reference) error {
	return f.preflight()
}

func (f *scopedFakeServiceAdapter) CheckDefinitionReplace(_ context.Context, source service.Reference, target service.Artifact) error {
	f.readOnlyChecks++
	if service.NormalizeScope(source.Scope) != service.ScopeSystem || service.NormalizeScope(target.Scope) != service.ScopeSystem ||
		source.UnitName != target.UnitName {
		return errors.New("invalid fake system definition replacement")
	}
	return nil
}

func (f *scopedFakeServiceAdapter) PreflightDefinitionReplace(context.Context, service.Reference, service.Artifact) error {
	return f.preflight()
}

func (f *scopedFakeServiceAdapter) StageDefinitionReplace(_ context.Context, _ service.Reference, target service.Artifact, _ string, _ func(context.Context, service.Reference) error) (service.ScopeTransition, error) {
	if err := f.fail("enable"); err != nil {
		return nil, err
	}
	return f.stage(target)
}

func (f *scopedFakeServiceAdapter) Disable(ctx context.Context, reference service.Reference) error {
	if err := f.fakeServiceAdapter.Disable(ctx, reference); err != nil {
		return err
	}
	if service.NormalizeScope(reference.Scope) == service.ScopeSystem {
		delete(f.artifacts, reference.Connection)
		f.observed[reference.Connection] = service.Observation{
			Scope: service.ScopeSystem, ProcessState: service.ProcessInactive, Platform: map[string]any{},
		}
	}
	return nil
}

func (f *scopedFakeServiceAdapter) stage(target service.Artifact) (service.ScopeTransition, error) {
	f.stageCalls++
	if f.stageErr != nil {
		return nil, f.stageErr
	}
	if f.failStage > 0 && f.stageCalls == f.failStage {
		return nil, errors.New("injected scope stage failure")
	}
	transaction := &fakeScopeTransaction{adapter: f.fakeServiceAdapter, connection: target.Connection}
	transaction.artifact, transaction.hadArtifact = f.artifacts[target.Connection]
	transaction.observation, transaction.hadObserve = f.observed[target.Connection]
	f.artifacts[target.Connection] = target
	f.observed[target.Connection] = service.Observation{
		Scope: target.Scope, Installed: true, AutoStart: true, ProcessState: service.ProcessRunning,
		Artifact: append([]byte(nil), target.Content...), Platform: map[string]any{"unit": target.UnitName},
	}
	return transaction, nil
}

func (f *scopedFakeServiceAdapter) StageScopeInstall(_ context.Context, target service.Artifact, _ string, _ func(context.Context, service.Reference) error) (service.ScopeTransition, error) {
	return f.stage(target)
}

func (f *scopedFakeServiceAdapter) StageScopeTransition(_ context.Context, _ service.Reference, target service.Artifact, _ string, _ func(context.Context, service.Reference) error) (service.ScopeTransition, error) {
	return f.stage(target)
}

func (t *fakeScopeTransaction) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *fakeScopeTransaction) Rollback(context.Context) error {
	if t.committed {
		return errors.New("scope transaction already committed")
	}
	if t.hadArtifact {
		t.adapter.artifacts[t.connection] = t.artifact
	} else {
		delete(t.adapter.artifacts, t.connection)
	}
	if t.hadObserve {
		t.adapter.observed[t.connection] = t.observation
	} else {
		delete(t.adapter.observed, t.connection)
	}
	return nil
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
	return service.Capabilities{Registration: true, Background: true, Persistence: true, Logs: true, Discovery: true, UserScope: true}
}

func TestParseEnableScopeIsStrictAndCommandSpecific(t *testing.T) {
	selection, err := parseServiceSelection("enable", []string{"db", "--scope", "system"})
	if err != nil {
		t.Fatal(err)
	}
	if selection.target != "db" || !selection.scopeSet || selection.scope != service.ScopeSystem {
		t.Fatalf("selection=%+v", selection)
	}
	for _, test := range []struct {
		name    string
		command string
		args    []string
	}{
		{name: "unknown", command: "enable", args: []string{"db", "--scope", "machine"}},
		{name: "case alias", command: "enable", args: []string{"db", "--scope", "USER"}},
		{name: "duplicate", command: "enable", args: []string{"db", "--scope", "user", "--scope", "system"}},
		{name: "wrong command", command: "start", args: []string{"db", "--scope", "user"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseServiceSelection(test.command, test.args); err == nil {
				t.Fatal("invalid scope usage was accepted")
			}
		})
	}
}

func TestParseEnableScopeSupportsBatchSelectors(t *testing.T) {
	for _, args := range [][]string{{"--group", "work", "--scope", "user"}, {"--all", "--scope", "system"}} {
		selection, err := parseServiceSelection("enable", args)
		if err != nil {
			t.Fatalf("args=%v: %v", args, err)
		}
		if !selection.scopeSet {
			t.Fatalf("args=%v selection=%+v", args, selection)
		}
	}
}

func TestUnsupportedBatchScopeFailsBeforeAnyMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "one", "-D", "1081", "me@example.com")
	_, _, _ = invoke(t, "add", "two", "-D", "1082", "me@example.com")
	materializedBefore := len(adapter.materializeCalls)
	code, out, _ := invoke(t, "enable", "--all", "--scope", "system", "--json", "--non-interactive")
	if code == 0 || strings.Count(out, `"code":"scope_unsupported"`) != 2 {
		t.Fatalf("enable exit=%d out=%s", code, out)
	}
	if len(adapter.materializeCalls) != materializedBefore || len(adapter.enableCalls) != 0 {
		t.Fatalf("unsupported batch scope mutated services: materialize=%v enable=%v", adapter.materializeCalls, adapter.enableCalls)
	}
}

func TestEnableDefaultsNewLocalServiceToUserScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	code, out, stderr := invoke(t, "enable", "db", "--json")
	if code != 0 || stderr != "" || strings.Count(out, `"scope":"user"`) < 2 {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
}

func TestEnableWithoutScopePreservesExistingRecordScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists {
		t.Fatalf("record exists=%t err=%v", exists, err)
	}
	record.Scope = service.ScopeSystem
	if err := records.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	artifact := adapter.artifacts["db"]
	artifact.Scope = service.ScopeSystem
	adapter.artifacts["db"] = artifact
	observation := adapter.observed["db"]
	observation.Scope = service.ScopeSystem
	adapter.observed["db"] = observation
	code, out, stderr := invoke(t, "enable", "db", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"scope":"system"`) {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	record, _, err = records.Load("db")
	if err != nil || record.Scope != service.ScopeSystem {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestEnableMigratesScopeAndPersistsTargetArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	code, out, stderr := invoke(t, "enable", "db", "--scope", "system", "--json", "--non-interactive")
	if code != 0 || stderr != "" || strings.Count(out, `"scope":"system"`) < 2 {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	validateCLIResponseSchema(t, []byte(out))
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists || record.Scope != service.ScopeSystem || record.Mode != service.ModePersistent {
		t.Fatalf("record=%+v exists=%t err=%v", record, exists, err)
	}
	if !strings.HasSuffix(record.ArtifactPath, ".system.service") || adapter.preflightCalls != 1 || adapter.stageCalls != 1 {
		t.Fatalf("record=%+v preflight=%d stage=%d", record, adapter.preflightCalls, adapter.stageCalls)
	}
}

func TestEnableSystemWithoutRecordInstallsDirectlyInSystemScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Remove("db"); err != nil {
		t.Fatal(err)
	}
	delete(adapter.artifacts, "db")
	delete(adapter.observed, "db")
	code, out, stderr := invoke(t, "enable", "db", "--scope", "system", "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"scope":"system"`) {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists || record.Scope != service.ScopeSystem || adapter.stageCalls != 1 {
		t.Fatalf("record=%+v exists=%t stage=%d err=%v", record, exists, adapter.stageCalls, err)
	}
}

func TestScopeBatchPreflightFailurePerformsZeroWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")), failPreflight: 2}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "one", "-D", "1081", "me@example.com")
	_, _, _ = invoke(t, "add", "two", "-D", "1082", "me@example.com")
	code, out, _ := invoke(t, "enable", "--all", "--scope", "system", "--json", "--non-interactive")
	if code == 0 || strings.Count(out, `"code":"scope_conflict"`) != 2 || adapter.stageCalls != 0 {
		t.Fatalf("enable exit=%d out=%s preflight=%d stage=%d", code, out, adapter.preflightCalls, adapter.stageCalls)
	}
	records, _ := serviceRecordStore()
	for _, name := range []string{"one", "two"} {
		record, _, err := records.Load(name)
		if err != nil || record.Scope != service.ScopeUser {
			t.Fatalf("%s record=%+v err=%v", name, record, err)
		}
	}
}

func TestScopeBatchRuntimeFailureIsPartialAndKeepsEarlierSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")), failStage: 2}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "one", "-D", "1081", "me@example.com")
	_, _, _ = invoke(t, "add", "two", "-D", "1082", "me@example.com")
	code, out, _ := invoke(t, "enable", "--all", "--scope", "system", "--json", "--non-interactive")
	if code != exitPartial || !strings.Contains(out, `"outcome":"partial"`) || !strings.Contains(out, `"code":"scope_transition_failed"`) {
		t.Fatalf("enable exit=%d out=%s", code, out)
	}
	records, _ := serviceRecordStore()
	one, _, _ := records.Load("one")
	two, _, _ := records.Load("two")
	if one.Scope != service.ScopeSystem || two.Scope != service.ScopeUser {
		t.Fatalf("one=%+v two=%+v", one, two)
	}
}

func TestNonInteractiveScopePrivilegeFailureUsesStableCodeAndDoesNotStage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")), privilegeError: true}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	code, out, _ := invoke(t, "enable", "db", "--scope", "system", "--json", "--non-interactive")
	if code != 4 || !strings.Contains(out, `"code":"privilege_required"`) || adapter.stageCalls != 0 {
		t.Fatalf("enable exit=%d out=%s stage=%d", code, out, adapter.stageCalls)
	}
}

func TestInteractiveScopeBatchAuthorizesOnceAfterAllReadOnlyChecks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "one", "-D", "1081", "me@example.com")
	_, _, _ = invoke(t, "add", "two", "-D", "1082", "me@example.com")
	authorizer := &fakePrivilegeAuthorizer{}
	terminal := true
	var out, stderr bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"enable", "--all", "--scope", "system", "--json"}, IO{
		Out: &out, Err: &stderr, Terminal: &terminal,
	}, options{
		serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
		privilegeAuthorizer: authorizer,
	})
	if code != 0 || authorizer.calls != 1 || authorizer.request.connections != 2 || authorizer.request.userToSystem != 2 ||
		adapter.readOnlyChecks != 2 || adapter.preflightCalls != 2 || adapter.stageCalls != 2 {
		t.Fatalf("exit=%d auth=%d checks=%d preflight=%d stage=%d out=%s err=%s", code, authorizer.calls,
			adapter.readOnlyChecks, adapter.preflightCalls, adapter.stageCalls, out.String(), stderr.String())
	}
}

func TestInteractiveScopeBatchNthPrivilegedPreflightFailurePerformsZeroWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{
		fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")), failPreflight: 2,
		preflightErr: &service.PrivilegeError{Operation: "second sudo -n preflight", Cause: errors.New("authorization cache unavailable")},
	}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "one", "-D", "1081", "me@example.com")
	_, _, _ = invoke(t, "add", "two", "-D", "1082", "me@example.com")
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]service.Record{}
	for _, name := range []string{"one", "two"} {
		record, exists, loadErr := records.Load(name)
		if loadErr != nil || !exists {
			t.Fatalf("load %s before preflight: exists=%t err=%v", name, exists, loadErr)
		}
		before[name] = record
	}
	authorizer := &fakePrivilegeAuthorizer{}
	terminal := true
	var out bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"enable", "--all", "--scope", "system", "--json"}, IO{
		Out: &out, Err: io.Discard, Terminal: &terminal,
	}, options{
		serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
		privilegeAuthorizer: authorizer,
	})
	if code != 4 || authorizer.calls != 1 || adapter.preflightCalls != 2 || adapter.stageCalls != 0 ||
		strings.Count(out.String(), `"code":"privilege_required"`) != 2 {
		t.Fatalf("exit=%d auth=%d preflight=%d stage=%d out=%s", code, authorizer.calls,
			adapter.preflightCalls, adapter.stageCalls, out.String())
	}
	for _, name := range []string{"one", "two"} {
		record, exists, loadErr := records.Load(name)
		if loadErr != nil || !exists || !reflect.DeepEqual(record, before[name]) {
			t.Fatalf("%s record=%+v exists=%t err=%v", name, record, exists, loadErr)
		}
	}
}

func TestScopeReadOnlyFailurePreventsAuthorizationAndAllWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{
		fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")), failReadOnly: 2,
	}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "one", "-D", "1081", "me@example.com")
	_, _, _ = invoke(t, "add", "two", "-D", "1082", "me@example.com")
	authorizer := &fakePrivilegeAuthorizer{}
	terminal := true
	var out bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"enable", "--all", "--scope", "system", "--json"}, IO{
		Out: &out, Err: io.Discard, Terminal: &terminal,
	}, options{
		serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
		privilegeAuthorizer: authorizer,
	})
	if code == 0 || authorizer.calls != 0 || adapter.preflightCalls != 0 || adapter.stageCalls != 0 {
		t.Fatalf("exit=%d auth=%d preflight=%d stage=%d out=%s", code, authorizer.calls, adapter.preflightCalls, adapter.stageCalls, out.String())
	}
}

func TestScopeAuthorizationFailureIsTypedAndStartsNoMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "one", "-D", "1081", "me@example.com")
	_, _, _ = invoke(t, "add", "two", "-D", "1082", "me@example.com")
	authorizer := &fakePrivilegeAuthorizer{err: &privilegeRequiredError{message: "authorization cancelled"}}
	terminal := true
	var out bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"enable", "--all", "--scope", "system", "--json"}, IO{
		Out: &out, Err: io.Discard, Terminal: &terminal,
	}, options{
		serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
		privilegeAuthorizer: authorizer,
	})
	if code != 4 || authorizer.calls != 1 || adapter.preflightCalls != 0 || adapter.stageCalls != 0 ||
		strings.Count(out.String(), `"code":"privilege_required"`) != 2 {
		t.Fatalf("exit=%d auth=%d preflight=%d stage=%d out=%s", code, authorizer.calls, adapter.preflightCalls, adapter.stageCalls, out.String())
	}
}

func TestScopeAuthorizationCoversBothMigrationDirectionsButNotSameScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	authorizer := &fakePrivilegeAuthorizer{}
	terminal := true
	run := func(scope string) int {
		var out bytes.Buffer
		return runContextWithOptions(context.Background(), []string{"enable", "db", "--scope", scope, "--json"}, IO{
			Out: &out, Err: io.Discard, Terminal: &terminal,
		}, options{
			serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
			privilegeAuthorizer: authorizer,
		})
	}
	if code := run("system"); code != 0 || authorizer.calls != 1 {
		t.Fatalf("user->system exit=%d auth=%d", code, authorizer.calls)
	}
	if code := run("system"); code != 0 || authorizer.calls != 1 {
		t.Fatalf("same system scope exit=%d auth=%d", code, authorizer.calls)
	}
	if code := run("user"); code != 0 || authorizer.calls != 2 {
		t.Fatalf("system->user exit=%d auth=%d", code, authorizer.calls)
	}
	if code := run("user"); code != 0 || authorizer.calls != 2 {
		t.Fatalf("same user scope exit=%d auth=%d", code, authorizer.calls)
	}
}

func TestScopeAuthorizationNoTTYFailsFastWithTypedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	runner := &fakePrivilegeCommandRunner{}
	terminal := false
	var out, stderr bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"enable", "db", "--scope", "system", "--json"}, IO{
		In: strings.NewReader(""), Out: &out, Err: &stderr, Terminal: &terminal,
	}, options{
		serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
		privilegeAuthorizer: sudoPrivilegeAuthorizer{runner: runner},
	})
	if code != 4 || runner.calls != 0 || adapter.preflightCalls != 0 || adapter.stageCalls != 0 || stderr.Len() != 0 ||
		!strings.Contains(out.String(), `"code":"privilege_required"`) || !strings.Contains(out.String(), "interactive terminal") {
		t.Fatalf("exit=%d runner=%d preflight=%d stage=%d out=%s err=%s", code, runner.calls,
			adapter.preflightCalls, adapter.stageCalls, out.String(), stderr.String())
	}
	validateCLIResponseSchema(t, out.Bytes())
}

func TestInteractiveScopeAuthorizationKeepsJSONStdoutPureAndPromptsOnStderr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	runner := &fakePrivilegeCommandRunner{}
	terminal := true
	var out, stderr bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"enable", "db", "--scope", "system", "--json"}, IO{
		In: strings.NewReader(""), Out: &out, Err: &stderr, Terminal: &terminal,
	}, options{
		serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
		privilegeAuthorizer: sudoPrivilegeAuthorizer{runner: runner},
	})
	if code != 0 || runner.calls != 1 || adapter.stageCalls != 1 || !strings.Contains(stderr.String(), "Requesting it now") ||
		!strings.Contains(stderr.String(), "1 connection(s)") || !strings.Contains(stderr.String(), "1 USER→SYSTEM") {
		t.Fatalf("exit=%d runner=%d stage=%d out=%s err=%s", code, runner.calls, adapter.stageCalls, out.String(), stderr.String())
	}
	validateCLIResponseSchema(t, out.Bytes())
	if strings.Contains(out.String(), "Administrator authorization") || strings.Contains(out.String(), "Password:") {
		t.Fatalf("authorization text leaked to JSON stdout: %s", out.String())
	}
}

func TestInteractiveNewToSystemBatchAuthorizationSummaryIsLocalizedAndJSONSafe(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		language  string
		expected  []string
		forbidden string
	}{
		{
			name: "english", language: "en",
			expected:  []string{"2 connection(s)", "2 NEW→SYSTEM", "Requesting it now"},
			forbidden: "个新服务",
		},
		{
			name: "chinese", language: "zh-CN",
			expected:  []string{"本批有 2 个连接", "2 个新服务→SYSTEM", "正在请求授权"},
			forbidden: "connection(s)",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
			invoke := useFakeServiceAdapter(t, adapter)
			for index, name := range []string{"one", "two"} {
				_, _, _ = invoke(t, "add", name, "-D", strconv.Itoa(1081+index), "me@example.com")
			}
			records, err := serviceRecordStore()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"one", "two"} {
				if err := records.Remove(name); err != nil {
					t.Fatal(err)
				}
				delete(adapter.artifacts, name)
				delete(adapter.observed, name)
			}
			runner := &fakePrivilegeCommandRunner{}
			terminal := true
			var out, stderr bytes.Buffer
			code := runContextWithOptions(context.Background(), []string{
				"enable", "--all", "--scope", "system", "--json", "--lang", testCase.language,
			}, IO{In: strings.NewReader(""), Out: &out, Err: &stderr, Terminal: &terminal}, options{
				serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
				privilegeAuthorizer: sudoPrivilegeAuthorizer{runner: runner},
			})
			if code != 0 || runner.calls != 1 || adapter.stageCalls != 2 {
				t.Fatalf("exit=%d runner=%d stage=%d out=%s err=%s", code, runner.calls, adapter.stageCalls, out.String(), stderr.String())
			}
			for _, expected := range testCase.expected {
				if !strings.Contains(stderr.String(), expected) {
					t.Fatalf("stderr missing %q: %s", expected, stderr.String())
				}
			}
			if strings.Contains(stderr.String(), testCase.forbidden) {
				t.Fatalf("stderr mixed languages: %s", stderr.String())
			}
			validateCLIResponseSchema(t, out.Bytes())
			if strings.Contains(out.String(), "NEW→SYSTEM") || strings.Contains(out.String(), "正在请求授权") ||
				strings.Contains(out.String(), "Requesting it now") {
				t.Fatalf("authorization prompt leaked to JSON stdout: %s", out.String())
			}
		})
	}
}

func TestNonInteractiveAndDryRunNeverCallInteractiveAuthorizer(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "non-interactive", args: []string{"enable", "db", "--scope", "system", "--json", "--non-interactive"}},
		{name: "dry-run", args: []string{"enable", "db", "--scope", "system", "--json", "--dry-run"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
			invoke := useFakeServiceAdapter(t, adapter)
			_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
			authorizer := &fakePrivilegeAuthorizer{err: errors.New("must not be called")}
			var out bytes.Buffer
			code := runContextWithOptions(context.Background(), testCase.args, IO{Out: &out, Err: io.Discard}, options{
				serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
				privilegeAuthorizer: authorizer,
			})
			if code != 0 || authorizer.calls != 0 {
				t.Fatalf("exit=%d authorizer calls=%d out=%s", code, authorizer.calls, out.String())
			}
			if testCase.name == "dry-run" && adapter.stageCalls != 0 {
				t.Fatalf("dry-run staged %d changes", adapter.stageCalls)
			}
		})
	}
}

func TestScopeDryRunPlansTargetWithoutPrivilegePreflight(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")), privilegeError: true}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	code, out, stderr := invoke(t, "enable", "db", "--scope", "system", "--dry-run", "--json", "--non-interactive")
	if code != 0 || stderr != "" || !strings.Contains(out, `"outcome":"planned"`) || !strings.Contains(out, `"artifact":{"adapter":"fake-native","scope":"system"`) {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	validateCLIResponseSchema(t, []byte(out))
	if adapter.readOnlyChecks == 0 || adapter.preflightCalls != 0 || adapter.stageCalls != 0 {
		t.Fatalf("dry-run checks=%d privileged-preflight=%d stage=%d", adapter.readOnlyChecks, adapter.preflightCalls, adapter.stageCalls)
	}
}

func TestSystemOnlyAdapterAddRemainsForegroundOnlyAndCapabilitiesHaveNoFalseDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	base := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	adapter := &systemOnlyFakeServiceAdapter{scopedFakeServiceAdapter: base}
	invoke := useFakeServiceAdapter(t, adapter)
	code, out, stderr := invoke(t, "add", "db", "-D", "1080", "me@example.com", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"code":"foreground_only"`) {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := records.Load("db"); err != nil || exists {
		t.Fatalf("foreground-only add created native record: exists=%v err=%v", exists, err)
	}
	code, out, stderr = invoke(t, "capabilities", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"default":null`) || !strings.Contains(out, `"supported":["system"]`) {
		t.Fatalf("capabilities exit=%d out=%s err=%s", code, out, stderr)
	}
}

func TestSystemOnlyAdapterStatusFindsSystemOrphan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	base := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	adapter := &systemOnlyFakeServiceAdapter{scopedFakeServiceAdapter: base}
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, _ := invoke(t, "add", "db", "-D", "1080", "me@example.com", "--json"); code != 0 {
		t.Fatalf("add exit=%d out=%s", code, out)
	}
	connectionStore, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := connectionStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, connection := configstore.Find(&document, "db")
	artifact, err := buildServiceArtifactScoped(context.Background(), adapter, *connection, service.ScopeSystem)
	if err != nil {
		t.Fatal(err)
	}
	base.artifacts["db"] = artifact
	base.observed["db"] = service.Observation{Scope: service.ScopeSystem, Installed: true, AutoStart: true, ProcessState: service.ProcessRunning}
	code, out, stderr := invoke(t, "status", "db", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"scope":"system"`) || !strings.Contains(out, `"code":"scope_conflict"`) ||
		!strings.Contains(out, "native service fake-db exists") {
		t.Fatalf("status exit=%d out=%s err=%s", code, out, stderr)
	}
}

func TestStatusPreservesScopeMismatchConditionCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, _ := invoke(t, "add", "db", "-D", "1080", "me@example.com", "--json"); code != 0 {
		t.Fatalf("add exit=%d out=%s", code, out)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Remove("db"); err != nil {
		t.Fatal(err)
	}
	delete(adapter.artifacts, "db")
	delete(adapter.observed, "db")
	adapter.failures["inspect"] = []error{&service.ScopeMismatchError{
		UnitName: "fake-db", Expected: service.ScopeUser, Actual: service.ScopeSystem,
	}}
	code, out, stderr := invoke(t, "status", "db", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"code":"scope_mismatch"`) ||
		!strings.Contains(out, `"scope":"system"`) || !strings.Contains(out, `"serviceState":"registered"`) ||
		strings.Contains(out, `"code":"service_manager_unavailable"`) {
		t.Fatalf("status exit=%d out=%s err=%s", code, out, stderr)
	}
}

func TestScopeMigrationPreservesRenamedNativeServiceBinding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "old", "-D", "1080", "me@example.com")
	_, _, _ = invoke(t, "rename", "old", "new")
	code, out, stderr := invoke(t, "enable", "new", "--scope", "system", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	records, _ := serviceRecordStore()
	record, exists, err := records.Load("new")
	if err != nil || !exists || record.Connection != "new" || record.ServiceKey != "old" || record.Scope != service.ScopeSystem {
		t.Fatalf("record=%+v exists=%t err=%v", record, exists, err)
	}
}

func TestInteractiveScopeMigrationShowsImpactAndIntermediateProgress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	_, _, _ = invoke(t, "add", "db", "-D", "1080", "me@example.com")
	terminal := true
	var out, stderr bytes.Buffer
	budget := lifecycleObservationBudget{Overall: time.Millisecond, PerItem: time.Millisecond, PollInterval: time.Millisecond}
	code := runContextWithOptions(context.Background(), []string{"enable", "db", "--scope", "system"}, IO{
		Out: &out, Err: &stderr, Terminal: &terminal,
	}, options{
		serviceAdapter: func() (service.Adapter, error) { return adapter, nil }, lifecycleBudget: &budget,
		privilegeAuthorizer: &fakePrivilegeAuthorizer{},
	})
	if code != 0 {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out.String(), stderr.String())
	}
	for _, expected := range []string{`Moving "db" from USER scope to SYSTEM scope`, "Target process is stable", "scope change complete"} {
		if !strings.Contains(stderr.String(), expected) {
			t.Fatalf("progress missing %q: %s", expected, stderr.String())
		}
	}
}

func TestResolveTunliteFailureRestoresSystemArtifactModeAndRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, "enable", "db", "--scope", "system", "--json"); code != 0 {
		t.Fatalf("enable exit=%d out=%s err=%s", code, out, stderr)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	wantRecord, exists, err := records.Load("db")
	if err != nil || !exists {
		t.Fatalf("load record exists=%t err=%v", exists, err)
	}
	wantRecord.LogPaths = append([]string(nil), wantRecord.LogPaths...)
	wantArtifact := adapter.artifacts["db"]
	wantArtifact.Content = append([]byte(nil), wantArtifact.Content...)
	wantArtifact.LogPaths = append([]string(nil), wantArtifact.LogPaths...)
	wantObservation := adapter.observed["db"]

	config, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Update(context.Background(), func(document *configstore.Document) error {
		_, connection := configstore.Find(document, "db")
		if connection == nil {
			return errors.New("db declaration disappeared")
		}
		connection.Spec.Config = bytes.Replace(connection.Spec.Config, []byte("1080"), []byte("1081"), 1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	adapter.failures["enable"] = []error{errors.New("injected resolve mode failure")}
	if code, out, _ := invoke(t, "resolve", "db", "--use", "tunlite", "--json"); code == 0 || !strings.Contains(out, "injected resolve mode failure") {
		t.Fatalf("resolve exit=%d out=%s", code, out)
	}

	gotRecord, exists, err := records.Load("db")
	if err != nil || !exists || !reflect.DeepEqual(gotRecord, wantRecord) {
		t.Fatalf("record was not restored exactly: got=%#v want=%#v exists=%t err=%v", gotRecord, wantRecord, exists, err)
	}
	gotArtifact := adapter.artifacts["db"]
	if gotArtifact.Scope != service.ScopeSystem || gotArtifact.UnitName != wantArtifact.UnitName || gotArtifact.Path != wantArtifact.Path ||
		gotArtifact.PlanDigest != wantArtifact.PlanDigest || !bytes.Equal(gotArtifact.Content, wantArtifact.Content) ||
		!reflect.DeepEqual(gotArtifact.LogPaths, wantArtifact.LogPaths) {
		t.Fatalf("system artifact was not restored: got=%#v want=%#v", gotArtifact, wantArtifact)
	}
	gotObservation := adapter.observed["db"]
	if gotObservation.Scope != service.ScopeSystem || gotObservation.AutoStart != wantObservation.AutoStart || gotObservation.ProcessState != wantObservation.ProcessState ||
		!bytes.Equal(gotObservation.Artifact, wantObservation.Artifact) {
		t.Fatalf("system mode was not restored: got=%#v want=%#v", gotObservation, wantObservation)
	}
}

func TestBuildBoundRejectsSystemRecordOnSystemdBeforeBuilding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	fake := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, fake)
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
	_, connection := configstore.Find(&document, "db")
	if connection == nil {
		t.Fatal("db declaration is missing")
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := records.Load("db")
	if err != nil {
		t.Fatal(err)
	}
	record.Scope = service.ScopeSystem
	_, err = buildBoundServiceArtifact(context.Background(), service.NewSystemd(t.TempDir(), nil), *connection, &record)
	var unsupported *service.UnsupportedScopeError
	if !errors.As(err, &unsupported) || unsupported.Code() != "scope_unsupported" {
		t.Fatalf("systemd system record was not rejected with typed error: %T %v", err, err)
	}
}

func TestUnsupportedRecordScopeIsRejectedByEveryCLIPathWithoutMutation(t *testing.T) {
	commands := []struct {
		name string
		args []string
		mode string
	}{
		{name: "start", args: []string{"start", "db", "--json"}, mode: service.ModeStopped},
		{name: "stop", args: []string{"stop", "db", "--json"}, mode: service.ModeBackground},
		{name: "enable", args: []string{"enable", "db", "--json"}, mode: service.ModeStopped},
		{name: "disable", args: []string{"disable", "db", "--json"}, mode: service.ModePersistent},
		{name: "restart", args: []string{"restart", "db", "--json"}, mode: service.ModePersistent},
		{name: "status", args: []string{"status", "db", "--json"}, mode: service.ModeStopped},
		{name: "logs", args: []string{"logs", "db", "--json"}, mode: service.ModeStopped},
		{name: "run", args: []string{"run", "db", "--dry-run", "--json"}, mode: service.ModeStopped},
		{name: "resolve", args: []string{"resolve", "db", "--use", "tunlite", "--json"}, mode: service.ModeStopped},
		{name: "remove", args: []string{"remove", "db", "--json"}, mode: service.ModeStopped},
		{name: "rename", args: []string{"rename", "db", "renamed", "--json"}, mode: service.ModeStopped},
	}
	for _, test := range commands {
		t.Run(test.name, func(t *testing.T) {
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
			record, _, err := records.Load("db")
			if err != nil {
				t.Fatal(err)
			}
			record.Scope, record.Mode = service.ScopeSystem, test.mode
			if err := records.Write(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			artifactBefore := adapter.artifacts["db"]
			observationBefore := adapter.observed["db"]
			callsBefore := []int{len(adapter.materializeCalls), len(adapter.startCalls), len(adapter.stopCalls), len(adapter.enableCalls), len(adapter.disableCalls), len(adapter.restartCalls), len(adapter.removeCalls)}

			code, out, stderr := invoke(t, test.args...)
			if test.name == "status" {
				if code != 0 || !strings.Contains(out, `"code":"scope_unsupported"`) {
					t.Fatalf("%s exit=%d out=%s err=%s", test.name, code, out, stderr)
				}
			} else if code == 0 || !strings.Contains(out, `"code":"scope_unsupported"`) {
				t.Fatalf("%s exit=%d out=%s err=%s", test.name, code, out, stderr)
			}
			gotRecord, exists, err := records.Load("db")
			if err != nil || !exists || !reflect.DeepEqual(gotRecord, record) {
				t.Fatalf("%s changed record: got=%#v want=%#v exists=%t err=%v", test.name, gotRecord, record, exists, err)
			}
			if _, exists, _ := records.Load("renamed"); exists {
				t.Fatalf("%s copied an unsupported record", test.name)
			}
			callsAfter := []int{len(adapter.materializeCalls), len(adapter.startCalls), len(adapter.stopCalls), len(adapter.enableCalls), len(adapter.disableCalls), len(adapter.restartCalls), len(adapter.removeCalls)}
			if !reflect.DeepEqual(callsAfter, callsBefore) || !reflect.DeepEqual(adapter.artifacts["db"], artifactBefore) || !reflect.DeepEqual(adapter.observed["db"], observationBefore) {
				t.Fatalf("%s mutated native state: calls=%v want=%v", test.name, callsAfter, callsBefore)
			}
		})
	}
}

func TestTypedScopeFailuresPreserveHighRiskCodes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	adapter.stageErr = codedTestError{code: "recovery_blocked", message: "injected compensation failure"}
	code, out, _ := invoke(t, "enable", "db", "--scope", "system", "--json")
	if code == 0 || !strings.Contains(out, `"code":"recovery_blocked"`) || !strings.Contains(out, `"riskLevel":"high"`) ||
		!strings.Contains(out, `"type":"resolve_connection"`) {
		t.Fatalf("typed stage recovery was downgraded: exit=%d out=%s", code, out)
	}
}

func TestSystemScopeOrphanIsVisibleBlockedAndResolvedInPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
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
	artifact.Scope = service.ScopeSystem
	artifact.Path = filepath.Join(home, "native", "db.system.service")
	adapter.artifacts["db"] = artifact
	observation := adapter.observed["db"]
	observation.Scope = service.ScopeSystem
	adapter.observed["db"] = observation
	materializedBefore := len(adapter.materializeCalls)

	if code, out, stderr := invoke(t, "status", "db", "--json"); code != 0 ||
		!strings.Contains(out, `"scope":"system"`) || !strings.Contains(out, `"serviceState":"registered"`) ||
		!strings.Contains(out, `"code":"scope_conflict"`) || strings.Contains(out, `"serviceState":"absent"`) || stderr != "" {
		t.Fatalf("status hid system orphan: exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, _ := invoke(t, "remove", "db", "--json"); code == 0 ||
		!strings.Contains(out, `"code":"scope_conflict"`) || !strings.Contains(out, `"riskLevel":"high"`) {
		t.Fatalf("remove crossed system orphan: exit=%d out=%s", code, out)
	}
	if code, out, _ := invoke(t, "enable", "db", "--scope", "system", "--json"); code == 0 ||
		!strings.Contains(out, `"code":"scope_conflict"`) || !strings.Contains(out, `"riskLevel":"high"`) || adapter.stageCalls != 0 {
		t.Fatalf("enable overwrote system orphan: exit=%d out=%s stage=%d", code, out, adapter.stageCalls)
	}
	if len(adapter.materializeCalls) != materializedBefore || adapter.artifacts["db"].Scope != service.ScopeSystem {
		t.Fatal("status/remove mutated the system orphan")
	}
	if code, out, stderr := invoke(t, "resolve", "db", "--use", "tunlite", "--json"); code != 0 {
		t.Fatalf("resolve exit=%d out=%s err=%s", code, out, stderr)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists || record.Scope != service.ScopeSystem || record.ArtifactPath != "" {
		t.Fatalf("resolve did not preserve stopped system intent: record=%#v exists=%t err=%v", record, exists, err)
	}
	if _, remains := adapter.artifacts["db"]; remains {
		t.Fatal("resolve retained a root artifact for stopped system intent")
	}
}

func TestAddCannotCreateUserTwinOfSystemScopeOrphan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	artifact, err := adapter.TargetScoped(service.ScopeSystem, "db")
	if err != nil {
		t.Fatal(err)
	}
	artifact.Content = []byte("existing system definition")
	adapter.artifacts["db"] = artifact
	adapter.observed["db"] = service.Observation{Scope: service.ScopeSystem, Installed: true, ProcessState: service.ProcessInactive, Artifact: append([]byte(nil), artifact.Content...), Platform: map[string]any{}}
	invoke := useFakeServiceAdapter(t, adapter)
	code, out, _ := invoke(t, "add", "db", "-D", "1080", "--json", "me@example.com")
	if code == 0 || !strings.Contains(out, `"code":"scope_conflict"`) || !strings.Contains(out, `"riskLevel":"high"`) ||
		!strings.Contains(out, `"type":"manage_native_service"`) {
		t.Fatalf("add created a user twin: exit=%d out=%s", code, out)
	}
	config, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := config.Load()
	if err != nil || len(document.Items) != 0 || adapter.artifacts["db"].Scope != service.ScopeSystem {
		t.Fatalf("failed add changed state: items=%v err=%v artifact=%#v", document.Items, err, adapter.artifacts["db"])
	}
}

func (f *fakeServiceAdapter) Target(name string) service.Artifact {
	return service.Artifact{Connection: name, Adapter: f.Name(), Scope: service.ScopeUser, UnitName: "fake-" + name, Path: filepath.Join(f.root, name+".service")}
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
	artifact.Scope = service.NormalizeScope(reference.Scope)
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
	observation.Scope = service.NormalizeScope(artifact.Scope)
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
	f.observed[reference.Connection] = service.Observation{Scope: service.NormalizeScope(reference.Scope), ProcessState: service.ProcessInactive, Platform: map[string]any{}}
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
	f.discoverMu.Lock()
	f.inspectCalls = append(f.inspectCalls, reference)
	f.discoverMu.Unlock()
	if err := f.fail("inspect"); err != nil {
		return service.Observation{Scope: service.NormalizeScope(reference.Scope), ProcessState: service.ProcessUnknown}, err
	}
	if observation, ok := f.observed[reference.Connection]; ok {
		return observation, nil
	}
	return service.Observation{Scope: service.NormalizeScope(reference.Scope), ProcessState: service.ProcessInactive, Platform: map[string]any{}}, nil
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

func TestHumanLifecycleResultPrioritizesPendingTransitionOverDrift(t *testing.T) {
	state := service.State{
		Connection:       "db",
		ServiceState:     service.ServiceRegistered,
		Mode:             service.ModePersistent,
		Scope:            service.ScopeSystem,
		ProcessState:     service.ProcessRunning,
		Health:           service.HealthHealthy,
		ConsistencyState: service.ConsistencyDrifted,
		Conditions: []core.Condition{{
			Code: "service_mode_drifted", Severity: "warning", Message: "native service mode is background but Tunlite recorded persistent",
		}},
	}
	result := serviceItemResult{Connection: "db", Outcome: "starting", State: &state}

	tests := []struct {
		name      string
		operation string
		language  language
		want      string
		reject    string
	}{
		{name: "English enable", operation: "enable", language: languageEnglish, want: "starting", reject: "needs attention"},
		{name: "Chinese migration", operation: "enable", language: languageChinese, want: "启动中", reject: "需要处理"},
		{name: "English restart", operation: "restart", language: languageEnglish, want: "restarting", reject: "needs attention"},
		{name: "Chinese restart", operation: "restart", language: languageChinese, want: "重启中", reject: "需要处理"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := renderServiceResults(IO{Out: &output, Err: &bytes.Buffer{}}, options{Language: test.language}, test.operation, []serviceItemResult{result}); err != nil {
				t.Fatal(err)
			}
			if text := output.String(); !strings.Contains(text, test.want) || strings.Contains(text, test.reject) {
				t.Fatalf("human result=%q want %q without %q", text, test.want, test.reject)
			}
		})
	}

	completed := result
	completed.Outcome = "succeeded"
	var output bytes.Buffer
	if err := renderServiceResults(IO{Out: &output, Err: &bytes.Buffer{}}, options{}, "enable", []serviceItemResult{completed}); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "needs attention") || strings.Contains(text, "starting") {
		t.Fatalf("completed drift must remain visible: %q", text)
	}
}

func TestLifecycleTransitionDoesNotChangeJSONResult(t *testing.T) {
	state := service.State{
		Connection: "db", ServiceState: service.ServiceRegistered, Mode: service.ModePersistent,
		Scope: service.ScopeSystem, ProcessState: service.ProcessStarting, Health: service.HealthUnknown,
		ConsistencyState: service.ConsistencyDrifted,
	}
	result := serviceItemResult{Connection: "db", Outcome: "starting", State: &state}
	var output bytes.Buffer
	if err := renderServiceResults(IO{Out: &output, Err: &bytes.Buffer{}}, options{JSON: true}, "enable", []serviceItemResult{result}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Outcome string              `json:"outcome"`
		Results []serviceItemResult `json:"results"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode %q: %v", output.String(), err)
	}
	if response.Outcome != "starting" || len(response.Results) != 1 || response.Results[0].Outcome != "starting" ||
		response.Results[0].State == nil || response.Results[0].State.ConsistencyState != service.ConsistencyDrifted {
		t.Fatalf("JSON lifecycle result changed: %#v", response)
	}
	if strings.Contains(output.String(), `"transition"`) {
		t.Fatalf("human-only transition leaked into JSON: %s", output.String())
	}
}

func TestTerminalLifecycleCompletionProgressUsesOperationAwareTransition(t *testing.T) {
	terminal := true
	tests := []struct {
		name      string
		operation string
		language  language
		want      string
	}{
		{name: "English start", operation: "start", language: languageEnglish, want: "[1/1] \"db\" starting.\n"},
		{name: "Chinese enable", operation: "enable", language: languageChinese, want: "[1/1] “db”启动中。\n"},
		{name: "English restart", operation: "restart", language: languageEnglish, want: "[1/1] \"db\" restarting.\n"},
		{name: "Chinese restart", operation: "restart", language: languageChinese, want: "[1/1] “db”重启中。\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			renderServiceCompletionProgress(IO{Err: &stderr, Terminal: &terminal}, options{Language: test.language}, 1, 1, test.operation, "db", "starting")
			if got := stderr.String(); got != test.want {
				t.Fatalf("completion progress=%q want=%q", got, test.want)
			}
		})
	}

	var stderr bytes.Buffer
	renderServiceCompletionProgress(IO{Err: &stderr, Terminal: &terminal}, options{JSON: true}, 1, 1, "restart", "db", "starting")
	if stderr.Len() != 0 {
		t.Fatalf("JSON completion emitted human progress: %q", stderr.String())
	}
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

func useFakeServiceAdapter(t *testing.T, adapter service.Adapter) func(*testing.T, ...string) (int, string, string) {
	t.Helper()
	authorizer := &fakePrivilegeAuthorizer{}
	return func(t *testing.T, args ...string) (int, string, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		budget := lifecycleObservationBudget{Overall: time.Millisecond, PerItem: time.Millisecond, PollInterval: time.Millisecond}
		code := runContextWithOptions(context.Background(), args, IO{Out: &out, Err: &stderr}, options{
			serviceAdapter: func() (service.Adapter, error) { return adapter, nil }, lifecycleBudget: &budget,
			privilegeAuthorizer: authorizer,
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

func TestSystemLifecycleUsesPersistentOnlyAndPreservesScopeIntent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	authorizer := &fakePrivilegeAuthorizer{}
	invoke := func(args ...string) (int, string) {
		var out bytes.Buffer
		budget := lifecycleObservationBudget{Overall: time.Millisecond, PerItem: time.Millisecond, PollInterval: time.Millisecond}
		code := runContextWithOptions(context.Background(), args, IO{Out: &out, Err: io.Discard}, options{
			serviceAdapter:  func() (service.Adapter, error) { return adapter, nil },
			lifecycleBudget: &budget, privilegeAuthorizer: authorizer,
		})
		return code, out.String()
	}
	if code, out := invoke("add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s", code, out)
	}
	if code, out := invoke("enable", "db", "--scope", "system", "--json"); code != 0 {
		t.Fatalf("system enable exit=%d out=%s", code, out)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists || record.Scope != service.ScopeSystem || record.Mode != service.ModePersistent {
		t.Fatalf("system record=%#v exists=%t err=%v", record, exists, err)
	}
	startCalls, stopCalls := len(adapter.startCalls), len(adapter.stopCalls)
	if code, out := invoke("start", "db", "--json"); code == 0 || !strings.Contains(out, `"code":"persistent_connection"`) {
		t.Fatalf("system persistent start exit=%d out=%s", code, out)
	}
	if code, out := invoke("stop", "db", "--json"); code == 0 || !strings.Contains(out, `"code":"persistent_connection"`) {
		t.Fatalf("system persistent stop exit=%d out=%s", code, out)
	}
	if len(adapter.startCalls) != startCalls || len(adapter.stopCalls) != stopCalls {
		t.Fatal("system persistent start/stop reached temporary adapter methods")
	}
	authorizedBeforeRestart := authorizer.calls
	if code, out := invoke("restart", "db", "--json"); code != 0 {
		t.Fatalf("system restart exit=%d out=%s", code, out)
	}
	if authorizer.calls != authorizedBeforeRestart || len(adapter.restartCalls) != 1 {
		t.Fatalf("system restart auth=%d want=%d restarts=%v", authorizer.calls, authorizedBeforeRestart, adapter.restartCalls)
	}
	recordAfterRestart, _, _ := records.Load("db")
	if !reflect.DeepEqual(recordAfterRestart, record) {
		t.Fatalf("restart changed lifecycle record: got=%#v want=%#v", recordAfterRestart, record)
	}
	if code, out := invoke("disable", "db", "--json"); code != 0 {
		t.Fatalf("system disable exit=%d out=%s", code, out)
	}
	disabled, exists, err := records.Load("db")
	if err != nil || !exists || disabled.Scope != service.ScopeSystem || disabled.Mode != service.ModeStopped || disabled.ArtifactPath != "" {
		t.Fatalf("disabled record=%#v exists=%t err=%v", disabled, exists, err)
	}
	if _, exists := adapter.artifacts["db"]; exists {
		t.Fatal("system disable retained fake root artifact")
	}
	if code, out := invoke("start", "db", "--json"); code == 0 || !strings.Contains(out, `"code":"system_scope_requires_enable"`) {
		t.Fatalf("disabled system start exit=%d out=%s", code, out)
	}
	if code, out := invoke("enable", "db", "--json"); code != 0 {
		t.Fatalf("system re-enable exit=%d out=%s", code, out)
	}
	reenabled, _, _ := records.Load("db")
	if reenabled.Scope != service.ScopeSystem || reenabled.Mode != service.ModePersistent || reenabled.ArtifactPath == "" {
		t.Fatalf("reenabled record=%#v", reenabled)
	}
	if code, out := invoke("disable", "db", "--json"); code != 0 {
		t.Fatalf("second system disable exit=%d out=%s", code, out)
	}
	authorizedBeforeRemove := authorizer.calls
	removeCalls := len(adapter.removeCalls)
	if code, out := invoke("remove", "db", "--json"); code != 0 {
		t.Fatalf("system remove exit=%d out=%s", code, out)
	}
	if authorizer.calls != authorizedBeforeRemove || len(adapter.removeCalls) != removeCalls {
		t.Fatalf("system remove used privileged/native removal: auth=%d/%d removes=%d/%d", authorizer.calls, authorizedBeforeRemove, len(adapter.removeCalls), removeCalls)
	}
	if _, exists, err := records.Load("db"); err != nil || exists {
		t.Fatalf("system remove retained record: exists=%t err=%v", exists, err)
	}
}

func TestDisableStoppedSystemServiceRemovesResidualRootArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	authorizer := &fakePrivilegeAuthorizer{}
	terminal := true
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := runContextWithOptions(context.Background(), args, IO{Out: &out, Err: io.Discard, Terminal: &terminal}, options{
			serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
			privilegeAuthorizer: authorizer,
		})
		return code, out.String()
	}
	if code, out := run("add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s", code, out)
	}
	if code, out := run("enable", "db", "--scope", "system", "--json"); code != 0 {
		t.Fatalf("enable exit=%d out=%s", code, out)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists {
		t.Fatalf("record exists=%t err=%v", exists, err)
	}
	record.Mode = service.ModeStopped
	if err := records.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	observation := adapter.observed["db"]
	observation.Installed = true
	observation.AutoStart = false
	observation.ProcessState = service.ProcessInactive
	adapter.observed["db"] = observation
	authorizer.calls = 0
	adapter.preflightCalls = 0

	code, out := run("disable", "db", "--json")
	if code != 0 || authorizer.calls != 1 || adapter.preflightCalls != 1 {
		t.Fatalf("disable exit=%d auth=%d preflight=%d out=%s", code, authorizer.calls, adapter.preflightCalls, out)
	}
	if _, exists := adapter.artifacts["db"]; exists {
		t.Fatal("disable retained the residual system artifact")
	}
	disabled, exists, err := records.Load("db")
	if err != nil || !exists || disabled.Scope != service.ScopeSystem || disabled.Mode != service.ModeStopped || disabled.ArtifactPath != "" {
		t.Fatalf("disabled record=%+v exists=%t err=%v", disabled, exists, err)
	}
	config, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, connection := configstore.Find(&document, "db"); connection == nil {
		t.Fatal("disable removed the connection declaration")
	}
}

func prepareSystemImportReplacement(t *testing.T, home string, adapter *scopedFakeServiceAdapter) (string, configstore.Document) {
	t.Helper()
	invoke := useFakeServiceAdapter(t, adapter)
	for index, name := range []string{"one", "two"} {
		port := strconv.Itoa(1081 + index)
		if code, out, stderr := invoke(t, "add", name, "-D", port, "--json", "me@example.com"); code != 0 {
			t.Fatalf("add %s exit=%d out=%s err=%s", name, code, out, stderr)
		}
		if code, out, stderr := invoke(t, "enable", name, "--scope", "system", "--json", "--non-interactive"); code != 0 {
			t.Fatalf("enable %s exit=%d out=%s err=%s", name, code, out, stderr)
		}
	}
	target, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := target.Load()
	if err != nil {
		t.Fatal(err)
	}
	sourceDocument := configstore.Empty()
	for _, connection := range before.Items {
		changed := cloneConnection(connection)
		config, err := sshdriver.DecodeConfig(changed.Spec.Config)
		if err != nil {
			t.Fatal(err)
		}
		config.Forwards[0].SrcPort += 100
		changed.Spec.Config, err = json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		sourceDocument.Items = append(sourceDocument.Items, changed)
	}
	sourcePath := filepath.Join(t.TempDir(), "system-import.json")
	if err := configstore.New(sourcePath).Write(context.Background(), sourceDocument, false); err != nil {
		t.Fatal(err)
	}
	adapter.preflightCalls = 0
	adapter.stageCalls = 0
	adapter.readOnlyChecks = 0
	return sourcePath, before
}

func runSystemImportReplacement(t *testing.T, sourcePath string, adapter *scopedFakeServiceAdapter, authorizer *fakePrivilegeAuthorizer) (int, string) {
	t.Helper()
	terminal := true
	var out bytes.Buffer
	code := runContextWithOptions(context.Background(), []string{"import", sourcePath, "--force", "--json"}, IO{
		Out: &out, Err: io.Discard, Terminal: &terminal,
	}, options{
		serviceAdapter:      func() (service.Adapter, error) { return adapter, nil },
		privilegeAuthorizer: authorizer,
	})
	return code, out.String()
}

func TestImportSystemReplacementsAuthorizesBatchOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	sourcePath, before := prepareSystemImportReplacement(t, home, adapter)
	authorizer := &fakePrivilegeAuthorizer{}

	code, out := runSystemImportReplacement(t, sourcePath, adapter, authorizer)
	if code != 0 || authorizer.calls != 1 || authorizer.request.connections != 2 || adapter.stageCalls != 2 {
		t.Fatalf("import exit=%d auth=%d connections=%d stage=%d out=%s", code, authorizer.calls,
			authorizer.request.connections, adapter.stageCalls, out)
	}
	target, _ := connectionStore(options{})
	after, err := target.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		_, oldConnection := configstore.Find(&before, name)
		_, newConnection := configstore.Find(&after, name)
		if oldConnection == nil || newConnection == nil || connectionsEqual(*oldConnection, *newConnection) {
			t.Fatalf("connection %s was not replaced: old=%+v new=%+v", name, oldConnection, newConnection)
		}
	}
}

func TestImportSystemReplacementPreflightFailurePerformsZeroWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	sourcePath, beforeDocument := prepareSystemImportReplacement(t, home, adapter)
	records, _ := serviceRecordStore()
	beforeRecords := map[string]service.Record{}
	beforeArtifacts := map[string]service.Artifact{}
	for _, name := range []string{"one", "two"} {
		beforeRecords[name], _, _ = records.Load(name)
		beforeArtifacts[name] = adapter.artifacts[name]
	}
	adapter.failPreflight = 2
	adapter.preflightErr = &service.PrivilegeError{Operation: "second system replacement preflight", Cause: errors.New("authorization cache unavailable")}
	authorizer := &fakePrivilegeAuthorizer{}

	code, out := runSystemImportReplacement(t, sourcePath, adapter, authorizer)
	if code != 4 || authorizer.calls != 1 || adapter.preflightCalls != 2 || adapter.stageCalls != 0 {
		t.Fatalf("import exit=%d auth=%d preflight=%d stage=%d out=%s", code, authorizer.calls,
			adapter.preflightCalls, adapter.stageCalls, out)
	}
	target, _ := connectionStore(options{})
	afterDocument, err := target.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterDocument, beforeDocument) {
		t.Fatalf("preflight failure changed declarations: before=%+v after=%+v", beforeDocument, afterDocument)
	}
	for _, name := range []string{"one", "two"} {
		record, _, _ := records.Load(name)
		if !reflect.DeepEqual(record, beforeRecords[name]) || !reflect.DeepEqual(adapter.artifacts[name], beforeArtifacts[name]) {
			t.Fatalf("preflight failure changed %s: record=%+v artifact=%+v", name, record, adapter.artifacts[name])
		}
	}
}

func TestImportSystemReplacementRuntimeFailureIsPartial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := &scopedFakeServiceAdapter{fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native"))}
	sourcePath, beforeDocument := prepareSystemImportReplacement(t, home, adapter)
	records, _ := serviceRecordStore()
	beforeOne, _, _ := records.Load("one")
	beforeTwo, _, _ := records.Load("two")
	adapter.failStage = 2
	authorizer := &fakePrivilegeAuthorizer{}

	code, out := runSystemImportReplacement(t, sourcePath, adapter, authorizer)
	if code != exitPartial || authorizer.calls != 1 || adapter.stageCalls != 2 || !strings.Contains(out, `"outcome":"partial"`) {
		t.Fatalf("import exit=%d auth=%d stage=%d out=%s", code, authorizer.calls, adapter.stageCalls, out)
	}
	target, _ := connectionStore(options{})
	afterDocument, err := target.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, beforeOneConnection := configstore.Find(&beforeDocument, "one")
	_, beforeTwoConnection := configstore.Find(&beforeDocument, "two")
	_, afterOneConnection := configstore.Find(&afterDocument, "one")
	_, afterTwoConnection := configstore.Find(&afterDocument, "two")
	if connectionsEqual(*beforeOneConnection, *afterOneConnection) || !connectionsEqual(*beforeTwoConnection, *afterTwoConnection) {
		t.Fatalf("unexpected partial declarations: before=%+v after=%+v", beforeDocument, afterDocument)
	}
	afterOne, _, _ := records.Load("one")
	afterTwo, _, _ := records.Load("two")
	if reflect.DeepEqual(afterOne, beforeOne) || !reflect.DeepEqual(afterTwo, beforeTwo) {
		t.Fatalf("unexpected partial records: one before=%+v after=%+v two before=%+v after=%+v", beforeOne, afterOne, beforeTwo, afterTwo)
	}
}

func TestImportForceWithoutServiceManagerAddsAndUpdatesForegroundOnlyConnection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	sourcePath := filepath.Join(t.TempDir(), "foreground-import.json")
	writeSource := func(port int, force bool) {
		document := configstore.Empty()
		document.Items = append(document.Items, core.Connection{
			ConfigVersion: core.ConfigVersion,
			Kind:          core.Kind,
			Metadata:      core.Metadata{Name: "db"},
			Spec: core.ConnectionSpec{Driver: sshdriver.ID, Config: json.RawMessage(fmt.Sprintf(
				`{"host":"me@example.com","port":22,"forwards":[{"type":"dynamic","bind":"127.0.0.1","srcPort":%d}]}`,
				port,
			))},
		})
		if err := configstore.New(sourcePath).Write(context.Background(), document, force); err != nil {
			t.Fatal(err)
		}
	}
	adapterCalls := 0
	runImport := func() (int, string) {
		var out bytes.Buffer
		code := runContextWithOptions(context.Background(), []string{"import", sourcePath, "--force", "--json"}, IO{
			Out: &out, Err: io.Discard,
		}, options{serviceAdapter: func() (service.Adapter, error) {
			adapterCalls++
			return nil, errors.New("no native service manager")
		}})
		return code, out.String()
	}

	writeSource(1080, false)
	code, out := runImport()
	if code != 0 || !strings.Contains(out, `"outcome":"added"`) || !strings.Contains(out, `"code":"foreground_only"`) || adapterCalls != 1 {
		t.Fatalf("add import exit=%d adapter=%d out=%s", code, adapterCalls, out)
	}
	writeSource(1081, true)
	code, out = runImport()
	if code != 0 || !strings.Contains(out, `"outcome":"updated"`) || !strings.Contains(out, `"code":"foreground_only"`) || adapterCalls != 2 {
		t.Fatalf("update import exit=%d adapter=%d out=%s", code, adapterCalls, out)
	}
	target, err := connectionStore(options{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := target.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, connection := configstore.Find(&document, "db")
	if connection == nil {
		t.Fatalf("foreground-only update was not persisted: %+v", connection)
	}
	config, err := sshdriver.DecodeConfig(connection.Spec.Config)
	if err != nil || len(config.Forwards) != 1 || config.Forwards[0].SrcPort != 1081 {
		t.Fatalf("foreground-only update was not persisted: config=%+v err=%v", config, err)
	}
}

func TestSetWithoutServiceManagerReportsForegroundOnlyOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := runContextWithOptions(context.Background(), args, IO{Out: &out, Err: io.Discard}, options{
			serviceAdapter: func() (service.Adapter, error) {
				return nil, errors.New("no native service manager")
			},
		})
		return code, out.String()
	}
	if code, out := run("add", "no-manager", "-D", "1080", "me@example.com", "--json"); code != 0 {
		t.Fatalf("add exit=%d out=%s", code, out)
	}
	code, out := run("set", "no-manager", "-p", "2222", "--json", "--non-interactive")
	if code != 0 {
		t.Fatalf("set exit=%d out=%s", code, out)
	}
	var response struct {
		Conditions []condition `json:"conditions"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Conditions) != 1 || response.Conditions[0].Code != "foreground_only" || response.Conditions[0].Connection != "no-manager" {
		t.Fatalf("conditions=%+v out=%s", response.Conditions, out)
	}
}

func TestConditionDeduplicationPreservesDistinctStableIdentities(t *testing.T) {
	base := condition{
		Code: "endpoint_conflict", Severity: "warning", Connection: "db", Driver: sshdriver.ID,
		Message: "endpoint conflict", Actions: []core.Action{{Type: "change_forward", Parameters: map[string]any{"endpoint": "L:127.0.0.1:1080"}}},
	}
	differentMessage := base
	differentMessage.Message = "different endpoint conflict"
	differentAction := base
	differentAction.Actions = []core.Action{{Type: "change_forward", Parameters: map[string]any{"endpoint": "L:127.0.0.1:1081"}}}
	got := deduplicateConditions([]condition{base, base, differentMessage, differentAction})
	if len(got) != 3 || !reflect.DeepEqual(got, []condition{base, differentMessage, differentAction}) {
		t.Fatalf("deduplicated conditions=%+v", got)
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

type launchdDryRunRunner struct {
	mutations int
	disabled  bool
}

func (r *launchdDryRunRunner) Run(_ context.Context, _ string, args []string, _ service.Streams) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "print-disabled":
		if r.disabled {
			return []byte(`"dev.tunlite.connection.db" => true`), nil
		}
		return []byte("disabled services = {}\n"), nil
	case "print":
		return []byte("Could not find service"), errors.New("not found")
	default:
		r.mutations++
		if args[0] == "disable" {
			r.disabled = true
		}
		if args[0] == "enable" {
			r.disabled = false
		}
		return nil, nil
	}
}

func TestLaunchdStartDryRunDoesNotCreateActiveMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	agentDir := filepath.Join(home, "LaunchAgents")
	logDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &launchdDryRunRunner{}
	adapter := service.NewLaunchd(agentDir, logDir, 501, runner)
	invoke := func(args ...string) (int, string, string) {
		var out, stderr bytes.Buffer
		code := runContext(context.Background(), args, IO{Out: &out, Err: &stderr}, func() (service.Adapter, error) { return adapter, nil })
		return code, out.String(), stderr.String()
	}
	if code, out, stderr := invoke("add", "db", "-D", "1080", "--json", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	baseline := snapshotFiles(t, logDir)
	baselineMutations := runner.mutations
	if code, out, stderr := invoke("start", "db", "--dry-run", "--json"); code != 0 {
		t.Fatalf("dry-run start exit=%d out=%s err=%s", code, out, stderr)
	}
	assertFileSnapshot(t, baseline, snapshotFiles(t, logDir))
	if runner.mutations != baselineMutations {
		t.Fatalf("dry-run invoked %d launchctl mutations", runner.mutations-baselineMutations)
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
	decoder := json.NewDecoder(strings.NewReader(out))
	var header struct {
		ProtocolVersion int    `json:"protocolVersion"`
		Outcome         string `json:"outcome"`
		Command         string `json:"command"`
		Connection      string `json:"connection"`
		Stream          string `json:"stream"`
	}
	if err := decoder.Decode(&header); err != nil {
		t.Fatalf("invalid NDJSON stream header: %v\n%s", err, out)
	}
	if header.ProtocolVersion != ProtocolVersion || header.Outcome != "starting" || header.Command != "logs" || header.Connection != "db" || header.Stream != "logs" {
		t.Fatalf("unexpected stream header: %#v", header)
	}
	var record struct {
		ProtocolVersion int    `json:"protocolVersion"`
		Outcome         string `json:"outcome"`
		Connection      string `json:"connection"`
		Stream          string `json:"stream"`
		Line            string `json:"line"`
	}
	if err := decoder.Decode(&record); err != nil {
		t.Fatalf("invalid NDJSON record: %v\n%s", err, out)
	}
	if record.ProtocolVersion != ProtocolVersion || record.Outcome != "log" || record.Connection != "db" || record.Stream != "stdout" || !strings.Contains(record.Line, "provider log") {
		t.Fatalf("unexpected log record: %#v", record)
	}
}

func TestSnapshotLogsJSONPreservesSourceStream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUNLITE_HOME", home)
	setTestSSH(t)
	adapter := newFakeServiceAdapter(filepath.Join(home, "native"))
	invoke := useFakeServiceAdapter(t, adapter)
	if code, out, stderr := invoke(t, "add", "db", "-D", "1080", "me@example.com"); code != 0 {
		t.Fatalf("add exit=%d out=%s err=%s", code, out, stderr)
	}
	code, out, stderr := invoke(t, "logs", "db", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("logs exit=%d out=%s err=%s", code, out, stderr)
	}
	var response struct {
		Command string      `json:"command"`
		Stream  string      `json:"stream"`
		Logs    []logRecord `json:"logs"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if response.Command != "logs" || response.Stream != "snapshot" || len(response.Logs) != 1 || response.Logs[0].Stream != "stdout" {
		t.Fatalf("unexpected log snapshot: %#v", response)
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

func TestManagePreservesDiscoveredServiceScope(t *testing.T) {
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
	artifact := adapter.artifacts["db"]
	artifact.Scope = service.ScopeSystem
	adapter.artifacts["db"] = artifact
	observation := adapter.observed["db"]
	observation.Scope = service.ScopeSystem
	adapter.observed["db"] = observation
	if code, out, stderr := invoke(t, "manage", "fake-db", "--name", "db", "--json", "--non-interactive"); code != 0 {
		t.Fatalf("manage exit=%d out=%s err=%s", code, out, stderr)
	}
	records, err := serviceRecordStore()
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := records.Load("db")
	if err != nil || !exists || record.Scope != service.ScopeSystem {
		t.Fatalf("record=%+v exists=%t err=%v", record, exists, err)
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
	targetDiscoverCall := adapter.discoverCalls + 2
	adapter.discoverHook = func(call int) {
		if call == targetDiscoverCall {
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
	result := enableService(canceled, adapter, records, *connection, store.Path(), "", false, IO{}, options{})
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
		!strings.Contains(out, `"consistencyState":"blocked"`) || !strings.Contains(out, `"code":"scope_conflict"`) ||
		!strings.Contains(out, `"type":"resolve_connection"`) {
		t.Fatalf("status exit=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, _ := invoke(t, "remove", "db", "--json"); code == 0 || !strings.Contains(out, `"code":"scope_conflict"`) {
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
