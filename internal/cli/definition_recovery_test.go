package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tunlite/tunlite/internal/configstore"
	sshdriver "github.com/tunlite/tunlite/internal/drivers/ssh"
	"github.com/tunlite/tunlite/internal/service"
)

type definitionRecoveryTestAdapter struct {
	*scopedFakeServiceAdapter
	afterStage func()
	recover    func(context.Context, service.ScopeTransition) error
}

func (a *definitionRecoveryTestAdapter) StageDefinitionReplace(ctx context.Context, source service.Reference, target service.Artifact, mode string, verify func(context.Context, service.Reference) error) (service.ScopeTransition, error) {
	transaction, err := a.scopedFakeServiceAdapter.StageDefinitionReplace(ctx, source, target, mode, verify)
	if err != nil {
		return nil, err
	}
	if a.afterStage != nil {
		a.afterStage()
	}
	return &definitionRecoveryTestTransaction{ScopeTransition: transaction, recover: a.recover}, nil
}

type definitionRecoveryTestTransaction struct {
	service.ScopeTransition
	recover func(context.Context, service.ScopeTransition) error
}

func (t *definitionRecoveryTestTransaction) Rollback(ctx context.Context) error {
	return t.recover(ctx, t.ScopeTransition)
}

func TestSystemDefinitionRecoveryPreservesRecordWriteBudget(t *testing.T) {
	for _, outcome := range []string{"healthy restoration", "native timeout still restores old record", "replacement record write canceled"} {
		t.Run(outcome, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			adapter := &definitionRecoveryTestAdapter{scopedFakeServiceAdapter: &scopedFakeServiceAdapter{
				fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")),
			}}
			invoke := useFakeServiceAdapter(t, adapter)
			for _, args := range [][]string{
				{"add", "db", "-D", "1080", "me@example.com", "--json"},
				{"enable", "db", "--scope", "system", "--json", "--non-interactive"},
			} {
				if code, out, stderr := invoke(t, args...); code != 0 {
					t.Fatalf("setup %v: code=%d out=%s err=%s", args, code, out, stderr)
				}
			}
			records, err := serviceRecordStore()
			if err != nil {
				t.Fatal(err)
			}
			oldRecord, exists, err := records.Load("db")
			if err != nil || !exists {
				t.Fatalf("old record missing: exists=%v err=%v", exists, err)
			}
			oldArtifact := adapter.artifacts["db"]
			declarations, err := connectionStore(options{})
			if err != nil {
				t.Fatal(err)
			}
			document, err := declarations.Load()
			if err != nil {
				t.Fatal(err)
			}
			before := document.Items[0]
			after := cloneConnection(before)
			config, err := sshdriver.DecodeConfig(after.Spec.Config)
			if err != nil {
				t.Fatal(err)
			}
			config.Forwards[0].SrcPort++
			after.Spec.Config, err = json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var recoveryStarted time.Time
				adapter.recover = func(recoveryCtx context.Context, transaction service.ScopeTransition) error {
					recoveryStarted = time.Now()
					if _, bounded := recoveryCtx.Deadline(); !bounded {
						t.Fatal("native recovery has no deadline")
					}
					// Model 8 s of native restoration, then 18 s of startup and
					// launchd's full 11 s stability window (within its 30 s limit).
					time.Sleep(8 * time.Second)
					if err := recoveryCtx.Err(); err != nil {
						return err
					}
					if err := transaction.Rollback(recoveryCtx); err != nil {
						return err
					}
					if outcome == "native timeout still restores old record" {
						<-recoveryCtx.Done()
						return recoveryCtx.Err()
					}
					timer := time.NewTimer(29 * time.Second)
					defer timer.Stop()
					select {
					case <-recoveryCtx.Done():
						return recoveryCtx.Err()
					case <-timer.C:
						return nil
					}
				}
				if outcome == "replacement record write canceled" {
					adapter.afterStage = cancel
				}
				mutation, err := stageUpdatedConnectionService(ctx, before, after, declarations.Path(), options{
					NonInteractive: true, serviceAdapter: func() (service.Adapter, error) { return adapter, nil },
				})
				if outcome == "replacement record write canceled" {
					if !errors.Is(err, context.Canceled) {
						t.Errorf("record-write failure could not recover: %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					defer mutation.finish()
					stagedRecord, _, err := records.Load("db")
					if err != nil || stagedRecord.PlanDigest == oldRecord.PlanDigest {
						t.Fatalf("replacement record was not staged: %+v err=%v", stagedRecord, err)
					}
					cancel()
					err = mutation.rollback()
					if outcome == "native timeout still restores old record" {
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Errorf("native timeout was not reported: %v", err)
						}
					} else if err != nil {
						t.Errorf("healthy recovery exhausted its budget: %v", err)
					}
				}
				elapsed := time.Since(recoveryStarted)
				if elapsed > time.Minute || (outcome != "native timeout still restores old record" && elapsed < 37*time.Second) {
					t.Errorf("unexpected recovery duration: %s", elapsed)
				}
				restored, exists, err := records.Load("db")
				if err != nil || !exists || !reflect.DeepEqual(restored, oldRecord) {
					t.Errorf("native recovery consumed the old-record write budget: got=%+v want=%+v err=%v", restored, oldRecord, err)
				}
				if !reflect.DeepEqual(adapter.artifacts["db"], oldArtifact) {
					t.Error("original native definition was not restored")
				}
				t.Logf("old record restored after %s of native recovery", elapsed)
			})
		})
	}
}

