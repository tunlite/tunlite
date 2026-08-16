package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

const (
	APIVersion = "tunlite.dev/v1"
	Kind       = "Connection"
)

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

type Connection struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   Metadata       `json:"metadata"`
	Spec       ConnectionSpec `json:"spec"`
}

type Metadata struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

type ConnectionSpec struct {
	Driver        string          `json:"driver"`
	Lifecycle     string          `json:"lifecycle"`
	ConfigVersion int             `json:"configVersion"`
	Config        json.RawMessage `json:"config"`
}

type Driver interface {
	ID() string
	Plan(context.Context, Connection, PlanOptions) (plan.ExecutionPlan, error)
}

type PlanOptions struct {
	NonInteractive bool
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
	if err := validateEnvelope(connection); err != nil {
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

func validateEnvelope(connection Connection) error {
	if connection.APIVersion != APIVersion {
		return fmt.Errorf("unsupported apiVersion %q", connection.APIVersion)
	}
	if connection.Kind != Kind {
		return fmt.Errorf("unsupported kind %q", connection.Kind)
	}
	if !validName.MatchString(connection.Metadata.Name) {
		return fmt.Errorf("invalid connection name %q", connection.Metadata.Name)
	}
	if connection.Spec.Driver == "" {
		return errors.New("spec.driver is required")
	}
	if connection.Spec.Lifecycle != "foreground" && connection.Spec.Lifecycle != "persistent" {
		return fmt.Errorf("unsupported lifecycle %q", connection.Spec.Lifecycle)
	}
	if connection.Spec.ConfigVersion < 1 {
		return errors.New("spec.configVersion must be at least 1")
	}
	if len(connection.Spec.Config) == 0 || !json.Valid(connection.Spec.Config) {
		return errors.New("spec.config must be valid JSON")
	}
	return nil
}
