package plan

import "testing"

func TestValidateRejectsInvalidDriverPlans(t *testing.T) {
	valid := ExecutionPlan{
		Mode:         ModeDirect,
		Exec:         Exec{Program: "/usr/bin/provider", Args: []string{"serve"}},
		Lifecycle:    Lifecycle{Restart: "never", StopTimeoutSeconds: 5},
		Endpoints:    []Endpoint{{Name: "default", Direction: "access", Protocol: "tcp", Address: "127.0.0.1:8080"}},
		HealthChecks: []HealthCheck{{Type: "tcp", Address: "127.0.0.1:8080", TimeoutMS: 1500}},
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("valid plan was rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ExecutionPlan)
	}{
		{"mode", func(value *ExecutionPlan) { value.Mode = "shell" }},
		{"program", func(value *ExecutionPlan) { value.Exec.Program = "" }},
		{"restart", func(value *ExecutionPlan) { value.Lifecycle.Restart = "sometimes" }},
		{"stop timeout", func(value *ExecutionPlan) { value.Lifecycle.StopTimeoutSeconds = 0 }},
		{"endpoint", func(value *ExecutionPlan) { value.Endpoints[0].Address = "missing-port" }},
		{"health type", func(value *ExecutionPlan) { value.HealthChecks[0].Type = "command" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			value.Endpoints = append([]Endpoint(nil), valid.Endpoints...)
			value.HealthChecks = append([]HealthCheck(nil), valid.HealthChecks...)
			test.mutate(&value)
			if err := Validate(value); err == nil {
				t.Fatal("invalid plan was accepted")
			}
		})
	}
}
