//go:build !windows

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	definitions   map[string][]byte
	disabled      map[string]bool
	stageCounter  int
	failKickstart bool
	failBootstrap bool
	failBootout   string
}

func newLaunchdScopeRunner(t *testing.T) *launchdScopeRunner {
	t.Helper()
	runner := &launchdScopeRunner{loaded: map[string]bool{}, definitions: map[string][]byte{}, disabled: map[string]bool{}}
	runner.fakeRunner.run = runner.execute
	return runner
}

func (r *launchdScopeRunner) execute(command string, args []string) ([]byte, error) {
	if filepath.Base(command) == "sudo" && command != MacOSSudoProgram {
		return nil, fmt.Errorf("sudo must use trusted absolute path, got %q", command)
	}
	if command == MacOSSudoProgram {
		if len(args) < 3 || args[0] != "-n" || args[1] != "--" {
			return nil, errors.New("unsafe sudo invocation")
		}
		switch args[2] {
		case "/usr/bin/true":
			return nil, nil
		case mktempProgram:
			r.stageCounter++
			template := args[3]
			staged := strings.TrimSuffix(template, "XXXXXXXX") + fmt.Sprintf("%08d", r.stageCounter)
			file, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
			return []byte(staged + "\n"), nil
		case ddProgram:
			var destination string
			for _, argument := range args[3:] {
				if strings.HasPrefix(argument, "of=") {
					destination = strings.TrimPrefix(argument, "of=")
				}
			}
			if destination == "" {
				return nil, errors.New("dd output is missing")
			}
			return nil, os.WriteFile(destination, append([]byte(nil), r.fakeRunner.input...), 0o600)
		case "/usr/bin/install":
			source, destination := args[len(args)-2], args[len(args)-1]
			if filepath.Dir(source) != filepath.Dir(destination) || source == destination {
				return nil, fmt.Errorf("system install source is not a separate daemon-directory stage: %s", source)
			}
			raw, err := os.ReadFile(source)
			if err != nil {
				return nil, err
			}
			err = os.WriteFile(destination, raw, 0o644)
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
	if command == launchctlProgram {
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
		target := args[1] + "/" + label
		if r.loaded[target] {
			return []byte("service already loaded"), errors.New("already loaded")
		}
		definition, err := os.ReadFile(args[2])
		if err != nil {
			return nil, err
		}
		r.definitions[target] = definition
		r.loaded[target] = true
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
		delete(r.definitions, args[1])
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

type systemRestartRunner struct {
	fakeRunner
	target         string
	path           string
	program        string
	uid            int
	pid            int
	replacementPID int
	printCalls     int
	changePIDAt    int
	killCalls      int
	privileged     int
}

func newSystemRestartRunner() *systemRestartRunner {
	runner := &systemRestartRunner{program: "/usr/bin/provider", uid: 501, pid: 42, replacementPID: 84}
	runner.fakeRunner.run = runner.execute
	return runner
}

func (r *systemRestartRunner) execute(command string, args []string) ([]byte, error) {
	if command == MacOSSudoProgram {
		r.privileged++
		return nil, errors.New("system restart must not invoke sudo")
	}
	if command == launchctlProgram {
		switch args[0] {
		case "print-disabled":
			return []byte(fmt.Sprintf("\"%s\" => false\n", strings.TrimPrefix(r.target, "system/"))), nil
		case "print":
			r.printCalls++
			if r.changePIDAt > 0 && r.printCalls >= r.changePIDAt {
				r.pid = r.replacementPID
			}
			return []byte(fmt.Sprintf("path = %s\nstate = running\nprogram = %s\npid = %d\n", r.path, r.program, r.pid)), nil
		default:
			return nil, fmt.Errorf("forbidden launchctl mutation %q", args[0])
		}
	}
	if command == psProgram {
		return []byte(fmt.Sprintf("%d %s\n", r.uid, r.program)), nil
	}
	if command == killProgram {
		r.killCalls++
		r.pid = r.replacementPID
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s", command)
}

func prepareSystemRestart(t *testing.T, mutate func(*systemRestartRunner, *Artifact, *Reference)) (*Launchd, *systemRestartRunner, Artifact, Reference) {
	t.Helper()
	runner := newSystemRestartRunner()
	adapter, _, _ := newScopedLaunchd(t, runner)
	artifact, err := adapter.BuildScoped(ScopeSystem, "db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact.Path, artifact.Content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setLaunchdDemand(adapter.logDir, artifact, true); err != nil {
		t.Fatal(err)
	}
	reference := artifact.Reference()
	runner.target = adapter.domainTarget(ScopeSystem, artifact.UnitName)
	runner.path = artifact.Path
	if mutate != nil {
		mutate(runner, &artifact, &reference)
	}
	return adapter, runner, artifact, reference
}

func TestLaunchdSystemRestartUsesVerifiedNonPrivilegedKeepAliveReplacement(t *testing.T) {
	adapter, runner, _, reference := prepareSystemRestart(t, nil)
	if err := adapter.Restart(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	if runner.killCalls != 1 || runner.pid != runner.replacementPID || runner.privileged != 0 {
		t.Fatalf("kill=%d pid=%d privileged=%d calls=%#v", runner.killCalls, runner.pid, runner.privileged, runner.calls)
	}
	for _, call := range runner.calls {
		if call.command == launchctlProgram && len(call.args) > 0 && slices.Contains([]string{"bootout", "bootstrap", "kickstart"}, call.args[0]) {
			t.Fatalf("system restart mutated launchd domain: %#v", call)
		}
	}
}

func TestLaunchdSystemRestartPreflightFailuresAreZeroChange(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*systemRestartRunner, *Artifact, *Reference)
	}{
		{name: "definition baseline drift", mutate: func(_ *systemRestartRunner, _ *Artifact, reference *Reference) {
			reference.BaselineDigest = "different"
		}},
		{name: "native path mismatch", mutate: func(runner *systemRestartRunner, _ *Artifact, _ *Reference) {
			runner.path += ".foreign"
		}},
		{name: "provider mismatch", mutate: func(runner *systemRestartRunner, _ *Artifact, _ *Reference) {
			runner.program = "/usr/bin/foreign"
		}},
		{name: "root provider", mutate: func(runner *systemRestartRunner, _ *Artifact, _ *Reference) {
			runner.uid = 0
		}},
		{name: "pid changed before signal", mutate: func(runner *systemRestartRunner, _ *Artifact, _ *Reference) {
			runner.changePIDAt = 2
		}},
		{name: "keepalive definition drift", mutate: func(_ *systemRestartRunner, artifact *Artifact, reference *Reference) {
			changed := bytes.Replace(artifact.Content, []byte("<key>KeepAlive</key>"), []byte("<key>KeepAliveChanged</key>"), 1)
			if err := os.WriteFile(artifact.Path, changed, 0o644); err != nil {
				panic(err)
			}
			reference.BaselineDigest = ContentDigest(changed)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, runner, _, reference := prepareSystemRestart(t, test.mutate)
			if err := adapter.Restart(context.Background(), reference); err == nil {
				t.Fatal("unsafe system restart unexpectedly succeeded")
			}
			if runner.killCalls != 0 || runner.privileged != 0 {
				t.Fatalf("preflight failure changed process: kill=%d privileged=%d calls=%#v", runner.killCalls, runner.privileged, runner.calls)
			}
		})
	}
}

func TestLaunchdSystemDisableDeletesArtifactAndCanBeEnabledAgain(t *testing.T) {
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
	if err := adapter.Disable(context.Background(), artifact.Reference()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("system disable retained root artifact: %v", err)
	}
	if runner.loaded[adapter.domainTarget(ScopeSystem, artifact.UnitName)] {
		t.Fatal("system disable retained loaded job")
	}
	reenabled, err := StageScopeInstall(context.Background(), adapter, artifact, ModePersistent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reenabled.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact.Path); err != nil {
		t.Fatalf("system enable did not recreate artifact: %v", err)
	}
}

func TestLaunchdSystemDefinitionReplacementCommitsOrRestoresAtomically(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		runner := newLaunchdScopeRunner(t)
		adapter, _, _ := newScopedLaunchd(t, runner)
		original, _ := adapter.BuildScoped(ScopeSystem, "db", testPlan())
		installed, err := StageScopeInstall(context.Background(), adapter, original, ModePersistent, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := installed.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		changedPlan := testPlan()
		changedPlan.Exec.Args = append(changedPlan.Exec.Args, "--changed")
		changed, _ := adapter.BuildScoped(ScopeSystem, "db", changedPlan)
		replacement, err := StageDefinitionReplace(context.Background(), adapter, original.Reference(), changed, ModePersistent, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := replacement.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(changed.Path)
		if err != nil || !bytes.Equal(raw, changed.Content) {
			t.Fatalf("replacement not committed: err=%v", err)
		}
	})

	t.Run("bootstrap failure restores old definition and mode", func(t *testing.T) {
		runner := newLaunchdScopeRunner(t)
		adapter, _, _ := newScopedLaunchd(t, runner)
		original, _ := adapter.BuildScoped(ScopeSystem, "db", testPlan())
		installed, err := StageScopeInstall(context.Background(), adapter, original, ModePersistent, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := installed.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		changedPlan := testPlan()
		changedPlan.Exec.Args = append(changedPlan.Exec.Args, "--changed")
		changed, _ := adapter.BuildScoped(ScopeSystem, "db", changedPlan)
		runner.failBootstrap = true
		if _, err := StageDefinitionReplace(context.Background(), adapter, original.Reference(), changed, ModePersistent, nil); err == nil {
			t.Fatal("replacement unexpectedly survived bootstrap failure")
		}
		raw, err := os.ReadFile(original.Path)
		if err != nil || !bytes.Equal(raw, original.Content) {
			t.Fatalf("old definition not restored: err=%v", err)
		}
		observation, err := adapter.Inspect(context.Background(), original.Reference())
		if err != nil || !observation.AutoStart || observation.ProcessState != ProcessRunning {
			t.Fatalf("old persistent mode not restored: observation=%#v err=%v", observation, err)
		}
	})
}

func TestLaunchdDefinitionRollbackRestoresEffectiveArgumentsAfterBootstrap(t *testing.T) {
	for _, failure := range []string{"health", "record or config commit", "unload", "restore bootstrap"} {
		t.Run(failure, func(t *testing.T) {
			runner := newLaunchdScopeRunner(t)
			adapter, _, _ := newScopedLaunchd(t, runner)
			original, _ := adapter.BuildScoped(ScopeSystem, "db", testPlan())
			installed, err := StageScopeInstall(context.Background(), adapter, original, ModePersistent, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := installed.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			changedPlan := testPlan()
			changedPlan.Exec.Args = append(changedPlan.Exec.Args, "--changed")
			changed, _ := adapter.BuildScoped(ScopeSystem, "db", changedPlan)
			var verify func(context.Context, Reference) error
			if failure == "health" {
				verify = func(context.Context, Reference) error { return errors.New("unhealthy replacement") }
			}
			transaction, err := StageDefinitionReplace(context.Background(), adapter, original.Reference(), changed, ModePersistent, verify)
			target := "system/" + original.UnitName
			if failure == "health" {
				if err == nil {
					t.Fatal("unhealthy replacement accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if failure == "unload" {
					runner.failBootout = target
				} else if failure == "restore bootstrap" {
					runner.failBootstrap = true
				}
				err = transaction.Rollback(context.Background())
				if failure != "record or config commit" {
					if err == nil {
						t.Fatal("failed recovery reported success")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			// launchd retains its loaded definition, even if the plist changes.
			// Check the definition used by its provider, not just the disk file.
			dictionary, err := decodeLaunchdDictionary(runner.definitions[target])
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprint(dictionary["ProgramArguments"]); strings.Contains(got, "--changed") || !runner.loaded[target] {
				t.Fatalf("rollback left replacement provider arguments active: %s loaded=%v", got, runner.loaded[target])
			}
			if !bytes.Equal(runner.definitions[target], original.Content) || runner.disabled[target] {
				t.Fatal("original provider definition and persistence were not restored")
			}
		})
	}
}

func TestLaunchdRejectsForgedSystemRecordIdentityBeforeSudoOrProcessMutation(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	artifact, err := adapter.BuildScoped(ScopeSystem, "db", testPlan())
	if err != nil {
		t.Fatal(err)
	}
	installed, err := StageScopeInstall(context.Background(), adapter, artifact, ModePersistent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := installed.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	privilegedBefore := 0
	for _, call := range runner.calls {
		if call.command == MacOSSudoProgram {
			privilegedBefore++
		}
	}
	forged := artifact.Reference()
	forged.ArtifactPath = filepath.Join(filepath.Dir(artifact.Path), "com.example.victim.plist")
	forged.UnitName = "com.example.victim"
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "build-bound-set-or-resolve", run: func() error {
			_, err := adapter.BuildBound(forged, testPlan())
			return err
		}},
		{name: "disable", run: func() error { return adapter.Disable(context.Background(), forged) }},
		{name: "remove", run: func() error { return adapter.Remove(context.Background(), forged) }},
		{name: "restart", run: func() error { return adapter.Restart(context.Background(), forged) }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			callsBefore := len(runner.calls)
			if err := operation.run(); err == nil {
				t.Fatal("forged system identity unexpectedly accepted")
			}
			for _, call := range runner.calls[callsBefore:] {
				if call.command == MacOSSudoProgram || call.command == killProgram ||
					(call.command == launchctlProgram && len(call.args) > 0 && slices.Contains([]string{"disable", "enable", "bootout", "bootstrap", "kickstart"}, call.args[0])) {
					t.Fatalf("forged identity caused mutation: %#v", call)
				}
			}
		})
	}
	privilegedAfter := 0
	for _, call := range runner.calls {
		if call.command == MacOSSudoProgram {
			privilegedAfter++
		}
	}
	if privilegedAfter != privilegedBefore {
		t.Fatalf("forged identity reached sudo: before=%d after=%d", privilegedBefore, privilegedAfter)
	}
}

func TestLaunchdSystemInstallUsesRootDaemonDirectoryStage(t *testing.T) {
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
	if err != nil || !bytes.Equal(raw, artifact.Content) {
		t.Fatalf("stdin install changed definition bytes: err=%v", err)
	}
	var staged string
	sequence := make([]string, 0, 5)
	for _, call := range runner.calls {
		if call.command == syncProgram {
			sequence = append(sequence, "sync")
			if len(call.args) != 0 {
				t.Fatalf("sync received unexpected arguments: %#v", call.args)
			}
			continue
		}
		if call.command != MacOSSudoProgram || len(call.args) < 3 {
			continue
		}
		switch call.args[2] {
		case mktempProgram:
			sequence = append(sequence, "mktemp")
			if len(call.args) != 4 || filepath.Dir(call.args[3]) != filepath.Dir(artifact.Path) {
				t.Fatalf("unsafe mktemp arguments: %#v", call.args)
			}
		case ddProgram:
			sequence = append(sequence, "dd")
			for _, argument := range call.args[3:] {
				if strings.HasPrefix(argument, "of=") {
					staged = strings.TrimPrefix(argument, "of=")
				}
			}
			if staged == "" || filepath.Dir(staged) != filepath.Dir(artifact.Path) || !slices.Contains(call.args, "conv=fsync") {
				t.Fatalf("unsafe staged write arguments: %#v", call.args)
			}
		case installProgram:
			sequence = append(sequence, "install")
			if len(call.args) < 2 || call.args[len(call.args)-2] != staged || call.args[len(call.args)-1] != artifact.Path {
				t.Fatalf("unsafe system install arguments: %#v", call.args)
			}
		case removeProgram:
			if call.args[len(call.args)-1] == staged {
				sequence = append(sequence, "cleanup")
			}
		}
	}
	if !slices.Equal(sequence, []string{"mktemp", "dd", "install", "sync", "cleanup"}) {
		t.Fatalf("privileged stage sequence=%v calls=%#v", sequence, runner.calls)
	}
	if _, err := os.Lstat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root staging file remains after success: %v", err)
	}
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

func TestLaunchdSystemInstallUsesOnlyNonInteractiveAbsoluteCommandsAndRollsBack(t *testing.T) {
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
	privilegedPrograms := make([]string, 0, 4)
	for _, call := range runner.calls {
		if call.command == MacOSSudoProgram && slices.Contains(call.args, installProgram) {
			installArgs = call.args
		}
		if call.command == MacOSSudoProgram && (len(call.args) < 2 || call.args[0] != "-n" || call.args[1] != "--") {
			t.Fatalf("interactive sudo call: %#v", call)
		}
		if call.command == MacOSSudoProgram && (len(call.args) < 3 || !filepath.IsAbs(call.args[2])) {
			t.Fatalf("sudo inner command was not absolute: %#v", call)
		}
		if call.command == MacOSSudoProgram {
			privilegedPrograms = append(privilegedPrograms, call.args[2])
			if slices.Contains(call.args, "/dev/stdin") {
				t.Fatalf("system install reused unsupported /dev/stdin source: %#v", call.args)
			}
		}
	}
	joined := strings.Join(installArgs, " ")
	for _, expected := range []string{"/usr/bin/install -S", "-o root", "-g wheel", "-m 0644"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("install command missing %q: %v", expected, installArgs)
		}
	}
	for _, expected := range []string{mktempProgram, ddProgram, installProgram, removeProgram} {
		if !slices.Contains(privilegedPrograms, expected) {
			t.Fatalf("privileged transaction omitted %s: %v", expected, privilegedPrograms)
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

func TestLaunchdSystemInstallRejectsUnsafeStageOutputBeforeWriting(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	baseExecute := runner.execute
	runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
		if command == MacOSSudoProgram && len(args) >= 3 && args[2] == mktempProgram {
			return []byte("/tmp/user-controlled-stage\n"), nil
		}
		return baseExecute(command, args)
	}
	adapter, _, _ := newScopedLaunchd(t, runner)
	artifact, _ := adapter.BuildScoped(ScopeSystem, "unsafe-stage", testPlan())
	if _, _, err := adapter.install(context.Background(), artifact); err == nil || !strings.Contains(err.Error(), "unsafe privileged launchd staging path") {
		t.Fatalf("unsafe mktemp output error=%v", err)
	}
	for _, call := range runner.calls {
		if call.command == MacOSSudoProgram && len(call.args) >= 3 && slices.Contains([]string{ddProgram, installProgram}, call.args[2]) {
			t.Fatalf("unsafe stage output reached a privileged write: %#v", call)
		}
	}
}

func TestLaunchdSystemInstallCleansRootStageAfterWriteOrInstallFailure(t *testing.T) {
	for _, failedProgram := range []string{ddProgram, installProgram} {
		t.Run(filepath.Base(failedProgram), func(t *testing.T) {
			runner := newLaunchdScopeRunner(t)
			baseExecute := runner.execute
			runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
				if command == MacOSSudoProgram && len(args) >= 3 && args[2] == failedProgram {
					return nil, errors.New("injected privileged stage failure")
				}
				return baseExecute(command, args)
			}
			adapter, _, daemons := newScopedLaunchd(t, runner)
			artifact, _ := adapter.BuildScoped(ScopeSystem, "stage-failure", testPlan())
			if _, _, err := adapter.install(context.Background(), artifact); err == nil {
				t.Fatal("injected system install failure unexpectedly succeeded")
			}
			if _, err := os.Lstat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed transaction left destination: %v", err)
			}
			entries, err := os.ReadDir(daemons)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), ".tunlite.") {
					t.Fatalf("failed transaction left staging file %q", entry.Name())
				}
			}
		})
	}
}

func TestLaunchdSystemStageCleanupFailureRollsBackDestinationAndReportsStage(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	baseExecute := runner.execute
	runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
		if command == MacOSSudoProgram && len(args) >= 3 && args[2] == removeProgram &&
			strings.HasPrefix(filepath.Base(args[len(args)-1]), ".") {
			return nil, errors.New("injected stage cleanup failure")
		}
		return baseExecute(command, args)
	}
	adapter, _, daemons := newScopedLaunchd(t, runner)
	artifact, _ := adapter.BuildScoped(ScopeSystem, "cleanup-failure", testPlan())
	if _, err := StageScopeInstall(context.Background(), adapter, artifact, ModePersistent, nil); err == nil {
		t.Fatal("stage cleanup failure unexpectedly succeeded")
	} else if !strings.Contains(err.Error(), filepath.Base(artifact.Path)+".tunlite.") {
		t.Fatalf("stage cleanup error omitted the exact recovery path: %v", err)
	}
	if _, err := os.Lstat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage cleanup failure retained the loadable destination: %v", err)
	}
	entries, err := os.ReadDir(daemons)
	if err != nil {
		t.Fatal(err)
	}
	stages := 0
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tunlite.") {
			stages++
		}
	}
	if stages != 1 {
		t.Fatalf("cleanup failure retained %d stage files, want one diagnosed root-owned stage", stages)
	}
}

