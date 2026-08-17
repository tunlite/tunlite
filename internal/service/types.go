package service

import (
	"context"
	"io"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

const RecordVersion = "tunlite.service/v1"

const (
	DesiredEnabled  = "enabled"
	DesiredDisabled = "disabled"

	ProcessInactive = "inactive"
	ProcessStarting = "starting"
	ProcessRunning  = "running"
	ProcessFailed   = "failed"
	ProcessUnknown  = "unknown"

	HealthUnknown   = "unknown"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"

	SyncSynced  = "synced"
	SyncDrifted = "drifted"
	SyncMissing = "missing"
	SyncBlocked = "blocked"
)

type Record struct {
	APIVersion         string `json:"apiVersion"`
	Connection         string `json:"connection"`
	Adapter            string `json:"adapter"`
	DesiredState       string `json:"desiredState"`
	ArtifactPath       string `json:"artifactPath,omitempty"`
	UnitName           string `json:"unitName,omitempty"`
	PlanDigest         string `json:"planDigest,omitempty"`
	ApprovedPlanDigest string `json:"approvedPlanDigest,omitempty"`
	DeclarationPath    string `json:"declarationPath,omitempty"`
}

type Artifact struct {
	Connection string
	Adapter    string
	UnitName   string
	Path       string
	Content    []byte
	PlanDigest string
	LogPaths   []string
	Plan       plan.ExecutionPlan
}

type Observation struct {
	Installed    bool
	Enabled      bool
	ProcessState string
	Artifact     []byte
	Platform     map[string]any
}

type State struct {
	Connection   string           `json:"connection"`
	Adapter      string           `json:"adapter"`
	DesiredState string           `json:"desiredState"`
	ProcessState string           `json:"processState"`
	Health       string           `json:"health"`
	SyncState    string           `json:"syncState"`
	ObservedAt   time.Time        `json:"observedAt"`
	Endpoints    []plan.Endpoint  `json:"endpoints"`
	Conditions   []core.Condition `json:"conditions"`
	Platform     map[string]any   `json:"platform,omitempty"`
}

type Streams struct {
	Out io.Writer
	Err io.Writer
}

type Adapter interface {
	Name() string
	Build(string, plan.ExecutionPlan) (Artifact, error)
	Target(string) Artifact
	Enable(context.Context, Artifact) error
	Disable(context.Context, string) error
	Restart(context.Context, string) error
	Inspect(context.Context, string) (Observation, error)
	Logs(context.Context, string, int, bool, Streams) error
}

type CommandRunner interface {
	Run(context.Context, string, []string, Streams) ([]byte, error)
}
