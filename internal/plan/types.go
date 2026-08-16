package plan

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	ModeDirect  = "direct"
	ModeManaged = "managed"
)

// ExecutionPlan is the provider-neutral boundary consumed by foreground and
// operating-system service adapters. It is always argv based; no shell command
// string is representable here.
type ExecutionPlan struct {
	Mode         string        `json:"mode"`
	Exec         Exec          `json:"exec"`
	Lifecycle    Lifecycle     `json:"lifecycle"`
	Endpoints    []Endpoint    `json:"endpoints"`
	HealthChecks []HealthCheck `json:"healthChecks"`
	Redactions   []Redaction   `json:"redactions"`
}

type Exec struct {
	Program          string            `json:"program"`
	Args             []string          `json:"args"`
	Environment      map[string]string `json:"environment"`
	WorkingDirectory *string           `json:"workingDirectory"`
}

type Lifecycle struct {
	Restart             string `json:"restart"`
	RestartDelaySeconds int    `json:"restartDelaySeconds"`
	StopTimeoutSeconds  int    `json:"stopTimeoutSeconds"`
}

type Endpoint struct {
	Name      string `json:"name"`
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	Address   string `json:"address"`
}

type HealthCheck struct {
	Type      string `json:"type"`
	Address   string `json:"address"`
	TimeoutMS int    `json:"timeoutMs"`
}

type Redaction struct {
	Source string `json:"source"`
}

// Validate enforces the provider-neutral contract before a plan reaches a
// foreground executor or an operating-system service adapter.
func Validate(value ExecutionPlan) error {
	if value.Mode != ModeDirect && value.Mode != ModeManaged {
		return fmt.Errorf("unsupported execution mode %q", value.Mode)
	}
	if strings.TrimSpace(value.Exec.Program) == "" {
		return errors.New("execution plan has no program")
	}
	if strings.ContainsRune(value.Exec.Program, '\x00') {
		return errors.New("execution program contains a null byte")
	}
	for i, arg := range value.Exec.Args {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("execution argument %d contains a null byte", i)
		}
	}
	for key, item := range value.Exec.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
		if strings.ContainsRune(item, '\x00') {
			return fmt.Errorf("environment variable %q contains a null byte", key)
		}
	}
	if value.Exec.WorkingDirectory != nil && strings.TrimSpace(*value.Exec.WorkingDirectory) == "" {
		return errors.New("working directory must not be empty")
	}

	switch value.Lifecycle.Restart {
	case "never", "on-failure", "always":
	default:
		return fmt.Errorf("unsupported restart policy %q", value.Lifecycle.Restart)
	}
	if value.Lifecycle.RestartDelaySeconds < 0 {
		return errors.New("restart delay must not be negative")
	}
	if value.Lifecycle.StopTimeoutSeconds <= 0 {
		return errors.New("stop timeout must be positive")
	}

	for i, endpoint := range value.Endpoints {
		if endpoint.Name == "" {
			return fmt.Errorf("endpoint %d has no name", i)
		}
		if endpoint.Direction != "access" && endpoint.Direction != "publish" {
			return fmt.Errorf("endpoint %q has unsupported direction %q", endpoint.Name, endpoint.Direction)
		}
		if endpoint.Protocol != "tcp" {
			return fmt.Errorf("endpoint %q has unsupported protocol %q", endpoint.Name, endpoint.Protocol)
		}
		if err := validateAddress(endpoint.Address); err != nil {
			return fmt.Errorf("endpoint %q: %w", endpoint.Name, err)
		}
	}
	for i, check := range value.HealthChecks {
		if check.Type != "tcp" {
			return fmt.Errorf("health check %d has unsupported type %q", i, check.Type)
		}
		if err := validateAddress(check.Address); err != nil {
			return fmt.Errorf("health check %d: %w", i, err)
		}
		if check.TimeoutMS <= 0 {
			return fmt.Errorf("health check %d timeout must be positive", i)
		}
	}
	for i, redaction := range value.Redactions {
		if redaction.Source == "" {
			return fmt.Errorf("redaction %d has no source", i)
		}
	}
	return nil
}

func validateAddress(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("invalid TCP address %q", address)
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return fmt.Errorf("invalid TCP address %q", address)
	}
	return nil
}