func TestLaunchdMaterializeCompensatesPostRenameFailures(t *testing.T) {
	for _, failure := range []string{"sync", "stage-cleanup"} {
		for _, existed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existed=%t", failure, existed), func(t *testing.T) {
				runner := newLaunchdScopeRunner(t)
				baseExecute := runner.execute
				injected := false
				runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
					if failure == "sync" && command == syncProgram && !injected {
						injected = true
						return nil, errors.New("injected post-rename sync failure")
					}
					if failure == "stage-cleanup" && command == MacOSSudoProgram && len(args) >= 3 &&
						args[2] == removeProgram && strings.HasPrefix(filepath.Base(args[len(args)-1]), ".") && !injected {
						injected = true
						return nil, errors.New("injected post-rename stage cleanup failure")
					}
					return baseExecute(command, args)
				}
				adapter, _, daemons := newScopedLaunchd(t, runner)
				oldArtifact, _ := adapter.BuildScoped(ScopeSystem, "materialize-compensation", testPlan())
				newPlan := testPlan()
				newPlan.Exec.Args = append(newPlan.Exec.Args, "--replacement")
				artifact, _ := adapter.BuildScoped(ScopeSystem, "materialize-compensation", newPlan)
				if existed {
					if err := os.WriteFile(artifact.Path, oldArtifact.Content, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := adapter.Materialize(context.Background(), artifact); err == nil {
					t.Fatal("post-rename failure unexpectedly succeeded")
				}
				current, err := os.ReadFile(artifact.Path)
				if existed {
					if err != nil || !bytes.Equal(current, oldArtifact.Content) {
						t.Fatalf("previous definition was not restored: err=%v", err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("prior-absent definition was not removed: err=%v", err)
				}
				entries, err := os.ReadDir(daemons)
				if err != nil {
					t.Fatal(err)
				}
				stages := 0
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".") {
						stages++
					}
				}
				wantStages := 0
				if failure == "stage-cleanup" {
					wantStages = 1
				}
				if stages != wantStages {
					t.Fatalf("staging residues=%d want=%d", stages, wantStages)
				}
			})
		}
	}
}

