package core

import (
	"bytes"
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
	validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
	validTag  = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,63}$`)
)

type Connection struct {
	// ConfigVersion is inherited from the containing ConnectionList. It remains
	// in memory for validation and preview compatibility, but is never repeated
	// inside items on disk.
	ConfigVersion int            `json:"-"`
	Kind          string         `json:"kind"`
	Metadata      Metadata       `json:"metadata"`
	Spec          ConnectionSpec `json:"spec"`
	legacyPreview bool
}

type Metadata struct {
	Name string   `json:"name"`
	Tags []string `json:"tags,omitempty"`
}

type ConnectionSpec struct {
	Driver string          `json:"driver"`
	Config json.RawMessage `json:"config"`
}

// UnmarshalJSON accepts the unreleased Go preview envelope as a strict,
// read-only compatibility input. Encoding always emits the current public
// configVersion envelope. Runtime mode is never persisted in a Connection.
func (c *Connection) UnmarshalJSON(raw []byte) error {
	var value struct {
		ConfigVersion *int     `json:"configVersion"`
		APIVersion    string   `json:"apiVersion,omitempty"`
		Kind          string   `json:"kind"`
		Metadata      Metadata `json:"metadata"`
		Spec          struct {
			Driver              string          `json:"driver"`
			LegacyLifecycle     string          `json:"lifecycle,omitempty"`
			LegacyConfigVersion *int            `json:"configVersion,omitempty"`
			Config              json.RawMessage `json:"config"`
		} `json:"spec"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if value.Spec.LegacyLifecycle != "" && value.Spec.LegacyLifecycle != "foreground" && value.Spec.LegacyLifecycle != "persistent" {
		return fmt.Errorf("unsupported legacy lifecycle %q", value.Spec.LegacyLifecycle)
	}
	version := 0
	switch {
	case value.ConfigVersion == nil && value.APIVersion == "" && value.Spec.LegacyConfigVersion == nil:
		// Current ConnectionList items inherit the document's root version.
		version = 0
		c.legacyPreview = false
	case value.ConfigVersion != nil && value.APIVersion == "" && value.Spec.LegacyConfigVersion == nil:
		// Read compatibility for early Go preview files that repeated the root
		// version on every item. MarshalJSON intentionally drops it.
		version = *value.ConfigVersion
		c.legacyPreview = false
	case value.ConfigVersion == nil && value.APIVersion == "tunlite.dev/v1" && value.Spec.LegacyConfigVersion != nil && *value.Spec.LegacyConfigVersion == 1:
		version = ConfigVersion
		c.legacyPreview = true
	default:
		return errors.New("Connection must use one supported config version envelope")
	}
	c.ConfigVersion = version
	c.Kind = value.Kind
	c.Metadata = value.Metadata
	c.Spec = ConnectionSpec{Driver: value.Spec.Driver, Config: value.Spec.Config}
	return nil
}

func (c Connection) MarshalJSON() ([]byte, error) {
	type currentConnection struct {
		Kind     string         `json:"kind"`
		Metadata Metadata       `json:"metadata"`
		Spec     ConnectionSpec `json:"spec"`
	}
	return json.Marshal(currentConnection{Kind: c.Kind, Metadata: c.Metadata, Spec: c.Spec})
}

// LegacyPreviewEnvelope reports whether decoding used the unreleased
// apiVersion/spec.configVersion envelope. It is only used to reject mixed
// documents and to create a byte-for-byte backup before the first write.
func (c Connection) LegacyPreviewEnvelope() bool { return c.legacyPreview }

func (c Connection) CurrentEnvelope() Connection {
	c.legacyPreview = false
	return c
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
	seenTags := make(map[string]struct{}, len(connection.Metadata.Tags))
	for _, tag := range connection.Metadata.Tags {
		if !validTag.MatchString(tag) {
			return fmt.Errorf("invalid connection tag %q", tag)
		}
		if _, exists := seenTags[tag]; exists {
			return fmt.Errorf("duplicate connection tag %q", tag)
		}
		seenTags[tag] = struct{}{}
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
