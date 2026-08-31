package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tunlite/tunlite/internal/plan"
)

type declaredScopeAdapter struct {
	observationAdapter
}

func (declaredScopeAdapter) Capabilities() Capabilities {
	return Capabilities{Registration: true, UserScope: true, SystemScope: true}
}

func (adapter declaredScopeAdapter) TargetScoped(scope Scope, connection string) (Artifact, error) {
	return Artifact{Connection: connection, Adapter: adapter.Name(), Scope: scope}, nil
}

func (adapter declaredScopeAdapter) BuildScoped(scope Scope, connection string, value plan.ExecutionPlan) (Artifact, error) {
	artifact, err := adapter.TargetScoped(scope, connection)
	artifact.Plan = value
	return artifact, err
}

func TestServiceStoreRejectsExistingRecordWithoutScope(t *testing.T) {
	root := t.TempDir()
	missingScope := []byte(`{"recordRevision":1,"connection":"db","serviceKey":"db","adapter":"systemd","mode":"stopped","unitName":"tunlite-db.service"}`)
	if err := os.WriteFile(filepath.Join(root, "db.json"), missingScope, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(root)
	if _, exists, err := store.Load("db"); err == nil || exists || !strings.Contains(err.Error(), "service scope is required") {
		t.Fatalf("exists=%t err=%v", exists, err)
	}
}

func TestServiceStoreNewRecordDefaultsToUserButWriteRequiresExplicitScope(t *testing.T) {
	store := NewStore(t.TempDir())
	record, exists, err := store.Load("db")
	if err != nil || exists || record.Scope != ScopeUser {
		t.Fatalf("new record=%#v exists=%t err=%v", record, exists, err)
	}
	record.Adapter = "systemd"
	record.Scope = ""
	if err := store.Write(context.Background(), record); err == nil || !strings.Contains(err.Error(), "service scope is required") {
		t.Fatalf("missing scope write error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(store.root, "db.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-scope write created state: %v", err)
	}
	record.Scope = ScopeUser
	if err := store.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.root, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["scope"] != string(ScopeUser) {
		t.Fatalf("stored scope=%#v document=%s", stored["scope"], raw)
	}
	record.Scope = "host"
	if err := store.Write(context.Background(), record); err == nil {
		t.Fatal("unknown scope was accepted")
	}
}

func TestScopedBuildDefaultsLegacyAdaptersToUserAndRejectsSystem(t *testing.T) {
	legacy := observationAdapter{}
	artifact, err := BuildScoped(legacy, "", "db", testPlan())
	if err != nil || artifact.Scope != ScopeUser {
		t.Fatalf("artifact=%#v err=%v", artifact, err)
	}
	_, err = BuildScoped(legacy, ScopeSystem, "db", testPlan())
	var unsupported *UnsupportedScopeError
	if !errors.As(err, &unsupported) || unsupported.Code() != "scope_unsupported" {
		t.Fatalf("system error=%T %v", err, err)
	}

	scoped := declaredScopeAdapter{}
	artifact, err = BuildScoped(scoped, ScopeSystem, "db", testPlan())
	if err != nil || artifact.Scope != ScopeSystem {
		t.Fatalf("system artifact=%#v err=%v", artifact, err)
	}
}

func TestScopeTransitionPlanIsExplicitAndSymmetric(t *testing.T) {
	adapter := declaredScopeAdapter{}
	record := Record{Connection: "db", Scope: ScopeUser}
	forward, err := PlanScopeTransition(adapter, record, ScopeSystem)
	if err != nil {
		t.Fatal(err)
	}
	record.Scope = ScopeSystem
	reverse, err := PlanScopeTransition(adapter, record, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"preflight", "stop_source", "install_target", "start_target", "observe_target", "record_scope", "remove_source"}
	stepNames := func(value ScopeTransitionPlan) []string {
		result := make([]string, len(value.Steps))
		for index, step := range value.Steps {
			result[index] = step.Name
		}
		return result
	}
	if !forward.RequiresPrivilege || !reverse.RequiresPrivilege ||
		!reflect.DeepEqual(stepNames(forward), wantNames) || !reflect.DeepEqual(stepNames(reverse), wantNames) {
		t.Fatalf("forward=%#v reverse=%#v", forward, reverse)
	}
	if !forward.Steps[0].RequiresPrivilege || forward.Steps[1].RequiresPrivilege ||
		reverse.Steps[0].RequiresPrivilege || !reverse.Steps[1].RequiresPrivilege {
		t.Fatalf("privilege annotations are not scope-specific: forward=%#v reverse=%#v", forward.Steps, reverse.Steps)
	}
}