func TestLaunchdMaterializeRefusesToOverwriteThirdStateDuringRecovery(t *testing.T) {
	for _, existed := range []bool{false, true} {
		t.Run(fmt.Sprintf("existed=%t", existed), func(t *testing.T) {
			runner := newLaunchdScopeRunner(t)
			baseExecute := runner.execute
			var destination string
			corrupted := []byte("root-authored third state\n")
			injected := false
			runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
				output, err := baseExecute(command, args)
				if command == MacOSSudoProgram && len(args) >= 3 && args[2] == installProgram {
					destination = args[len(args)-1]
				}
				if command == syncProgram && !injected {
					injected = true
					if writeErr := os.WriteFile(destination, corrupted, 0o644); writeErr != nil {
						return nil, writeErr
					}
				}
				return output, err
			}
			adapter, _, _ := newScopedLaunchd(t, runner)
			oldArtifact, _ := adapter.BuildScoped(ScopeSystem, "materialize-third-state", testPlan())
			newPlan := testPlan()
			newPlan.Exec.Args = append(newPlan.Exec.Args, "--replacement")
			artifact, _ := adapter.BuildScoped(ScopeSystem, "materialize-third-state", newPlan)
			if existed {
				if err := os.WriteFile(artifact.Path, oldArtifact.Content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := adapter.Materialize(context.Background(), artifact)
			var blocked *RecoveryBlockedError
			if !errors.As(err, &blocked) {
				t.Fatalf("third state did not block recovery: %T %v", err, err)
			}
			current, readErr := os.ReadFile(artifact.Path)
			if readErr != nil || !bytes.Equal(current, corrupted) {
				t.Fatalf("third state was overwritten: content=%q err=%v", current, readErr)
			}
		})
	}
}

func TestLaunchdCanceledInstallCleansStageBeforeAnyPrivilegedWrite(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, daemons := newScopedLaunchd(t, runner)
	artifact, _ := adapter.BuildScoped(ScopeSystem, "canceled-stage", testPlan())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := adapter.install(ctx, artifact); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled install error=%v", err)
	}
	for _, call := range runner.calls {
		if call.command == MacOSSudoProgram && len(call.args) >= 3 && slices.Contains([]string{ddProgram, installProgram}, call.args[2]) {
			t.Fatalf("canceled install reached privileged write: %#v", call)
		}
	}
	entries, err := os.ReadDir(daemons)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled install retained daemon-directory entries: %v", entries)
	}
}

