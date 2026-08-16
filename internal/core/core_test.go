package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

type fakeDriver struct {
	called bool
}

func (d *fakeDriver) ID() string { return "example.test/fake" }

func (d *fakeDriver) Plan(_ context.Context, _ Connection, _ PlanOptions) (plan.ExecutionPlan, error) {
	d.called = true
	return plan.ExecutionPlan{
		Mode:      plan.ModeDirect,
		Exec:      plan.Exec{Program: "/bin/fake", Args: []string{"serve"}},
		Lifecycle: plan.Lifecycle{Restart: "never", StopTimeoutSeconds: 5},
	}, nil
}

func TestPlannerUsesDriverWithoutProviderSpecialCases(t *testing.T) {
	driver := &fakeDriver{}
	registry, err := NewRegistry(driver)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]string{"value": "opaque-to-core"})
	connection := Connection{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "fake-connection"},
		Spec: ConnectionSpec{
			Driver: "example.test/fake", Lifecycle: "foreground", ConfigVersion: 1, Config: config,
		},
	}

	got, err := NewPlanner(registry).Plan(context.Background(), connection, PlanOptions{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !driver.called {
		t.Fatal("core did not dispatch through the registered driver")
	}
	if got.Exec.Program != "/bin/fake" {
		t.Fatalf("unexpected plan: %#v", got)
	}
}

type invalidDriver struct{}

func (*invalidDriver) ID() string { return "example.test/invalid" }

func (*invalidDriver) Plan(_ context.Context, _ Connection, _ PlanOptions) (plan.ExecutionPlan, error) {
	return plan.ExecutionPlan{Mode: "shell"}, nil
}

func TestPlannerRejectsInvalidDriverPlan(t *testing.T) {
	registry, err := NewRegistry(&invalidDriver{})
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]string{"value": "opaque-to-core"})
	connection := Connection{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "invalid-plan"},
		Spec: ConnectionSpec{
			Driver: "example.test/invalid", Lifecycle: "foreground", ConfigVersion: 1, Config: config,
		},
	}
	if _, err := NewPlanner(registry).Plan(context.Background(), connection, PlanOptions{}); err == nil {
		t.Fatal("core accepted an invalid driver plan")
	}
}
