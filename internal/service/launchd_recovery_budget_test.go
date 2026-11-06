//go:build !windows

package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestLaunchdDefinitionRecoveryCoversProductionStabilityBudget(t *testing.T) {
	for _, test := range []struct {
		name        string
		restoreTime time.Duration
		startupTime time.Duration
		stall       bool
	}{
		{name: "immediately healthy"},
		{name: "native restoration and slow startup", restoreTime: 8 * time.Second, startupTime: 18 * time.Second},
		{name: "stalled native restoration remains bounded", stall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runner := newLaunchdScopeRunner(t)
				root := t.TempDir()
				// Use the production constructor, not newScopedLaunchd's 2 ms
				// stability window. Virtual time keeps the full 11 s check fast.
				adapter := NewLaunchdWithSystemScope(filepath.Join(root, "agents"), filepath.Join(root, "daemons"),
					filepath.Join(root, "logs"), 501, "alice", runner)
				for _, directory := range []string{adapter.agentDir, adapter.daemonDir} {
					if err := os.MkdirAll(directory, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				original, err := adapter.BuildScoped(ScopeSystem, "db", testPlan())
				if err != nil {
					t.Fatal(err)
				}
				installed, err := StageScopeInstall(context.Background(), adapter, original, ModePersistent, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := installed.Commit(context.Background()); err != nil {
					t.Fatal(err)
				}
				changedPlan := testPlan()
				changedPlan.Exec.Args = append(changedPlan.Exec.Args, "--changed")
				changed, err := adapter.BuildScoped(ScopeSystem, "db", changedPlan)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cause := errors.New("replacement bootstrap failed")
				var recoveryStarted, restoredAt time.Time
				runner.fakeRunner.runContext = func(commandCtx context.Context, command string, args []string) ([]byte, error) {
					if err := commandCtx.Err(); err != nil {
						return nil, err
					}
					isMutation := command == MacOSSudoProgram && len(args) > 3 && args[2] == launchctlProgram
					if isMutation && args[3] == "bootstrap" && recoveryStarted.IsZero() {
						recoveryStarted = time.Now()
						cancel()
						return nil, cause
					}
					if isMutation && args[3] == "bootout" && !recoveryStarted.IsZero() {
						if _, bounded := commandCtx.Deadline(); !bounded {
							t.Fatal("native recovery has no deadline")
						}
						if test.stall {
							<-commandCtx.Done()
							return nil, commandCtx.Err()
						}
						timer := time.NewTimer(test.restoreTime)
						defer timer.Stop()
						select {
						case <-commandCtx.Done():
							return nil, commandCtx.Err()
						case <-timer.C:
						}
					}
					if command == launchctlProgram && args[0] == "print" && !restoredAt.IsZero() && time.Since(restoredAt) < test.startupTime {
						return []byte("state = waiting\n"), nil
					}
					output, err := runner.execute(command, args)
					if isMutation && args[3] == "bootstrap" && err == nil {
						restoredAt = time.Now()
					}
					return output, err
				}
				transaction, err := StageDefinitionReplace(ctx, adapter, original.Reference(), changed, ModePersistent, nil)
				if transaction != nil || !errors.Is(err, cause) {
					t.Fatalf("replacement did not report the original failure: transaction=%v err=%v", transaction, err)
				}
				elapsed := time.Since(recoveryStarted)
				var blocked *RecoveryBlockedError
				if test.stall {
					if !errors.As(err, &blocked) || !errors.Is(err, context.DeadlineExceeded) || elapsed > time.Minute {
						t.Fatalf("stalled recovery not bounded: elapsed=%s err=%v", elapsed, err)
					}
					return
				}
				if errors.As(err, &blocked) {
					t.Fatalf("healthy recovery exhausted its budget after %s: %v", elapsed, err)
				}
				if required := test.restoreTime + test.startupTime + adapter.transitionStable; elapsed < required {
					t.Fatalf("recovery skipped the production stability check: elapsed=%s required=%s", elapsed, required)
				}
				target := "system/" + original.UnitName
				if !runner.loaded[target] || runner.disabled[target] || !bytes.Equal(runner.definitions[target], original.Content) {
					t.Fatal("original running definition and persistence were not restored")
				}
				t.Logf("recovered the original provider after %s with the %s production stability window", elapsed, adapter.transitionStable)
			})
		})
	}
}
