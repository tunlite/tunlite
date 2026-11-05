package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tunlite/tunlite/internal/service"
)

type disabledScopeTestAdapter struct {
	*scopedFakeServiceAdapter
	afterInstall func()
	commitError  error
}

func (a *disabledScopeTestAdapter) StageScopeInstall(ctx context.Context, target service.Artifact, mode string, verify func(context.Context, service.Reference) error) (service.ScopeTransition, error) {
	transaction, err := a.scopedFakeServiceAdapter.StageScopeInstall(ctx, target, mode, verify)
	if err != nil {
		return nil, err
	}
	if a.afterInstall != nil {
		a.afterInstall()
	}
	return &disabledScopeTestTransaction{ScopeTransition: transaction, commitError: a.commitError}, nil
}

type disabledScopeTestTransaction struct {
	service.ScopeTransition
	commitError error
}

func (t *disabledScopeTestTransaction) Commit(ctx context.Context) error {
	if t.commitError != nil {
		return t.commitError
	}
	return t.ScopeTransition.Commit(ctx)
}

func TestDisabledSystemConnectionCanEnableUserScopeTransactionally(t *testing.T) {
	for _, outcome := range []string{"success", "dry run", "install failure", "record failure", "commit failure", "unexpected running source", "unexpected missing definition"} {
		t.Run(outcome, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TUNLITE_HOME", home)
			setTestSSH(t)
			adapter := &disabledScopeTestAdapter{scopedFakeServiceAdapter: &scopedFakeServiceAdapter{
				fakeServiceAdapter: newFakeServiceAdapter(filepath.Join(home, "native")),
			}}
			authorizer := &fakePrivilegeAuthorizer{}
			invoke := func(ctx context.Context, args ...string) (int, string) {
				var out bytes.Buffer
				code := runContextWithOptions(ctx, args, IO{Out: &out, Err: io.Discard}, options{
					serviceAdapter: func() (service.Adapter, error) { return adapter, nil }, privilegeAuthorizer: authorizer,
				})
				return code, out.String()
			}
			for _, args := range [][]string{
				{"add", "db", "-R", "1080:localhost:1080", "me@example.com", "--json"},
				{"enable", "db", "--scope", "system", "--json"},
				{"rename", "db", "renamed", "--json"},
				{"disable", "renamed", "--json"},
			} {
				if code, out := invoke(context.Background(), args...); code != 0 {
					t.Fatalf("setup %v: %d %s", args, code, out)
				}
			}
			records, _ := serviceRecordStore()
			before, exists, err := records.Load("renamed")
			if err != nil || !exists || before.Mode != service.ModeStopped || before.Scope != service.ScopeSystem || before.ArtifactPath != "" {
				t.Fatalf("disabled source not retained: %+v %v", before, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch outcome {
			case "install failure":
				adapter.stageErr = errors.New("user install failed")
			case "record failure":
				adapter.afterInstall = cancel
			case "commit failure":
				adapter.commitError = errors.New("commit failed")
			case "unexpected running source":
				observation := adapter.observed["db"]
				observation.ProcessState = service.ProcessRunning
				adapter.observed["db"] = observation
			case "unexpected missing definition":
				before.ArtifactPath = filepath.Join(home, "native", "missing.system.service")
				if err := records.Write(ctx, before); err != nil {
					t.Fatal(err)
				}
			}
			authorizations := authorizer.calls
			stageCalls := adapter.stageCalls
			args := []string{"enable", "renamed", "--scope", "user", "--json"}
			if outcome == "dry run" {
				args = append(args, "--dry-run")
			}
			code, output := invoke(ctx, args...)
			after, _, err := records.Load("renamed")
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "success" {
				if code != 0 || after.Scope != service.ScopeUser || after.Mode != service.ModePersistent {
					t.Fatalf("disabled system -> user failed: code=%d record=%+v output=%s", code, after, output)
				}
				if after.ServiceKey != "db" || after.UnitName != "fake-db" || !adapter.observed["db"].AutoStart || adapter.observed["db"].ProcessState != service.ProcessRunning {
					t.Fatalf("native identity or lifecycle lost: %+v %+v", after, adapter.observed["db"])
				}
			} else {
				if outcome == "dry run" {
					if code != 0 || !strings.Contains(output, `"outcome":"planned"`) || adapter.stageCalls != stageCalls {
						t.Fatalf("dry-run: code=%d output=%s", code, output)
					}
				} else if code == 0 {
					t.Fatalf("%s unexpectedly succeeded: %s", outcome, output)
				}
				wantFailure := map[string]string{"install failure": "user install failed", "record failure": "context canceled", "commit failure": "commit failed"}[outcome]
				if wantFailure != "" && !strings.Contains(output, wantFailure) {
					t.Fatalf("did not reach %s: %s", outcome, output)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("failed enable changed record: before=%+v after=%+v", before, after)
				}
				if _, installed := adapter.artifacts["db"]; installed {
					t.Fatalf("%s left a user artifact", outcome)
				}
				if outcome != "unexpected running source" && adapter.observed["db"].ProcessState != service.ProcessInactive {
					t.Fatal("failed enable left a running target")
				}
			}
			if authorizer.calls != authorizations {
				t.Fatal("absent stopped system source caused an unnecessary administrator authorization")
			}
		})
	}
}
