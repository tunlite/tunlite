package cli

import (
	"context"
	"time"

	"github.com/tunlite/tunlite/internal/core"
	"github.com/tunlite/tunlite/internal/plan"
	"github.com/tunlite/tunlite/internal/service"
)

type statusObservationBudget struct {
	Overall    time.Duration
	PerItem    time.Duration
	Concurrent int
}

// These are implementation budgets, not public protocol promises. They keep
// one slow service manager call or endpoint probe from serially blocking a
// full status/monitor refresh and can be tuned from real platform benchmarks.
func defaultStatusObservationBudget() statusObservationBudget {
	return statusObservationBudget{
		Overall: 5 * time.Second, PerItem: 2 * time.Second, Concurrent: 8,
	}
}

type statusObservation struct {
	index  int
	result serviceItemResult
}

func collectStatusResults(
	ctx context.Context,
	adapter service.Adapter,
	records *service.Store,
	connections []core.Connection,
	declarationPath string,
	budget statusObservationBudget,
) []serviceItemResult {
	results := make([]serviceItemResult, len(connections))
	if len(connections) == 0 {
		return results
	}
	if budget.Concurrent < 1 {
		budget.Concurrent = 1
	}
	if budget.Concurrent > len(connections) {
		budget.Concurrent = len(connections)
	}
	if budget.Overall <= 0 {
		budget.Overall = time.Second
	}
	if budget.PerItem <= 0 || budget.PerItem > budget.Overall {
		budget.PerItem = budget.Overall
	}

	batchCtx, cancelBatch := context.WithTimeout(ctx, budget.Overall)
	defer cancelBatch()
	jobs := make(chan int)
	completed := make(chan statusObservation, len(connections))

	for worker := 0; worker < budget.Concurrent; worker++ {
		go func() {
			for index := range jobs {
				itemCtx, cancelItem := context.WithTimeout(batchCtx, budget.PerItem)
				result := statusService(itemCtx, adapter, records, connections[index], declarationPath)
				timedOut := itemCtx.Err() != nil
				cancelItem()
				if timedOut {
					result = statusObservationTimeout(connections[index].Metadata.Name)
				}
				completed <- statusObservation{index: index, result: result}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range connections {
			select {
			case jobs <- index:
			case <-batchCtx.Done():
				return
			}
		}
	}()

	done := make([]bool, len(connections))
	remaining := len(connections)
	for remaining > 0 {
		select {
		case observed := <-completed:
			if !done[observed.index] {
				results[observed.index] = observed.result
				done[observed.index] = true
				remaining--
			}
		case <-batchCtx.Done():
			for index, connection := range connections {
				if !done[index] {
					results[index] = statusObservationTimeout(connection.Metadata.Name)
				}
			}
			return results
		}
	}
	return results
}

func statusObservationTimeout(connection string) serviceItemResult {
	state := service.State{
		Connection: connection, Scope: service.ScopeUser, ServiceState: service.ServiceUnknown, Mode: service.ModeUnknown,
		ProcessState: service.ProcessUnknown, Health: service.HealthUnknown,
		ConsistencyState: service.ConsistencyBlocked, ObservedAt: time.Now().UTC(),
		Endpoints: []plan.Endpoint{}, Conditions: []core.Condition{{
			Code: "status_observation_timeout", Severity: "error", Connection: connection,
			Message: "status observation exceeded its time budget",
			Actions: []core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{}}},
		}},
	}
	return succeededServiceItem(connection, state)
}
