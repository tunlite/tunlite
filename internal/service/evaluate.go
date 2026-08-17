package service

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

func Evaluate(ctx context.Context, adapter Adapter, record Record, expected *Artifact) State {
	state := State{
		Connection: record.Connection, Adapter: adapter.Name(), DesiredState: record.DesiredState,
		ProcessState: ProcessUnknown, Health: HealthUnknown, SyncState: SyncSynced,
		ObservedAt: time.Now().UTC(), Conditions: []core.Condition{},
	}
	if expected != nil {
		state.Endpoints = expected.Plan.Endpoints
	} else {
		state.Endpoints = []plan.Endpoint{}
	}
	observation, inspectErr := adapter.Inspect(ctx, record.Connection)
	if observation.ProcessState != "" {
		state.ProcessState = observation.ProcessState
	}
	state.Platform = observation.Platform
	if state.Platform == nil {
		state.Platform = map[string]any{}
	}
	if inspectErr != nil {
		state.SyncState = SyncBlocked
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_manager_unavailable", "error",
			fmt.Sprintf("%s could not inspect the service: %v", adapter.Name(), inspectErr),
			[]core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{"adapter": adapter.Name()}}}))
		return state
	}
	if record.DesiredState == DesiredEnabled && record.Adapter != "" && record.Adapter != adapter.Name() {
		state.SyncState = SyncBlocked
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_adapter_changed", "error",
			fmt.Sprintf("connection was enabled with %s but this machine uses %s", record.Adapter, adapter.Name()), nil))
		return state
	}
	if record.DesiredState == DesiredEnabled {
		switch {
		case expected == nil:
			state.SyncState = SyncBlocked
		case !observation.Installed:
			state.SyncState = SyncMissing
			state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_artifact_missing", "error",
				"the connection is enabled but its native service artifact is missing",
				[]core.Action{{Type: "sync_connection", Parameters: map[string]any{"connection": record.Connection}}}))
		case !bytes.Equal(observation.Artifact, expected.Content):
			state.SyncState = SyncDrifted
			state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_artifact_drifted", "warning",
				"the native service artifact differs from the current execution plan",
				[]core.Action{{Type: "sync_connection", Parameters: map[string]any{"connection": record.Connection}}}))
		case !observation.Enabled:
			state.SyncState = SyncDrifted
			state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_not_enabled", "warning",
				"the native service exists but is not enabled",
				[]core.Action{{Type: "sync_connection", Parameters: map[string]any{"connection": record.Connection}}}))
		}
	} else if observation.Installed || observation.Enabled || observation.ProcessState == ProcessRunning || observation.ProcessState == ProcessStarting {
		state.SyncState = SyncDrifted
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "disabled_service_present", "warning",
			"the connection is disabled but its native service is still present or running",
			[]core.Action{{Type: "sync_connection", Parameters: map[string]any{"connection": record.Connection}}}))
	}
	if state.ProcessState == ProcessRunning && expected != nil && len(expected.Plan.HealthChecks) > 0 {
		state.Health = HealthHealthy
		for _, check := range expected.Plan.HealthChecks {
			probeCtx, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutMS)*time.Millisecond)
			connection, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", check.Address)
			cancel()
			if err == nil {
				_ = connection.Close()
				continue
			}
			state.Health = HealthUnhealthy
			state.Conditions = append(state.Conditions, stateCondition(record.Connection, "endpoint_unhealthy", "error",
				fmt.Sprintf("endpoint %s did not accept a TCP connection", check.Address),
				[]core.Action{{Type: "inspect_local_endpoint", Parameters: map[string]any{"address": check.Address}}}))
		}
	}
	return state
}

func stateCondition(connection, code, severity, message string, actions []core.Action) core.Condition {
	if actions == nil {
		actions = []core.Action{}
	}
	return core.Condition{Code: code, Severity: severity, Message: message, Connection: connection, Actions: actions}
}
