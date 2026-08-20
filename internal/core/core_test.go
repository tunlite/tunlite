package core

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

func TestLegacyLifecycleIsAcceptedButNotWrittenAgain(t *testing.T) {
	raw := []byte(`{"apiVersion":"tunlite.dev/v1","kind":"Connection","metadata":{"name":"legacy"},"spec":{"driver":"example.test/fake","lifecycle":"persistent","configVersion":1,"config":{"value":"ok"}}}`)
	var connection Connection
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&connection); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("lifecycle")) {
		t.Fatalf("legacy lifecycle leaked back into the declaration: %s", encoded)
	}
	if bytes.Contains(encoded, []byte(`"configVersion"`)) || bytes.Contains(encoded, []byte(`"apiVersion"`)) {
		t.Fatalf("legacy envelope was not normalized: %s", encoded)
	}
}

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
		ConfigVersion: ConfigVersion,
		Kind:          Kind,
		Metadata:      Metadata{Name: "fake-connection"},
		Spec: ConnectionSpec{
			Driver: "example.test/fake", Config: config,
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
		ConfigVersion: ConfigVersion,
		Kind:          Kind,
		Metadata:      Metadata{Name: "invalid-plan"},
		Spec: ConnectionSpec{
			Driver: "example.test/invalid", Config: config,
		},
	}
	if _, err := NewPlanner(registry).Plan(context.Background(), connection, PlanOptions{}); err == nil {
		t.Fatal("core accepted an invalid driver plan")
	}
}

func TestValidateConnectionAllowsAllAsAnOrdinaryName(t *testing.T) {
	config, _ := json.Marshal(map[string]string{"value": "opaque"})
	connection := Connection{
		ConfigVersion: ConfigVersion,
		Kind:          Kind,
		Metadata:      Metadata{Name: "all"},
		Spec:          ConnectionSpec{Driver: "example.test/fake", Config: config},
	}
	if err := ValidateConnection(connection); err != nil {
		t.Fatalf("ordinary connection name was rejected: %v", err)
	}
}

func TestValidateConnectionRequiresConfigObject(t *testing.T) {
	connection := Connection{
		ConfigVersion: ConfigVersion,
		Kind:          Kind,
		Metadata:      Metadata{Name: "bad-config"},
		Spec:          ConnectionSpec{Driver: "example.test/fake", Config: json.RawMessage(`[]`)},
	}
	if err := ValidateConnection(connection); err == nil {
		t.Fatal("non-object driver config was accepted")
	}
}
