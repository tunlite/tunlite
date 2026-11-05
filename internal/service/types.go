package service

import (
	"context"
	"io"
	"time"

	"github.com/tunlite/tunlite/internal/core"
	"github.com/tunlite/tunlite/internal/plan"
)

const CurrentRecordRevision = 1

type Scope string

const (
	ScopeUser   Scope = "user"
	ScopeSystem Scope = "system"
)

func NormalizeScope(scope Scope) Scope {
	if scope == "" {
		return ScopeUser
	}
	return scope
}

const (
	ModeStopped    = "stopped"
	ModeForeground = "foreground"
	ModeBackground = "background"
	ModePersistent = "persistent"
	ModeUnknown    = "unknown"

	ServiceRegistered  = "registered"
	ServiceAbsent      = "absent"
	ServiceUnsupported = "unsupported"
	ServiceUnknown     = "unknown"

	ProcessInactive = "inactive"
	ProcessStarting = "starting"
	ProcessRunning  = "running"
	ProcessFailed   = "failed"
	ProcessUnknown  = "unknown"

	HealthUnknown   = "unknown"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"

	ConsistencyConsistent = "consistent"
	ConsistencyDrifted    = "drifted"
	ConsistencyMissing    = "missing"
	ConsistencyBlocked    = "blocked"
)

// Record is machine-local coordination state. It is not part of the portable
// Connection declaration. The record stores the native identity explicitly so
// a managed service does not have to be rediscovered on every CLI invocation.
type Record struct {
	RecordRevision  int      `json:"recordRevision"`
	Connection      string   `json:"connection"`
	ServiceKey      string   `json:"serviceKey,omitempty"`
	Adapter         string   `json:"adapter"`
	Scope           Scope    `json:"scope"`
	Mode            string   `json:"mode"`
	ArtifactPath    string   `json:"artifactPath,omitempty"`
	UnitName        string   `json:"unitName,omitempty"`
	LogPaths        []string `json:"logPaths,omitempty"`
	PlanDigest      string   `json:"planDigest,omitempty"`
	BaselineDigest  string   `json:"baselineDigest,omitempty"`
	DeclarationPath string   `json:"declarationPath,omitempty"`
	Origin          string   `json:"origin,omitempty"`
}

type Reference struct {
	Connection     string
	Adapter        string
	Scope          Scope
	UnitName       string
	ArtifactPath   string
	LogPaths       []string
	PlanDigest     string
	BaselineDigest string
}

func (r Record) Reference() Reference {
	serviceKey := r.ServiceKey
	if serviceKey == "" {
		serviceKey = r.Connection
	}
	return Reference{
		Connection: serviceKey, Adapter: r.Adapter, Scope: NormalizeScope(r.Scope), UnitName: r.UnitName,
		ArtifactPath: r.ArtifactPath, LogPaths: append([]string(nil), r.LogPaths...),
		PlanDigest: r.PlanDigest, BaselineDigest: r.BaselineDigest,
	}
}

type Artifact struct {
	Connection string
	Adapter    string
	Scope      Scope
	UnitName   string
	Path       string
	Content    []byte
	PlanDigest string
	LogPaths   []string
	Plan       plan.ExecutionPlan
}

func (a Artifact) Reference() Reference {
	return Reference{
		Connection: a.Connection, Adapter: a.Adapter, Scope: NormalizeScope(a.Scope), UnitName: a.UnitName,
		ArtifactPath: a.Path, LogPaths: append([]string(nil), a.LogPaths...),
		PlanDigest: a.PlanDigest, BaselineDigest: ContentDigest(a.Content),
	}
}

type Observation struct {
	Scope        Scope
	Installed    bool
	AutoStart    bool
	ProcessState string
	Artifact     []byte
	Platform     map[string]any
}

type Candidate struct {
	Reference   Reference
	Plan        plan.ExecutionPlan
	Observation Observation
}

type State struct {
	Connection       string           `json:"connection"`
	Adapter          string           `json:"adapter,omitempty"`
	Scope            Scope            `json:"scope"`
	ServiceState     string           `json:"serviceState"`
	Mode             string           `json:"mode"`
	ProcessState     string           `json:"processState"`
	Health           string           `json:"health"`
	ConsistencyState string           `json:"consistencyState"`
	ObservedAt       time.Time        `json:"observedAt"`
	Endpoints        []plan.Endpoint  `json:"endpoints"`
	Conditions       []core.Condition `json:"conditions"`
	Platform         map[string]any   `json:"platform,omitempty"`
}

type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// Streaming forwards all output but retains only a bounded diagnostic
	// tail. Ordinary finite commands keep their complete captured output.
	Streaming bool
}

type Capabilities struct {
	Registration bool `json:"registration"`
	Background   bool `json:"background"`
	Persistence  bool `json:"persistence"`
	Logs         bool `json:"logs"`
	Discovery    bool `json:"discovery"`
	UserScope    bool `json:"userScope"`
	SystemScope  bool `json:"systemScope"`
}

// Adapter owns operating-system service mechanics only. It never parses a
// Connection or makes Provider-specific decisions.
type Adapter interface {
	Name() string
	Capabilities() Capabilities
	Build(string, plan.ExecutionPlan) (Artifact, error)
	BuildBound(Reference, plan.ExecutionPlan) (Artifact, error)
	Target(string) Artifact
	Materialize(context.Context, Artifact) error
	Remove(context.Context, Reference) error
	Start(context.Context, Reference) error
	Stop(context.Context, Reference) error
	Restart(context.Context, Reference) error
	Enable(context.Context, Reference) error
	Disable(context.Context, Reference) error
	Inspect(context.Context, Reference) (Observation, error)
	Logs(context.Context, Reference, int, bool, Streams) error
	Discover(context.Context) ([]Candidate, error)
}

type CommandRunner interface {
	Run(context.Context, string, []string, Streams) ([]byte, error)
}