func TestLaunchdMktempOutputWithErrorStillCleansValidatedStage(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	baseExecute := runner.execute
	runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
		output, err := baseExecute(command, args)
		if command == MacOSSudoProgram && len(args) >= 3 && args[2] == mktempProgram && err == nil {
			return output, errors.New("injected mktemp post-create failure")
		}
		return output, err
	}
	adapter, _, daemons := newScopedLaunchd(t, runner)
	artifact, _ := adapter.BuildScoped(ScopeSystem, "mktemp-error", testPlan())
	if _, _, err := adapter.install(context.Background(), artifact); err == nil {
		t.Fatal("mktemp output+error unexpectedly succeeded")
	}
	entries, err := os.ReadDir(daemons)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("mktemp output+error retained daemon-directory entries: %v", entries)
	}
}

func TestLaunchdMktempIgnoresStderrWarningWhenStdoutPathIsValid(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	baseExecute := runner.execute
	runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
		output, err := baseExecute(command, args)
		if command == MacOSSudoProgram && len(args) >= 3 && args[2] == mktempProgram && err == nil {
			return append(append([]byte(nil), output...), []byte("mktemp warning on stderr\n")...), nil
		}
		return output, err
	}
	runner.fakeRunner.streamOutput = func(command string, args []string, output []byte) []byte {
		if command == MacOSSudoProgram && len(args) >= 3 && args[2] == mktempProgram {
			if newline := bytes.IndexByte(output, '\n'); newline >= 0 {
				return output[:newline+1]
			}
		}
		return output
	}
	adapter, _, _ := newScopedLaunchd(t, runner)
	artifact, _ := adapter.BuildScoped(ScopeSystem, "mktemp-warning", testPlan())
	if _, _, err := adapter.install(context.Background(), artifact); err != nil {
		t.Fatalf("stderr warning polluted validated mktemp stdout: %v", err)
	}
	installed, err := os.ReadFile(artifact.Path)
	if err != nil || !bytes.Equal(installed, artifact.Content) {
		t.Fatalf("installed bytes after warning: err=%v", err)
	}
}

