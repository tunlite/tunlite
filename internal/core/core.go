package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

const (
	ConfigVersion = 1
	Kind          = "Connection"
)

var (
	validName  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
	validGroup = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,63}$`)
)

type Connection struct {
	// ConfigVersion is inherited from the containing ConnectionList and is never
	// repeated inside items on disk.
	ConfigVersion int            `json:"-"`
	Kind          string         `json:"kind"`
	Metadata      Metadata       `json:"metadata"`
	Spec          ConnectionSpec `json:"spec"`
}

type Metadata struct {
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
}

type ConnectionSpec struct {
	Driver string          `json:"driver"`
	Config json.RawMessage `json:"config"`
}

type Driver interface {
	ID() string
	Plan(context.Context, Connection, PlanOptions) (plan.ExecutionPlan, error)
}

type PlanOptions struct {
	NonInteractive bool
}

type Action struct {
	Type       string         `json:"type"`
	Parameters map[string]any `json:"parameters"`
}

type Condition struct {
	Code       string   `json:"code"`
	Severity   string   `json:"severity"`
	Message    string   `json:"message"`
	Driver     string   `json:"driver,omitempty"`
	Connection string   `json:"connection,omitempty"`
	Actions    []Action `json:"actions"`
}

type CheckOptions struct {
	Timeout time.Duration
}

type CheckReport struct {
	Healthy    bool        `json:"healthy"`
	ObservedAt time.Time   `json:"observedAt"`
	Conditions []Condition `json:"conditions"`
}

type CheckDriver interface {
	Check(context.Context, Connection, CheckOptions) (CheckReport, error)
}

type Registry struct {
	drivers map[string]Driver
}

func NewRegistry(drivers ...Driver) (*Registry, error) {
	r := &Registry{drivers: make(map[string]Driver, len(drivers))}
	for _, driver := range drivers {
		if driver == nil || driver.ID() == "" {
			return nil, errors.New("driver ID must not be empty")
		}
		if _, exists := r.drivers[driver.ID()]; exists {
			return nil, fmt.Errorf("driver %q is already registered", driver.ID())
		}
		r.drivers[driver.ID()] = driver
	}
	return r, nil
}

func (r *Registry) Driver(id string) (Driver, bool) {
	driver, ok := r.drivers[id]
	return driver, ok
}

type Planner struct {
	registry *Registry
}

func NewPlanner(registry *Registry) *Planner {
	return &Planner{registry: registry}
}

func (p *Planner) Plan(ctx context.Context, connection Connection, options PlanOptions) (plan.ExecutionPlan, error) {
	if err := ValidateConnection(connection); err != nil {
		return plan.ExecutionPlan{}, err
	}
	driver, ok := p.registry.Driver(connection.Spec.Driver)
	if !ok {
		return plan.ExecutionPlan{}, fmt.Errorf("driver %q is not installed", connection.Spec.Driver)
	}
	executionPlan, err := driver.Plan(ctx, connection, options)
	if err != nil {
		return plan.ExecutionPlan{}, err
	}
	if err := plan.Validate(executionPlan); err != nil {
		return plan.ExecutionPlan{}, fmt.Errorf("driver %q returned an invalid execution plan: %w", driver.ID(), err)
	}
	return executionPlan, nil
}

func (p *Planner) Check(ctx context.Context, connection Connection, options CheckOptions) (CheckReport, error) {
	if err := ValidateConnection(connection); err != nil {
		return CheckReport{}, err
	}
	driver, ok := p.registry.Driver(connection.Spec.Driver)
	if !ok {
		return CheckReport{}, fmt.Errorf("driver %q is not installed", connection.Spec.Driver)
	}
	checker, ok := driver.(CheckDriver)
	if !ok {
		return CheckReport{}, fmt.Errorf("driver %q does not support check", connection.Spec.Driver)
	}
	return checker.Check(ctx, connection, options)
}

func ValidateConnection(connection Connection) error {
	if connection.ConfigVersion != 0 && connection.ConfigVersion != ConfigVersion {
		return fmt.Errorf("unsupported configVersion %d", connection.ConfigVersion)
	}
	if connection.Kind != Kind {
		return fmt.Errorf("unsupported kind %q", connection.Kind)
	}
	if !validName.MatchString(connection.Metadata.Name) {
		return fmt.Errorf("invalid connection name %q", connection.Metadata.Name)
	}
	if connection.Metadata.Group != "" && !validGroup.MatchString(connection.Metadata.Group) {
		return fmt.Errorf("invalid connection group %q", connection.Metadata.Group)
	}
	if connection.Spec.Driver == "" {
		return errors.New("spec.driver is required")
	}
	if len(connection.Spec.Config) == 0 || !json.Valid(connection.Spec.Config) {
		return errors.New("spec.config must be valid JSON")
	}
	var configObject map[string]json.RawMessage
	if err := json.Unmarshal(connection.Spec.Config, &configObject); err != nil || configObject == nil {
		return errors.New("spec.config must be a JSON object")
	}
	return nil
}
