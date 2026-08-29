package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	"github.com/yuanyuanzijin/tunlite/internal/plan"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

type lifecycleObservationBudget struct {
	Overall      time.Duration
	PerItem      time.Duration
	PollInterval time.Duration
}

// Lifecycle observation is deliberately separate from the native mutation.
// The service manager has already accepted the operation when this budget is
// used, so exhausting it reports a truthful transient state instead of
// cancelling or rolling back an otherwise successful start.
func defaultLifecycleObservationBudget() lifecycleObservationBudget {
	return lifecycleObservationBudget{
		Overall: 10 * time.Second, PerItem: 3 * time.Second, PollInterval: 100 * time.Millisecond,
	}
}

type lifecycleObserver struct {
	deadline     time.Time
	perItem      time.Duration
	pollInterval time.Duration
}

type lifecycleObservationOutcome int

const (
	lifecycleConverged lifecycleObservationOutcome = iota
	lifecyclePending
	lifecycleFailed
)

func newLifecycleObserver(budget lifecycleObservationBudget) *lifecycleObserver {
	if budget.Overall <= 0 {
		budget.Overall = time.Second
	}
	if budget.PerItem <= 0 || budget.PerItem > budget.Overall {
		budget.PerItem = budget.Overall
	}
	if budget.PollInterval <= 0 {
		budget.PollInterval = 100 * time.Millisecond
	}
	return &lifecycleObserver{
		deadline: time.Now().Add(budget.Overall), perItem: budget.PerItem, pollInterval: budget.PollInterval,
	}
}

func lifecycleObserverFor(opts *options) *lifecycleObserver {
	if opts.lifecycleObserver != nil {
		return opts.lifecycleObserver
	}
	budget := defaultLifecycleObservationBudget()
	if opts.lifecycleBudget != nil {
		budget = *opts.lifecycleBudget
	}
	opts.lifecycleObserver = newLifecycleObserver(budget)
	return opts.lifecycleObserver
}

func (observer *lifecycleObserver) observe(
	ctx context.Context,
	adapter service.Adapter,
	record service.Record,
	artifact service.Artifact,
	operation string,
) (service.State, lifecycleObservationOutcome) {
	now := time.Now()
	itemDeadline := now.Add(observer.perItem)
	if observer.deadline.Before(itemDeadline) {
		itemDeadline = observer.deadline
	}
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(itemDeadline) {
		itemDeadline = deadline
	}
	if !itemDeadline.After(now) || ctx.Err() != nil {
		return lifecyclePendingState(adapter, record, artifact, operation, nil), lifecyclePending
	}

	itemCtx, cancel := context.WithDeadline(ctx, itemDeadline)
	defer cancel()
	var last *service.State
	for {
		state := service.Evaluate(itemCtx, adapter, record, &artifact)
		if itemCtx.Err() == nil {
			last = &state
		}
		if lifecycleStateConverged(state, artifact) {
			return state, lifecycleConverged
		}
		if itemCtx.Err() == nil && lifecycleStateFailed(state) {
			return state, lifecycleFailed
		}
		remaining := time.Until(itemDeadline)
		if remaining <= 0 || itemCtx.Err() != nil {
			return lifecycleObservationEnded(adapter, record, artifact, operation, last)
		}
		pause := observer.pollInterval
		if remaining < pause {
			pause = remaining
		}
		timer := time.NewTimer(pause)
		select {
		case <-itemCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return lifecycleObservationEnded(adapter, record, artifact, operation, last)
		case <-timer.C:
		}
	}
}

func lifecycleStateConverged(state service.State, artifact service.Artifact) bool {
	if state.ServiceState != service.ServiceRegistered ||
		state.ConsistencyState != service.ConsistencyConsistent ||
		state.ProcessState != service.ProcessRunning {
		return false
	}
	if len(artifact.Plan.HealthChecks) > 0 {
		return state.Health == service.HealthHealthy
	}
	return true
}

func lifecycleStateFailed(state service.State) bool {
	return state.ProcessState == service.ProcessFailed ||
		state.ServiceState == service.ServiceAbsent ||
		state.ConsistencyState == service.ConsistencyMissing ||
		state.ConsistencyState == service.ConsistencyBlocked
}

func lifecycleObservationEnded(
	adapter service.Adapter,
	record service.Record,
	artifact service.Artifact,
	operation string,
	last *service.State,
) (service.State, lifecycleObservationOutcome) {
	if last != nil && lifecycleStateFailed(*last) {
		return *last, lifecycleFailed
	}
	return lifecyclePendingState(adapter, record, artifact, operation, last), lifecyclePending
}

func lifecyclePendingState(
	adapter service.Adapter,
	record service.Record,
	artifact service.Artifact,
	operation string,
	last *service.State,
) service.State {
	pendingCondition := core.Condition{
		Code: "lifecycle_convergence_pending", Severity: "warning", Connection: record.Connection,
		Message: fmt.Sprintf("%s was accepted but the service has not reported healthy within the observation budget", operation),
		Actions: []core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{"connection": record.Connection}}},
	}
	if last != nil {
		pending := *last
		pending.Endpoints = append([]plan.Endpoint(nil), last.Endpoints...)
		pending.Conditions = append(append([]core.Condition(nil), last.Conditions...), pendingCondition)
		if last.Platform != nil {
			pending.Platform = make(map[string]any, len(last.Platform))
			for key, value := range last.Platform {
				pending.Platform[key] = value
			}
		}
		return pending
	}
	return service.State{
		Connection: record.Connection, Adapter: adapter.Name(), ServiceState: service.ServiceUnknown,
		Mode: record.Mode, ProcessState: service.ProcessUnknown, Health: service.HealthUnknown,
		ConsistencyState: service.ConsistencyBlocked, ObservedAt: time.Now().UTC(),
		Endpoints: artifact.Plan.Endpoints, Platform: map[string]any{},
		Conditions: []core.Condition{pendingCondition},
	}
}

func observeLifecycleResult(
	ctx context.Context,
	adapter service.Adapter,
	record service.Record,
	artifact service.Artifact,
	operation string,
	result serviceItemResult,
	opts options,
) serviceItemResult {
	state, outcome := lifecycleObserverFor(&opts).observe(ctx, adapter, record, artifact, operation)
	result.State = &state
	switch outcome {
	case lifecyclePending:
		result.Outcome = "starting"
	case lifecycleFailed:
		result.Outcome = "failed"
		result.Error = &serviceItemError{
			Code:      "service_" + operation + "_failed",
			Message:   fmt.Sprintf("%s was accepted but the service reported a failed process state", operation),
			RiskLevel: "low",
			Actions:   []core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{"connection": record.Connection}}},
		}
	}
	return result
}