func TestResolveTunliteDefinitionRecordWriteFailureCoversProductionRecoveryBudget(t *testing.T) {
	for _, test := range []struct {
		name        string
		restoreTime time.Duration
		startupTime time.Duration
	}{
		{name: "production stability"},
		{name: "native restoration and startup", restoreTime: 8 * time.Second, startupTime: 18 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			adapter := &definitionRecoveryTestAdapter{scopedFakeServiceAdapter: &scopedFakeServiceAdapter{
				fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")),
			}}
			invoke := useFakeServiceAdapter(t, adapter)
			for _, args := range [][]string{
				{"add", "db", "-D", "1080", "me@example.com", "--json"},
				{"enable", "db", "--scope", "system", "--json", "--non-interactive"},
			} {
				if code, out, stderr := invoke(t, args...); code != 0 {
					t.Fatalf("setup %v: code=%d out=%s err=%s", args, code, out, stderr)
				}
			}
			records, err := serviceRecordStore()
			if err != nil {
				t.Fatal(err)
			}
			oldRecord, exists, err := records.Load("db")
			if err != nil || !exists {
				t.Fatalf("old record missing: exists=%v err=%v", exists, err)
			}
			oldArtifact, oldObservation := adapter.artifacts["db"], adapter.observed["db"]
			declarations, err := connectionStore(options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := declarations.Update(context.Background(), func(document *configstore.Document) error {
				_, connection := configstore.Find(document, "db")
				connection.Spec.Config = bytes.Replace(connection.Spec.Config, []byte("1080"), []byte("1081"), 1)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			desiredDocument, err := declarations.Load()
			if err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				adapter.afterStage = func() {
					if adapter.artifacts["db"].PlanDigest == oldArtifact.PlanDigest {
						t.Fatal("resolve did not stage the changed definition")
					}
					// Fail the real Store.Write after native replacement succeeds.
					cancel()
				}
				var recoveryStarted time.Time
				const productionStability = 11 * time.Second
				adapter.recover = func(recoveryCtx context.Context, transaction service.ScopeTransition) error {
					recoveryStarted = time.Now()
					if _, bounded := recoveryCtx.Deadline(); !bounded {
						t.Fatal("resolve native recovery has no deadline")
					}
					wait := func(duration time.Duration) error {
						timer := time.NewTimer(duration)
						defer timer.Stop()
						select {
						case <-recoveryCtx.Done():
							return recoveryCtx.Err()
						case <-timer.C:
							return nil
						}
					}
					if err := wait(test.restoreTime); err != nil {
						return err
					}
					if err := transaction.Rollback(recoveryCtx); err != nil {
						return err
					}
					return wait(test.startupTime + productionStability)
				}
				var out bytes.Buffer
				code := runContextWithOptions(ctx, []string{"resolve", "db", "--use", "tunlite", "--json", "--non-interactive"},
					IO{Out: &out, Err: io.Discard}, options{serviceAdapter: func() (service.Adapter, error) { return adapter, nil }})
				if code == 0 || !strings.Contains(out.String(), context.Canceled.Error()) || strings.Contains(out.String(), "recovery_blocked") {
					t.Errorf("resolve record-write failure did not recover: code=%d output=%s", code, out.String())
				}
				elapsed := time.Since(recoveryStarted)
				if required := test.restoreTime + test.startupTime + productionStability; recoveryStarted.IsZero() || elapsed < required || elapsed > time.Minute {
					t.Errorf("resolve skipped or exhausted production recovery: elapsed=%s required=%s", elapsed, required)
				}
				// The failed Store.Write is atomic: its existing record must still
				// describe the original provider restored by native rollback.
				restored, exists, err := records.Load("db")
				if err != nil || !exists || !reflect.DeepEqual(restored, oldRecord) {
					t.Errorf("resolve did not preserve the actual old record: got=%+v want=%+v err=%v", restored, oldRecord, err)
				}
				if !reflect.DeepEqual(adapter.artifacts["db"], oldArtifact) || !reflect.DeepEqual(adapter.observed["db"], oldObservation) {
					t.Error("resolve did not restore the original native definition and mode")
				}
				retainedDocument, err := declarations.Load()
				if err != nil || !reflect.DeepEqual(retainedDocument, desiredDocument) {
					t.Errorf("failed resolve changed the desired declaration: %v", err)
				}
				if !t.Failed() {
					t.Logf("resolve preserved the actual old record and restored its provider after %s", elapsed)
				}
			})
		})
	}
}