func TestLaunchdCancellationWaitsForRootMutationThenCompensates(t *testing.T) {
	for _, delayedProgram := range []string{ddProgram, installProgram} {
		for _, existed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existed=%t", filepath.Base(delayedProgram), existed), func(t *testing.T) {
				runner := newLaunchdScopeRunner(t)
				baseExecute := runner.execute
				started := make(chan struct{})
				release := make(chan struct{})
				triggered := false
				runner.fakeRunner.runContext = func(commandCtx context.Context, command string, args []string) ([]byte, error) {
					if command == MacOSSudoProgram && len(args) >= 3 && args[2] == delayedProgram && !triggered {
						triggered = true
						close(started)
						select {
						case <-release:
							return baseExecute(command, args)
						case <-commandCtx.Done():
							go func() {
								<-release
								_, _ = baseExecute(command, args)
							}()
							return nil, commandCtx.Err()
						}
					}
					return baseExecute(command, args)
				}
				adapter, _, daemons := newScopedLaunchd(t, runner)
				oldArtifact, _ := adapter.BuildScoped(ScopeSystem, "cancel-root-mutation", testPlan())
				newPlan := testPlan()
				newPlan.Exec.Args = append(newPlan.Exec.Args, "--replacement")
				artifact, _ := adapter.BuildScoped(ScopeSystem, "cancel-root-mutation", newPlan)
				if existed {
					if err := os.WriteFile(artifact.Path, oldArtifact.Content, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				result := make(chan error, 1)
				go func() { result <- adapter.Materialize(ctx, artifact) }()
				<-started
				cancel()
				select {
				case err := <-result:
					t.Fatalf("Materialize returned before root mutation ended: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				close(release)
				err := <-result
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled Materialize error=%v", err)
				}
				assertState := func() {
					current, readErr := os.ReadFile(artifact.Path)
					if existed {
						if readErr != nil || !bytes.Equal(current, oldArtifact.Content) {
							t.Fatalf("previous definition not restored: err=%v", readErr)
						}
					} else if !errors.Is(readErr, os.ErrNotExist) {
						t.Fatalf("prior-absent definition remains: err=%v", readErr)
					}
				}
				assertState()
				time.Sleep(20 * time.Millisecond)
				assertState()
				entries, err := os.ReadDir(daemons)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".") {
						t.Fatalf("canceled mutation retained stage %q", entry.Name())
					}
				}
			})
		}
	}
}

