package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type launchdScopeRunner struct {
	fakeRunner
	loaded        map[string]bool
	disabled      map[string]bool
	failKickstart bool
	failBootstrap bool
	failBootout   string
}

func newLaunchdScopeRunner(t *testing.T) *launchdScopeRunner {
	t.Helper()
	runner := &launchdScopeRunner{loaded: map[string]bool{}, disabled: map[string]bool{}}
	runner.fakeRunner.run = runner.execute
	return runner
}

func (r *launchdScopeRunner) execute(command string, args []string) ([]byte, error) {
	if command == "sudo" {
		if len(args) < 3 || args[0] != "-n" || args[1] != "--" {
			return nil, errors.New("unsafe sudo invocation")
		}
		switch args[2] {
		case "/usr/bin/true":
			return nil, nil
		case "/usr/bin/install":
			source, destination := args[len(args)-2], args[len(args)-1]
			raw, err := os.ReadFile(source)
			if err == nil {
				err = os.WriteFile(destination, raw, 0o644)
			}
			return nil, err
		case "/bin/rm":
			err := os.Remove(args[len(args)-1])
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			return nil, err
		case "/bin/launchctl":
			return r.launchctl(args[3:])
		}
		return nil, errors.New("unexpected privileged command")
	}
	if command == "launchctl" {
		return r.launchctl(args)
	}
	return nil, nil
}

func (r *launchdScopeRunner) launchctl(args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("missing launchctl operation")
	}
	switch args[0] {
	case "print-disabled":
		var output strings.Builder
		for target, disabled := range r.disabled {
			if strings.HasPrefix(target, args[1]+"/") {
				label := strings.TrimPrefix(target, args[1]+"/")
				output.WriteString(`"` + label + `" => `)
				if disabled {
					output.WriteString("true\n")
				} else {
					output.WriteString("false\n")
				}
			}
		}
		return []byte(output.String()), nil
	case "print":
		if r.loaded[args[1]] {
			return []byte("state = running\npid = 42\n"), nil
		}
		return []byte("Could not find service"), errors.New("not found")
	case "enable":
		r.disabled[args[1]] = false
		return nil, nil
	case "disable":
		r.disabled[args[1]] = true
		return nil, nil
	case "bootstrap":
		if r.failBootstrap {
			r.failBootstrap = false
			return nil, errors.New("bootstrap failed")
		}
		label := strings.TrimSuffix(filepath.Base(args[2]), ".plist")
		r.loaded[args[1]+"/"+label] = true
		return nil, nil
	case "kickstart":
		if r.failKickstart {
			r.failKickstart = false
			return nil, errors.New("kickstart failed")
		}
		r.loaded[args[len(args)-1]] = true
		return nil, nil
	case "bootout":
		if r.failBootout == args[1] {
			return nil, errors.New("bootout failed")
		}
		if !r.loaded[args[1]] {
			return []byte("Could not find service"), errors.New("not found")
		}
		delete(r.loaded, args[1])
		return nil, nil
	}
	return nil, errors.New("unexpected launchctl operation")
}

func newScopedLaunchd(t *testing.T, runner CommandRunner) (*Launchd, string, string) {
	t.Helper()
	root := t.TempDir()
	agents := filepath.Join(root, "agents")
	daemons := filepath.Join(root, "daemons")
	logs := filepath.Join(root, "logs")
	for _, directory := range []string{agents, daemons} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	adapter := NewLaunchdWithSystemScope(agents, daemons, logs, 501, "alice", runner)
	adapter.transitionTimeout = 100 * time.Millisecond
	adapter.transitionStable = 2 * time.Millisecond
	adapter.transitionPoll = time.Millisecond
	return adapter, agents, daemons
}

