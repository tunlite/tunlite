package service

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

func Evaluate(ctx context.Context, adapter Adapter, record Record, expected *Artifact) State {
	state := State{
		Connection: record.Connection, Adapter: adapter.Name(), ServiceState: ServiceUnknown,
		Mode: record.Mode, ProcessState: ProcessUnknown, Health: HealthUnknown,
		ConsistencyState: ConsistencyConsistent, ObservedAt: time.Now().UTC(),
		Conditions: []core.Condition{}, Endpoints: []plan.Endpoint{},
	}
	if expected != nil {
		state.Endpoints = expected.Plan.Endpoints
	}
	if record.Adapter != "" && record.Adapter != adapter.Name() {
		state.ConsistencyState = ConsistencyBlocked
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_adapter_changed", "error",
			fmt.Sprintf("connection is bound to %s but this machine uses %s", record.Adapter, adapter.Name()), nil))
		return state
	}
	observation, inspectErr := adapter.Inspect(ctx, record.Reference())
	if observation.ProcessState != "" {
		state.ProcessState = observation.ProcessState
	}
	state.Platform = observation.Platform
	if state.Platform == nil {
		state.Platform = map[string]any{}
	}
	if inspectErr != nil {
		state.ConsistencyState = ConsistencyBlocked
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_manager_unavailable", "error",
			fmt.Sprintf("%s could not inspect the service: %v", adapter.Name(), inspectErr),
			[]core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{"adapter": adapter.Name()}}}))
		return state
	}
	if observation.Installed {
		state.ServiceState = ServiceRegistered
	} else {
		state.ServiceState = ServiceAbsent
		state.ConsistencyState = ConsistencyMissing
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_artifact_missing", "error",
			"the native service definition is missing",
			[]core.Action{{Type: "resolve_connection", Parameters: map[string]any{"connection": record.Connection, "use": "tunlite"}}}))
		return state
	}
	if record.BaselineDigest == "" {
		state.ConsistencyState = ConsistencyBlocked
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_baseline_missing", "warning",
			"the native service predates the current consistency baseline; choose which side to keep",
			[]core.Action{
				{Type: "resolve_connection", Parameters: map[string]any{"connection": record.Connection, "use": "tunlite"}},
				{Type: "resolve_connection", Parameters: map[string]any{"connection": record.Connection, "use": "system"}},
			}))
		return state
	}
	declarationChanged := expected != nil && record.PlanDigest != "" && expected.PlanDigest != record.PlanDigest
	systemChanged := record.BaselineDigest != "" && ContentDigest(observation.Artifact) != record.BaselineDigest
	if declarationChanged || systemChanged {
		state.ConsistencyState = ConsistencyDrifted
		message := "the native service definition differs from the last consistent baseline"
		if declarationChanged && !systemChanged {
			message = "the Connection declaration differs from the last applied execution plan"
		} else if declarationChanged && systemChanged {
			message = "both the Connection declaration and native service changed since the last consistent baseline"
		}
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_artifact_drifted", "warning",
			message,
			[]core.Action{
				{Type: "resolve_connection", Parameters: map[string]any{"connection": record.Connection, "use": "tunlite"}},
				{Type: "resolve_connection", Parameters: map[string]any{"connection": record.Connection, "use": "system"}},
			}))
	}
	observedMode := modeFromObservation(observation)
	if observedMode != record.Mode {
		state.ConsistencyState = ConsistencyDrifted
		state.Conditions = append(state.Conditions, stateCondition(record.Connection, "service_mode_drifted", "warning",
			fmt.Sprintf("native service mode is %s but Tunlite recorded %s", observedMode, record.Mode),
			[]core.Action{
				{Type: "resolve_connection", Parameters: map[string]any{"connection": record.Connection, "use": "tunlite"}},
				{Type: "resolve_connection", Parameters: map[string]any{"connection": record.Connection, "use": "system"}},
			}))
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

func modeFromObservation(observation Observation) string {
	if observation.AutoStart {
		return ModePersistent
	}
	if observation.ProcessState == ProcessRunning || observation.ProcessState == ProcessStarting || observation.ProcessState == ProcessFailed {
		return ModeBackground
	}
	return ModeStopped
}

func stateCondition(connection, code, severity, message string, actions []core.Action) core.Condition {
	if actions == nil {
		actions = []core.Action{}
	}
	return core.Condition{Code: code, Severity: severity, Message: message, Connection: connection, Actions: actions}
}