func TestLaunchdSystemInstallRejectsWritableDaemonDirectoryBeforeSudo(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, daemons := newScopedLaunchd(t, runner)
	if err := os.Chmod(daemons, 0o777); err != nil {
		t.Fatal(err)
	}
	artifact, _ := adapter.BuildScoped(ScopeSystem, "writable-directory", testPlan())
	if _, _, err := adapter.install(context.Background(), artifact); err == nil || !strings.Contains(err.Error(), "group/world writable") {
		t.Fatalf("writable daemon directory error=%v", err)
	}
	for _, call := range runner.calls {
		if call.command == MacOSSudoProgram {
			t.Fatalf("writable daemon directory reached sudo: %#v", call)
		}
	}
}

func TestLaunchdSystemInstallRejectsLinkedOrReplacedStageBeforeInstall(t *testing.T) {
	for _, mutation := range []string{"hardlink", "replace-after-write"} {
		t.Run(mutation, func(t *testing.T) {
			runner := newLaunchdScopeRunner(t)
			baseExecute := runner.execute
			mutationSetupComplete := false
			var mutationSetupErr error
			runner.fakeRunner.run = func(command string, args []string) ([]byte, error) {
				output, err := baseExecute(command, args)
				if err != nil {
					return output, err
				}
				if mutation == "hardlink" && command == MacOSSudoProgram && len(args) >= 3 && args[2] == mktempProgram {
					stage := strings.TrimSuffix(string(output), "\n")
					linked := stage + ".link"
					t.Cleanup(func() {
						if cleanupErr := os.Remove(linked); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
							t.Errorf("clean up injected hardlink %q: %v", linked, cleanupErr)
						}
					})
					if mutationSetupErr = os.Link(stage, linked); mutationSetupErr != nil {
						mutationSetupErr = fmt.Errorf("create injected hardlink: %w", mutationSetupErr)
						return nil, mutationSetupErr
					}
					mutationSetupComplete = true
				}
				if mutation == "replace-after-write" && command == MacOSSudoProgram && len(args) >= 3 && args[2] == ddProgram {
					var stage string
					for _, argument := range args[3:] {
						if strings.HasPrefix(argument, "of=") {
							stage = strings.TrimPrefix(argument, "of=")
						}
					}
					content, readErr := os.ReadFile(stage)
					if readErr != nil {
						mutationSetupErr = fmt.Errorf("read stage before injected replacement: %w", readErr)
						return nil, mutationSetupErr
					}
					displaced := stage + ".replaced"
					t.Cleanup(func() {
						if cleanupErr := os.Remove(displaced); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
							t.Errorf("clean up displaced stage %q: %v", displaced, cleanupErr)
						}
					})
					if mutationSetupErr = os.Rename(stage, displaced); mutationSetupErr != nil {
						mutationSetupErr = fmt.Errorf("displace stage before injected replacement: %w", mutationSetupErr)
						return nil, mutationSetupErr
					}
					if mutationSetupErr = os.WriteFile(stage, content, 0o600); mutationSetupErr != nil {
						mutationSetupErr = fmt.Errorf("create injected replacement stage: %w", mutationSetupErr)
						return nil, mutationSetupErr
					}
					mutationSetupComplete = true
				}
				return output, nil
			}
			adapter, _, _ := newScopedLaunchd(t, runner)
			artifact, _ := adapter.BuildScoped(ScopeSystem, "unsafe-stage-identity", testPlan())
			_, _, installErr := adapter.install(context.Background(), artifact)
			if mutationSetupErr != nil {
				t.Fatalf("%s mutation fixture failed: %v", mutation, mutationSetupErr)
			}
			if !mutationSetupComplete {
				t.Fatalf("%s mutation fixture did not run", mutation)
			}
			if installErr == nil {
				t.Fatalf("%s stage mutation unexpectedly succeeded", mutation)
			}
			switch mutation {
			case "hardlink":
				if !strings.Contains(installErr.Error(), "unsafe privileged launchd staging file") ||
					!strings.Contains(installErr.Error(), "identity, link count, or filesystem is unsafe") {
					t.Fatalf("hardlink rejection error=%v", installErr)
				}
			case "replace-after-write":
				if !strings.Contains(installErr.Error(), "staged system launchd definition changed during write") ||
					!strings.Contains(installErr.Error(), "inode changed during the privileged write") {
					t.Fatalf("replacement rejection error=%v", installErr)
				}
			}
			for _, call := range runner.calls {
				if call.command == MacOSSudoProgram && len(call.args) >= 3 && call.args[2] == installProgram {
					t.Fatalf("%s stage mutation reached install: %#v", mutation, call)
				}
			}
		})
	}
}