func TestLaunchdSystemBuildUsesDaemonPathAndNonRootUser(t *testing.T) {
	adapter, _, daemons := newScopedLaunchd(t, newLaunchdScopeRunner(t))
	artifact, err := adapter.BuildScoped(ScopeSystem, "db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Scope != ScopeSystem || filepath.Dir(artifact.Path) != daemons {
		t.Fatalf("artifact=%#v", artifact)
	}
	content := string(artifact.Content)
	if !strings.Contains(content, "<key>UserName</key>\n  <string>alice</string>") || strings.Contains(content, "<string>root</string>") {
		t.Fatalf("system plist has unsafe identity:\n%s", content)
	}
	userArtifact, err := adapter.BuildScoped(ScopeUser, "db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if launchdDemandPath(adapter.logDir, userArtifact) == launchdDemandPath(adapter.logDir, artifact) {
		t.Fatalf("user and system scopes share demand marker %q", launchdDemandPath(adapter.logDir, artifact))
	}
	if !strings.Contains(content, launchdDemandPath(adapter.logDir, artifact)) || !strings.Contains(string(userArtifact.Content), launchdDemandPath(adapter.logDir, userArtifact)) {
		t.Fatal("scoped plist does not reference its own controlled demand marker")
	}
	value := testPlan()
	value.Exec.Environment["SSH_AUTH_SOCK"] = "/tmp/agent.sock"
	if _, err := adapter.BuildScoped(ScopeSystem, "db", value); err == nil {
		t.Fatal("system plist accepted SSH_AUTH_SOCK")
	}
}

func TestLaunchdSystemInstallUsesNonInteractiveSingleCommandAndRollsBack(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	artifact, err := adapter.BuildScoped(ScopeSystem, "db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	transition, err := StageScopeInstall(context.Background(), adapter, artifact, ModePersistent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(artifact.Path)
	if err != nil || string(raw) != string(artifact.Content) {
		t.Fatalf("installed artifact mismatch: err=%v", err)
	}
	var installArgs []string
	for _, call := range runner.calls {
		if call.command == "sudo" && slices.Contains(call.args, "/usr/bin/install") {
			installArgs = call.args
		}
		if call.command == "sudo" && (len(call.args) < 2 || call.args[0] != "-n" || call.args[1] != "--") {
			t.Fatalf("interactive sudo call: %#v", call)
		}
	}
	joined := strings.Join(installArgs, " ")
	for _, expected := range []string{"/usr/bin/install -S", "-o root", "-g wheel", "-m 0644"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("install command missing %q: %v", expected, installArgs)
		}
	}

	failing := newLaunchdScopeRunner(t)
	failing.failBootstrap = true
	rollbackAdapter, _, _ := newScopedLaunchd(t, failing)
	rollbackArtifact, _ := rollbackAdapter.BuildScoped(ScopeSystem, "broken", testPlan())
	if _, err := StageScopeInstall(context.Background(), rollbackAdapter, rollbackArtifact, ModePersistent, nil); err == nil {
		t.Fatal("failed system start unexpectedly succeeded")
	}
	if _, err := os.Stat(rollbackArtifact.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed install left system artifact: %v", err)
	}
}

func TestLaunchdScopeTransitionCommitsAndFailureRestoresSource(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	userArtifact, _ := adapter.BuildScoped(ScopeUser, "db", testPlan())
	if err := os.WriteFile(userArtifact.Path, userArtifact.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	userTarget := "gui/501/" + userArtifact.UnitName
	runner.loaded[userTarget] = true
	runner.disabled[userTarget] = false
	systemArtifact, _ := adapter.BuildScoped(ScopeSystem, "db", testPlan())
	transition, err := StageScopeTransition(context.Background(), adapter, userArtifact.Reference(), systemArtifact, ModePersistent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userArtifact.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit left source artifact: %v", err)
	}
	if _, err := os.Stat(systemArtifact.Path); err != nil {
		t.Fatalf("commit removed target artifact: %v", err)
	}
	if active, err := launchdDemandActive(adapter.logDir, userArtifact); err != nil || active {
		t.Fatalf("commit retained source demand: active=%t err=%v", active, err)
	}
	if active, err := launchdDemandActive(adapter.logDir, systemArtifact); err != nil || !active {
		t.Fatalf("commit lost target demand: active=%t err=%v", active, err)
	}

	rollbackRunner := newLaunchdScopeRunner(t)
	rollbackAdapter, _, _ := newScopedLaunchd(t, rollbackRunner)
	rollbackUser, _ := rollbackAdapter.BuildScoped(ScopeUser, "rollback", testPlan())
	if err := os.WriteFile(rollbackUser.Path, rollbackUser.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	rollbackUserTarget := "gui/501/" + rollbackUser.UnitName
	rollbackRunner.loaded[rollbackUserTarget] = true
	rollbackRunner.disabled[rollbackUserTarget] = false
	rollbackRunner.failBootstrap = true
	rollbackSystem, _ := rollbackAdapter.BuildScoped(ScopeSystem, "rollback", testPlan())
	if _, err := StageScopeTransition(context.Background(), rollbackAdapter, rollbackUser.Reference(), rollbackSystem, ModePersistent, nil); err == nil {
		t.Fatal("failed transition unexpectedly succeeded")
	}
	if _, err := os.Stat(rollbackUser.Path); err != nil || !rollbackRunner.loaded[rollbackUserTarget] {
		t.Fatalf("rollback did not restore source: stat=%v loaded=%v", err, rollbackRunner.loaded)
	}
	if _, err := os.Stat(rollbackSystem.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left target artifact: %v", err)
	}
	if active, err := launchdDemandActive(rollbackAdapter.logDir, rollbackUser); err != nil || active {
		t.Fatalf("rollback did not preserve absent source demand drift: active=%t err=%v", active, err)
	}
	if active, err := launchdDemandActive(rollbackAdapter.logDir, rollbackSystem); err != nil || active {
		t.Fatalf("rollback retained target demand: active=%t err=%v", active, err)
	}
}

func TestLaunchdRejectsSameLabelAcrossScopesAndDiscoversSystemScope(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	userArtifact, _ := adapter.BuildScoped(ScopeUser, "db", testPlan())
	systemArtifact, _ := adapter.BuildScoped(ScopeSystem, "db", testPlan())
	if err := os.WriteFile(userArtifact.Path, userArtifact.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(systemArtifact.Path, systemArtifact.Content, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.Inspect(context.Background(), userArtifact.Reference())
	var conflict *ScopeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("dual-domain identity error=%T %v", err, err)
	}
	if err := os.Remove(userArtifact.Path); err != nil {
		t.Fatal(err)
	}
	candidates, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Reference.Scope != ScopeSystem || candidates[0].Observation.Scope != ScopeSystem {
		t.Fatalf("system discovery=%#v", candidates)
	}
}

func TestLaunchdDiscoveryUsesDirectoryScopeForUserNameAndDemandValidation(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	userArtifact, err := adapter.BuildScoped(ScopeUser, "db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	systemArtifact, err := adapter.BuildScoped(ScopeSystem, "db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	removeUserName := func(raw string) string {
		return strings.Replace(raw, "  <key>UserName</key>\n  <string>alice</string>\n", "", 1)
	}
	for _, test := range []struct {
		name    string
		scope   Scope
		content string
		want    int
	}{
		{name: "valid user", scope: ScopeUser, content: string(userArtifact.Content), want: 1},
		{name: "user directory rejects UserName and system marker", scope: ScopeUser, content: string(systemArtifact.Content)},
		{name: "valid system", scope: ScopeSystem, content: string(systemArtifact.Content), want: 1},
		{name: "system missing UserName", scope: ScopeSystem, content: removeUserName(string(systemArtifact.Content))},
		{name: "system foreign UserName", scope: ScopeSystem, content: strings.Replace(string(systemArtifact.Content), "<string>alice</string>", "<string>bob</string>", 1)},
		{name: "system root UserName", scope: ScopeSystem, content: strings.Replace(string(systemArtifact.Content), "<string>alice</string>", "<string>root</string>", 1)},
		{name: "system wrong marker", scope: ScopeSystem, content: strings.Replace(string(systemArtifact.Content), ".db.system.active", ".db.user.active", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "candidate.plist"), []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := adapter.discoverDirectory(context.Background(), test.scope, directory, entries, map[string]Scope{})
			if err != nil || len(candidates) != test.want {
				t.Fatalf("candidates=%#v err=%v", candidates, err)
			}
			if test.want == 1 && candidates[0].Reference.Scope != test.scope {
				t.Fatalf("candidate scope=%s want=%s", candidates[0].Reference.Scope, test.scope)
			}
		})
	}
}

func TestLaunchdPrivilegeFailureIsTypedAndNeverPrompts(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
		if command == "sudo" {
			return nil, errors.New("a password is required")
		}
		return runner.execute(command, args)
	}
	adapter, _, _ := newScopedLaunchd(t, runner)
	artifact, _ := adapter.BuildScoped(ScopeSystem, "db", testPlan())
	err := PreflightScopeInstall(context.Background(), adapter, artifact)
	var privilege *PrivilegeError
	if !errors.As(err, &privilege) || privilege.Code() != "privilege_required" {
		t.Fatalf("privilege error=%T %v", err, err)
	}
	var sudoCall *runnerCall
	for index := range runner.calls {
		if runner.calls[index].command == "sudo" {
			sudoCall = &runner.calls[index]
			break
		}
	}
	if sudoCall == nil || !slices.Equal(sudoCall.args[:2], []string{"-n", "--"}) {
		t.Fatalf("sudo was not non-interactive: %#v", runner.calls)
	}
}

func TestLaunchdTransitionRequiresContinuousRunningWindowAndCreatesLogs(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	adapter.transitionStable = 25 * time.Millisecond
	adapter.transitionTimeout = 200 * time.Millisecond
	artifact, err := adapter.BuildScoped(ScopeSystem, "stable", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	transition, err := StageScopeInstall(context.Background(), adapter, artifact, ModePersistent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < adapter.transitionStable {
		t.Fatalf("transition accepted first running observation after %s; stable window=%s", elapsed, adapter.transitionStable)
	}
	if _, err := os.Stat(adapter.logDir); err != nil {
		t.Fatalf("direct scope install did not create log directory: %v", err)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchdTransitionHealthFailureResetsStableWindow(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	adapter.transitionStable = 30 * time.Millisecond
	adapter.transitionPoll = 5 * time.Millisecond
	adapter.transitionTimeout = 200 * time.Millisecond
	artifact, _ := adapter.BuildScoped(ScopeSystem, "reset", testPlan())
	verifyCalls := 0
	verify := func(context.Context, Reference) error {
		verifyCalls++
		if verifyCalls == 2 {
			return errors.New("transient health failure")
		}
		return nil
	}
	started := time.Now()
	transition, err := StageScopeInstall(context.Background(), adapter, artifact, ModePersistent, verify)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < adapter.transitionStable+adapter.transitionPoll {
		t.Fatalf("transient failure did not reset stable window: elapsed=%s calls=%d", elapsed, verifyCalls)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchdTransitionAllowsConnectionBudgetBeforeStableWindow(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	adapter.transitionStable = 10 * time.Millisecond
	adapter.transitionPoll = time.Millisecond
	adapter.transitionTimeout = time.Second
	artifact, _ := adapter.BuildScoped(ScopeSystem, "connect-budget", testPlan())
	failedChecks := 0
	healthyChecks := 0
	verify := func(context.Context, Reference) error {
		if failedChecks < 4 {
			failedChecks++
			return errors.New("ssh connection is not ready yet")
		}
		healthyChecks++
		return nil
	}
	transition, err := StageScopeInstall(context.Background(), adapter, artifact, ModePersistent, verify)
	if err != nil {
		t.Fatalf("transition rejected a target that became healthy within the connection budget: %v", err)
	}
	if failedChecks != 4 || healthyChecks < 2 {
		t.Fatalf("readiness did not isolate connection and stability phases: failed=%d healthy=%d", failedChecks, healthyChecks)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchdTransitionDefaultsCoverConnectionAndStabilityBudgets(t *testing.T) {
	adapter := NewLaunchdWithSystemScope(
		filepath.Join(t.TempDir(), "agents"), filepath.Join(t.TempDir(), "daemons"), t.TempDir(),
		501, "alice", newLaunchdScopeRunner(t),
	)
	const sshConnectBudget = 10 * time.Second
	const schedulingMargin = 5 * time.Second
	required := sshConnectBudget + adapter.transitionStable + schedulingMargin
	if adapter.transitionTimeout < required {
		t.Fatalf("default transition timeout %s does not cover SSH connection %s + stability %s + margin %s",
			adapter.transitionTimeout, sshConnectBudget, adapter.transitionStable, schedulingMargin)
	}
}

func TestLaunchdStageReportsBlockedRecoveryWithoutRestartingSource(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	source, _ := adapter.BuildScoped(ScopeUser, "blocked", testPlan())
	if err := os.WriteFile(source.Path, source.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceTarget := "gui/501/" + source.UnitName
	runner.loaded[sourceTarget] = true
	runner.disabled[sourceTarget] = false
	target, _ := adapter.BuildScoped(ScopeSystem, "blocked", testPlan())
	runner.failBootstrap = true
	runner.failBootout = "system/" + target.UnitName
	_, err := StageScopeTransition(context.Background(), adapter, source.Reference(), target, ModePersistent, nil)
	var blocked *RecoveryBlockedError
	if !errors.As(err, &blocked) || blocked.Code() != "recovery_blocked" {
		t.Fatalf("stage error=%T %v", err, err)
	}
	if runner.loaded[sourceTarget] {
		t.Fatalf("source was restarted while target removal was unproven: loaded=%v", runner.loaded)
	}
}

func TestLaunchdInspectReportsOppositeScopeForForeignAndLoadedOnlyIdentity(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	userTarget, _ := adapter.TargetScoped(ScopeUser, "foreign")
	systemTarget := adapter.rawTarget(ScopeSystem, "foreign")
	foreignPlan := testPlan()
	foreignArtifact, err := adapter.build(systemTarget, foreignPlan)
	if err != nil {
		t.Fatal(err)
	}
	foreignArtifact.Content = []byte(strings.Replace(string(foreignArtifact.Content), "<string>alice</string>", "<string>bob</string>", 1))
	if err := os.WriteFile(foreignArtifact.Path, foreignArtifact.Content, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Inspect(context.Background(), userTarget.Reference())
	var mismatch *ScopeMismatchError
	if !errors.As(err, &mismatch) || mismatch.Actual != ScopeSystem {
		t.Fatalf("foreign opposite identity error=%T %v", err, err)
	}
	if err := os.Remove(foreignArtifact.Path); err != nil {
		t.Fatal(err)
	}
	runner.loaded["system/"+systemTarget.UnitName] = true
	_, err = adapter.Inspect(context.Background(), userTarget.Reference())
	if !errors.As(err, &mismatch) || mismatch.Actual != ScopeSystem {
		t.Fatalf("loaded-only opposite identity error=%T %v", err, err)
	}
}

func TestLaunchdUnavailableOppositeDomainStillChecksStaticIdentity(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	adapter.setDomainAvailability(true, false)
	userTarget, _ := adapter.TargetScoped(ScopeUser, "static")
	systemTarget := adapter.rawTarget(ScopeSystem, "static")
	if err := os.WriteFile(systemTarget.Path, []byte("foreign daemon"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.Inspect(context.Background(), userTarget.Reference())
	var mismatch *ScopeMismatchError
	if !errors.As(err, &mismatch) || mismatch.Actual != ScopeSystem {
		t.Fatalf("static opposite identity error=%T %v", err, err)
	}
}

func TestLaunchdSystemCapabilityRequiresSafeRuntimeUser(t *testing.T) {
	for _, identity := range []struct {
		uid  int
		name string
	}{{501, ""}, {0, "alice"}, {501, "root"}} {
		adapter := NewLaunchdWithSystemScope(t.TempDir(), t.TempDir(), t.TempDir(), identity.uid, identity.name, newLaunchdScopeRunner(t))
		if adapter.Capabilities().SystemScope {
			t.Fatalf("unsafe identity unexpectedly supports system scope: %+v", identity)
		}
	}
}

func TestLaunchdReadOnlyScopeCheckNeverInvokesSudo(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	target, _ := adapter.BuildScoped(ScopeSystem, "dry", testPlan())
	if err := CheckScopeInstall(context.Background(), adapter, target); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if call.command == "sudo" {
			t.Fatalf("read-only check invoked sudo: %#v", call)
		}
	}
}

func TestLaunchdReadOnlyPreflightFindsCustomFilenameWithSameLabel(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, daemons := newScopedLaunchd(t, runner)
	source, _ := adapter.BuildScoped(ScopeUser, "custom", testPlan())
	if err := os.WriteFile(source.Path, source.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	target, _ := adapter.BuildScoped(ScopeSystem, "custom", testPlan())
	foreign := []byte(strings.Replace(string(target.Content), "<string>alice</string>", "<string>bob</string>", 1))
	customPath := filepath.Join(daemons, "operator-chosen-name.plist")
	if err := os.WriteFile(customPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckScopeTransition(context.Background(), adapter, source.Reference(), target)
	var conflict *ScopeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("custom-filename static identity error=%T %v", err, err)
	}
	for _, call := range runner.calls {
		if call.command == "sudo" {
			t.Fatalf("read-only custom identity check invoked sudo: %#v", call)
		}
	}
}