func TestLaunchdSystemInstallRefusesExistingSymlinkBeforeSudo(t *testing.T) {
	runner := newLaunchdScopeRunner(t)
	adapter, _, _ := newScopedLaunchd(t, runner)
	artifact, _ := adapter.BuildScoped(ScopeSystem, "symlink", testPlan())
	target := filepath.Join(t.TempDir(), "target.plist")
	if err := os.WriteFile(target, artifact.Content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, artifact.Path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.install(context.Background(), artifact); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("symlink install error=%v", err)
	}
	for _, call := range runner.calls {
		if call.command == MacOSSudoProgram {
			t.Fatalf("untrusted existing definition reached sudo: %#v", call)
		}
	}
}

func TestValidateLaunchdStagingPathRejectsUntrustedOutput(t *testing.T) {
	destination := "/Library/LaunchDaemons/dev.tunlite.connection.db.plist"
	valid := "/Library/LaunchDaemons/.dev.tunlite.connection.db.plist.tunlite.Ab12Cd34\n"
	if got, err := validateLaunchdStagingPath(destination, []byte(valid)); err != nil || got != strings.TrimSuffix(valid, "\n") {
		t.Fatalf("valid staging path=%q err=%v", got, err)
	}
	for _, output := range []string{
		"/tmp/.dev.tunlite.connection.db.plist.tunlite.Ab12Cd34\n",
		"/Library/LaunchDaemons/../LaunchDaemons/.dev.tunlite.connection.db.plist.tunlite.Ab12Cd34\n",
		"/Library/LaunchDaemons/.other.tunlite.Ab12Cd34\n",
		"/Library/LaunchDaemons/.dev.tunlite.connection.db.plist.tunlite.short\n",
		valid + "extra\n",
	} {
		if got, err := validateLaunchdStagingPath(destination, []byte(output)); err == nil {
			t.Fatalf("unsafe staging output accepted as %q: %q", got, output)
		}
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
		if command == MacOSSudoProgram {
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
		if runner.calls[index].command == MacOSSudoProgram {
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
		if call.command == MacOSSudoProgram {
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
		if call.command == MacOSSudoProgram {
			t.Fatalf("read-only custom identity check invoked sudo: %#v", call)
		}
	}
}
