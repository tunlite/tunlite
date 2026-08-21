package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	appPaths "github.com/yuanyuanzijin/tunlite/internal/paths"
	"github.com/yuanyuanzijin/tunlite/internal/plan"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

type serviceSelection struct {
	target string
	tags   []string
	all    bool
}

type serviceItemError struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	RiskLevel string        `json:"riskLevel"`
	Actions   []core.Action `json:"actions"`
}

type serviceArtifactResult struct {
	Adapter    string `json:"adapter"`
	UnitName   string `json:"unitName"`
	Path       string `json:"path"`
	PlanDigest string `json:"planDigest"`
}

type serviceItemResult struct {
	Connection  string                 `json:"connection"`
	Outcome     string                 `json:"outcome"`
	Changed     bool                   `json:"changed"`
	State       *service.State         `json:"state,omitempty"`
	Artifact    *serviceArtifactResult `json:"artifact,omitempty"`
	Conditions  []core.Condition       `json:"conditions"`
	Error       *serviceItemError      `json:"error,omitempty"`
	Declaration *core.Connection       `json:"-"`
	Detailed    bool                   `json:"-"`
}

func serviceCommand(ctx context.Context, command string, args []string, streams IO, opts options) error {
	selection, err := parseServiceSelection(command, args)
	if err != nil {
		return err
	}
	adapter, records, config, document, err := serviceDependencies(opts)
	if err != nil {
		if command == "status" {
			return renderUnsupportedStatus(args, selection, streams, opts, err)
		}
		return capabilityError(err.Error())
	}
	targets, err := selectServiceConnections(document, selection, command == "status")
	if err != nil {
		return err
	}
	if command == "status" {
		results := collectStatusResults(ctx, adapter, records, targets, config.Path(), defaultStatusObservationBudget())
		for index := range results {
			declaration := cloneConnection(targets[index])
			results[index].Declaration = &declaration
			results[index].Detailed = selection.target != ""
		}
		return renderServiceResults(streams, opts, command, results)
	}
	results := make([]serviceItemResult, 0, len(targets))
	for _, connection := range targets {
		release := func() {}
		if !opts.DryRun {
			var lockErr error
			release, lockErr = records.Lock(ctx, connection.Metadata.Name)
			if lockErr != nil {
				results = append(results, failedServiceItem(connection.Metadata.Name, "service_busy", lockErr, "low", nil))
				continue
			}
		}
		if records.RunActive(connection.Metadata.Name) {
			release()
			results = append(results, failedServiceItem(connection.Metadata.Name, "foreground_running",
				errors.New("connection is running in the foreground; stop that run before changing its lifecycle"), "low", nil))
			continue
		}
		fresh, loadErr := config.Load()
		if loadErr != nil {
			release()
			results = append(results, failedServiceItem(connection.Metadata.Name, "declaration_read_failed", loadErr, "low", nil))
			continue
		}
		_, current := configstore.Find(&fresh, connection.Metadata.Name)
		if current == nil {
			release()
			results = append(results, failedServiceItem(connection.Metadata.Name, "not_found",
				errors.New("connection was removed before the service operation started"), "low", nil))
			continue
		}
		connection = cloneConnection(*current)
		var result serviceItemResult
		switch command {
		case "start":
			result = startService(ctx, adapter, records, connection, config.Path(), opts)
		case "stop":
			result = stopService(ctx, adapter, records, connection, config.Path(), opts)
		case "enable":
			result = enableService(ctx, adapter, records, connection, config.Path(), opts)
		case "disable":
			result = disableService(ctx, adapter, records, connection, config.Path(), opts)
		case "restart":
			result = restartService(ctx, adapter, records, connection, config.Path(), opts)
		}
		release()
		results = append(results, result)
	}
	return renderServiceResults(streams, opts, command, results)
}

func renderUnsupportedStatus(_ []string, selection serviceSelection, streams IO, opts options, cause error) error {
	config, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := config.Load()
	if err != nil {
		return err
	}
	targets, err := selectServiceConnections(document, selection, true)
	if err != nil {
		return err
	}
	results := make([]serviceItemResult, 0, len(targets))
	for _, connection := range targets {
		state := service.State{
			Connection: connection.Metadata.Name, ServiceState: service.ServiceUnsupported,
			Mode: service.ModeStopped, ProcessState: service.ProcessInactive, Health: service.HealthUnknown,
			ConsistencyState: service.ConsistencyBlocked, ObservedAt: time.Now().UTC(),
			Endpoints: []plan.Endpoint{}, Conditions: []core.Condition{{
				Code: "foreground_only", Severity: "warning", Message: cause.Error(), Connection: connection.Metadata.Name,
				Actions: []core.Action{},
			}},
		}
		if records, storeErr := serviceRecordStore(); storeErr == nil && records.RunActive(connection.Metadata.Name) {
			state.Mode, state.ProcessState = service.ModeForeground, service.ProcessRunning
		}
		result := succeededServiceItem(connection.Metadata.Name, state)
		declaration := cloneConnection(connection)
		result.Declaration = &declaration
		results = append(results, result)
	}
	return renderServiceResults(streams, opts, "status", results)
}

func parseServiceSelection(command string, args []string) (serviceSelection, error) {
	selection := serviceSelection{}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		selection.target = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var rawTags stringsFlag
	flags.Var(&rawTags, "tag", "select connections by tag")
	flags.BoolVar(&selection.all, "all", false, "select all connections")
	if err := flags.Parse(args); err != nil {
		return selection, usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return selection, usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	var err error
	selection.tags, err = parseTags(rawTags)
	if err != nil {
		return selection, err
	}
	selectors := 0
	if selection.target != "" {
		selectors++
	}
	if len(selection.tags) > 0 {
		selectors++
	}
	if selection.all {
		selectors++
	}
	if selectors > 1 {
		return selection, usageError("choose exactly one of a connection name, --tag, or --all")
	}
	if selectors == 0 && command != "status" {
		return selection, usageError(fmt.Sprintf("usage: tunlite %s <name> | --tag <tag> | --all", command))
	}
	return selection, nil
}

func serviceDependencies(opts options) (service.Adapter, *service.Store, *configstore.Store, configstore.Document, error) {
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return nil, nil, nil, configstore.Document{}, err
	}
	records, err := serviceRecordStore()
	if err != nil {
		return nil, nil, nil, configstore.Document{}, err
	}
	config, err := connectionStore(opts)
	if err != nil {
		return nil, nil, nil, configstore.Document{}, err
	}
	document, err := config.Load()
	if err != nil {
		return nil, nil, nil, configstore.Document{}, err
	}
	return adapter, records, config, document, nil
}

func serviceRecordStore() (*service.Store, error) {
	stateDir, err := appPaths.StateDir()
	if err != nil {
		return nil, err
	}
	return service.NewStore(filepath.Join(stateDir, "services")), nil
}

func selectServiceConnections(document configstore.Document, selection serviceSelection, defaultAll bool) ([]core.Connection, error) {
	if selection.target != "" {
		_, connection := configstore.Find(&document, selection.target)
		if connection == nil {
			return nil, notFoundError(fmt.Sprintf("no such connection: %s", selection.target))
		}
		return []core.Connection{cloneConnection(*connection)}, nil
	}
	if !selection.all && len(selection.tags) == 0 && !defaultAll {
		return nil, usageError("a connection selector is required")
	}
	results := make([]core.Connection, 0, len(document.Items))
	for _, connection := range document.Items {
		if len(selection.tags) == 0 || hasAllTags(connection.Metadata.Tags, selection.tags) {
			results = append(results, cloneConnection(connection))
		}
	}
	if len(results) == 0 && len(selection.tags) > 0 {
		return nil, notFoundError(fmt.Sprintf("no connections tagged %s", quoteList(selection.tags)))
	}
	return results, nil
}

func buildServiceArtifact(ctx context.Context, adapter service.Adapter, connection core.Connection) (service.Artifact, error) {
	return buildBoundServiceArtifact(ctx, adapter, connection, nil)
}

func buildBoundServiceArtifact(ctx context.Context, adapter service.Adapter, connection core.Connection, record *service.Record) (service.Artifact, error) {
	registry, err := core.NewRegistry(sshdriver.New())
	if err != nil {
		return service.Artifact{}, err
	}
	executionPlan, err := core.NewPlanner(registry).Plan(ctx, connection, core.PlanOptions{NonInteractive: true})
	if err != nil {
		return service.Artifact{}, err
	}
	if record != nil {
		return adapter.BuildBound(record.Reference(), executionPlan)
	}
	return adapter.Build(connection.Metadata.Name, executionPlan)
}

func loadServiceRecord(records *service.Store, connection core.Connection, declarationPath string) (service.Record, error) {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return service.Record{}, err
	}
	if !exists {
		return service.Record{}, errors.New("native service was not materialized; this connection is foreground-only")
	}
	if err := ensureDeclarationOwner(record, declarationPath); err != nil {
		return service.Record{}, err
	}
	return record, nil
}

func lifecycleRecordAndArtifact(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string) (service.Record, service.Artifact, bool, error) {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return service.Record{}, service.Artifact{}, false, err
	}
	if exists {
		if err := ensureDeclarationOwner(record, declarationPath); err != nil {
			return service.Record{}, service.Artifact{}, false, err
		}
		artifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &record)
		return record, artifact, false, err
	}
	artifact, err := buildServiceArtifact(ctx, adapter, connection)
	if err != nil {
		return service.Record{}, service.Artifact{}, false, err
	}
	return recordForArtifact(artifact, service.ModeStopped, declarationPath), artifact, true, nil
}

func materializeLifecycleRecord(ctx context.Context, adapter service.Adapter, records *service.Store, record service.Record, artifact service.Artifact, missing bool) error {
	if !missing {
		return nil
	}
	observation, err := adapter.Inspect(ctx, artifact.Reference())
	if err != nil {
		return err
	}
	if observation.Installed {
		return fmt.Errorf("native service %s already exists outside this declaration", artifact.UnitName)
	}
	if err := adapter.Materialize(ctx, artifact); err != nil {
		return err
	}
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return recoveryBlockedError(err, adapter.Remove(recoveryCtx, artifact.Reference()), nil)
	}
	return nil
}

func startService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, opts options) serviceItemResult {
	record, artifact, missing, err := lifecycleRecordAndArtifact(ctx, adapter, records, connection, declarationPath)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, "low", nil)
	}
	if record.Mode == service.ModePersistent {
		return failedServiceItem(connection.Metadata.Name, "persistent_connection", errors.New("connection is persistent; start cannot change it to temporary background mode"), "low", nil)
	}
	conditions := []core.Condition{}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	if !missing && before.ConsistencyState != service.ConsistencyConsistent {
		return failedServiceItem(connection.Metadata.Name, "connection_drifted", errors.New("connection has drifted; resolve it before starting"), "low", resolveActions(connection.Metadata.Name))
	}
	if record.Mode == service.ModeBackground && (before.ProcessState == service.ProcessRunning || before.ProcessState == service.ProcessStarting) {
		return succeededServiceItem(connection.Metadata.Name, before)
	}
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: conditions, Artifact: artifactResult(artifact)}
	if opts.DryRun {
		result.Outcome = "planned"
		result.State = &before
		return result
	}
	if err := materializeLifecycleRecord(ctx, adapter, records, record, artifact, missing); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_materialization_failed", err, "low", nil)
	}
	if err := adapter.Start(ctx, record.Reference()); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_start_failed", err,
			applyNativeMode(recoveryCtx, adapter, record.Reference(), record.Mode))
	}
	previous := record.Mode
	record.Mode = service.ModeBackground
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		rollbackErr := adapter.Stop(recoveryCtx, record.Reference())
		record.Mode = previous
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_state_write_failed", err, rollbackErr)
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	result.State = &state
	return result
}

func stopService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, opts options) serviceItemResult {
	record, err := loadServiceRecord(records, connection, declarationPath)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, "low", nil)
	}
	if record.Mode == service.ModePersistent {
		return failedServiceItem(connection.Metadata.Name, "persistent_connection", errors.New("connection is persistent; use disable to stop it and cancel automatic startup"), "low", nil)
	}
	artifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &record)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "execution_plan_failed", err, "low", nil)
	}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	if record.Mode == service.ModeStopped && before.ProcessState == service.ProcessInactive {
		return succeededServiceItem(connection.Metadata.Name, before)
	}
	if before.ConsistencyState != service.ConsistencyConsistent {
		return failedServiceItem(connection.Metadata.Name, "connection_drifted", errors.New("connection has drifted; resolve it before stopping"), "low", resolveActions(connection.Metadata.Name))
	}
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: []core.Condition{}, Artifact: artifactResult(artifact)}
	if opts.DryRun {
		result.Outcome = "planned"
		result.State = &before
		return result
	}
	if err := adapter.Stop(ctx, record.Reference()); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_stop_failed", err,
			applyNativeMode(recoveryCtx, adapter, record.Reference(), record.Mode))
	}
	record.Mode = service.ModeStopped
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_state_write_failed", err,
			adapter.Start(recoveryCtx, record.Reference()))
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	result.State = &state
	return result
}

func enableService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, opts options) serviceItemResult {
	record, artifact, missing, err := lifecycleRecordAndArtifact(ctx, adapter, records, connection, declarationPath)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, "low", nil)
	}
	conditions := []core.Condition{}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	if !missing && before.ConsistencyState != service.ConsistencyConsistent {
		return failedServiceItem(connection.Metadata.Name, "connection_drifted", errors.New("connection has drifted; resolve it before enabling"), "low", resolveActions(connection.Metadata.Name))
	}
	if record.Mode == service.ModePersistent && (before.ProcessState == service.ProcessRunning || before.ProcessState == service.ProcessStarting) {
		return succeededServiceItem(connection.Metadata.Name, before)
	}
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: conditions, Artifact: artifactResult(artifact)}
	if opts.DryRun {
		result.Outcome = "planned"
		result.State = &before
		return result
	}
	if err := materializeLifecycleRecord(ctx, adapter, records, record, artifact, missing); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_materialization_failed", err, "low", nil)
	}
	previousMode := record.Mode
	if err := adapter.Enable(ctx, record.Reference()); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_enable_failed", err,
			applyNativeMode(recoveryCtx, adapter, record.Reference(), previousMode))
	}
	record.Mode = service.ModePersistent
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		disableErr := adapter.Disable(recoveryCtx, record.Reference())
		var startErr error
		if previousMode == service.ModeBackground {
			startErr = adapter.Start(recoveryCtx, record.Reference())
		}
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_state_write_failed", err,
			errors.Join(disableErr, startErr))
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	result.State = &state
	return result
}

func disableService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, opts options) serviceItemResult {
	record, err := loadServiceRecord(records, connection, declarationPath)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, "low", nil)
	}
	artifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &record)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "execution_plan_failed", err, "low", nil)
	}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	if record.Mode == service.ModeStopped && before.ProcessState == service.ProcessInactive {
		return succeededServiceItem(connection.Metadata.Name, before)
	}
	if record.Mode == service.ModeBackground {
		return failedServiceItem(connection.Metadata.Name, "background_connection", errors.New("connection is in background mode; use stop"), "low", nil)
	}
	if before.ConsistencyState != service.ConsistencyConsistent {
		return failedServiceItem(connection.Metadata.Name, "connection_drifted", errors.New("connection has drifted; resolve it before disabling"), "low", resolveActions(connection.Metadata.Name))
	}
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: []core.Condition{}, Artifact: artifactResult(artifact)}
	if opts.DryRun {
		result.Outcome = "planned"
		result.State = &before
		return result
	}
	if err := adapter.Disable(ctx, record.Reference()); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_disable_failed", err,
			applyNativeMode(recoveryCtx, adapter, record.Reference(), record.Mode))
	}
	record.Mode = service.ModeStopped
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_state_write_failed", err,
			adapter.Enable(recoveryCtx, record.Reference()))
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	result.State = &state
	return result
}

func restartService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, opts options) serviceItemResult {
	record, err := loadServiceRecord(records, connection, declarationPath)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, "low", nil)
	}
	if record.Mode != service.ModeBackground && record.Mode != service.ModePersistent {
		return failedServiceItem(connection.Metadata.Name, "connection_stopped", errors.New("connection is stopped; use start or enable instead of restart"), "low", nil)
	}
	artifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &record)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "execution_plan_failed", err, "low", nil)
	}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	if before.ConsistencyState != service.ConsistencyConsistent {
		return failedServiceItem(connection.Metadata.Name, "connection_drifted", errors.New("connection has drifted; resolve it before restarting"), "low", resolveActions(connection.Metadata.Name))
	}
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: []core.Condition{}, Artifact: artifactResult(artifact)}
	if opts.DryRun {
		result.Outcome = "planned"
		result.State = &before
		return result
	}
	if err := adapter.Restart(ctx, record.Reference()); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_restart_failed", err,
			applyNativeMode(recoveryCtx, adapter, record.Reference(), record.Mode))
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	result.State = &state
	return result
}

func statusService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string) serviceItemResult {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, "low", nil)
	}
	if !exists {
		artifact, planErr := buildServiceArtifact(ctx, adapter, connection)
		consistency := service.ConsistencyMissing
		serviceState := service.ServiceAbsent
		mode := service.ModeStopped
		processState := service.ProcessInactive
		statusCondition := core.Condition{
			Code: "service_missing", Severity: "warning", Message: "native service has not been materialized on this machine",
			Connection: connection.Metadata.Name, Actions: []core.Action{{Type: "start_connection", Parameters: map[string]any{"connection": connection.Metadata.Name}}},
		}
		if planErr != nil {
			consistency = service.ConsistencyBlocked
			statusCondition = core.Condition{
				Code: "execution_plan_blocked", Severity: "error", Message: planErr.Error(), Connection: connection.Metadata.Name,
				Driver: connection.Spec.Driver, Actions: []core.Action{},
			}
		} else if observation, inspectErr := adapter.Inspect(ctx, artifact.Reference()); inspectErr != nil {
			consistency = service.ConsistencyBlocked
			serviceState = service.ServiceUnknown
			processState = service.ProcessUnknown
			statusCondition = core.Condition{
				Code: "service_manager_unavailable", Severity: "error", Message: inspectErr.Error(), Connection: connection.Metadata.Name,
				Actions: []core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{"adapter": adapter.Name()}}},
			}
		} else if observation.Installed {
			consistency = service.ConsistencyBlocked
			serviceState = service.ServiceRegistered
			mode = observedMode(observation)
			processState = observation.ProcessState
			statusCondition = core.Condition{
				Code: "service_binding_missing", Severity: "error",
				Message:    "a native service exists at the expected identity but Tunlite has no management binding",
				Connection: connection.Metadata.Name,
				Actions: []core.Action{
					{Type: "resolve_connection", Parameters: map[string]any{"connection": connection.Metadata.Name, "use": "tunlite"}},
					{Type: "resolve_connection", Parameters: map[string]any{"connection": connection.Metadata.Name, "use": "system"}},
				},
			}
		}
		state := service.State{
			Connection: connection.Metadata.Name, Adapter: adapter.Name(), ServiceState: serviceState,
			Mode: mode, ProcessState: processState, Health: service.HealthUnknown,
			ConsistencyState: consistency, ObservedAt: time.Now().UTC(),
			Endpoints: []plan.Endpoint{}, Conditions: []core.Condition{statusCondition},
		}
		if records.RunActive(connection.Metadata.Name) {
			state.Mode, state.ProcessState = service.ModeForeground, service.ProcessRunning
		}
		return succeededServiceItem(connection.Metadata.Name, state)
	}
	if err := ensureDeclarationOwner(record, declarationPath); err != nil {
		return failedServiceItem(connection.Metadata.Name, "declaration_conflict", err, "low", nil)
	}
	artifact, planErr := buildBoundServiceArtifact(ctx, adapter, connection, &record)
	var expected *service.Artifact
	if planErr == nil {
		expected = &artifact
	}
	state := service.Evaluate(ctx, adapter, record, expected)
	if records.RunActive(connection.Metadata.Name) {
		state.Mode, state.ProcessState = service.ModeForeground, service.ProcessRunning
	}
	if planErr != nil {
		state.ConsistencyState = service.ConsistencyBlocked
		state.Conditions = append(state.Conditions, core.Condition{
			Code: "execution_plan_blocked", Severity: "error", Message: planErr.Error(), Connection: connection.Metadata.Name,
			Driver: connection.Spec.Driver, Actions: []core.Action{},
		})
	}
	return serviceItemResult{Connection: connection.Metadata.Name, Outcome: "succeeded", Changed: false, State: &state, Conditions: []core.Condition{}}
}

func planRiskConditions(connection core.Connection, executionPlan plan.ExecutionPlan) []core.Condition {
	var reasons []string
	for _, endpoint := range executionPlan.Endpoints {
		if endpoint.Direction == "publish" {
			reasons = append(reasons, "publishes a remote endpoint")
			continue
		}
		host, _, err := net.SplitHostPort(endpoint.Address)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			reasons = append(reasons, "listens outside the loopback interface")
		}
	}
	if len(reasons) == 0 {
		return []core.Condition{}
	}
	sort.Strings(reasons)
	reasons = compactStrings(reasons)
	return []core.Condition{{
		Code: "network_exposure", Severity: "warning", Connection: connection.Metadata.Name, Driver: connection.Spec.Driver,
		Message: "connection " + strings.Join(reasons, " and "),
		Actions: []core.Action{},
	}}
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func recordForArtifact(artifact service.Artifact, mode, declarationPath string) service.Record {
	return service.Record{
		RecordRevision: service.CurrentRecordRevision, Connection: artifact.Connection, ServiceKey: artifact.Connection, Adapter: artifact.Adapter,
		Mode: mode, ArtifactPath: artifact.Path, UnitName: artifact.UnitName,
		LogPaths: append([]string(nil), artifact.LogPaths...), PlanDigest: artifact.PlanDigest,
		BaselineDigest: service.ContentDigest(artifact.Content), DeclarationPath: declarationPath, Origin: "tunlite",
	}
}

func ensureDeclarationOwner(record service.Record, declarationPath string) error {
	if record.DeclarationPath != "" && record.DeclarationPath != declarationPath {
		return fmt.Errorf("connection name %q is already owned by declarations at %s", record.Connection, record.DeclarationPath)
	}
	return nil
}

func resolveActions(connection string) []core.Action {
	return []core.Action{
		{Type: "resolve_connection", Parameters: map[string]any{"connection": connection, "use": "tunlite"}},
		{Type: "resolve_connection", Parameters: map[string]any{"connection": connection, "use": "system"}},
	}
}

func artifactResult(artifact service.Artifact) *serviceArtifactResult {
	return &serviceArtifactResult{Adapter: artifact.Adapter, UnitName: artifact.UnitName, Path: artifact.Path, PlanDigest: artifact.PlanDigest}
}

func succeededServiceItem(connection string, state service.State) serviceItemResult {
	return serviceItemResult{Connection: connection, Outcome: "succeeded", Changed: false, State: &state, Conditions: []core.Condition{}}
}

func failedServiceItem(connection, code string, err error, risk string, actions []core.Action) serviceItemResult {
	if actions == nil {
		actions = []core.Action{}
	}
	return serviceItemResult{
		Connection: connection, Outcome: "failed", Changed: false, Conditions: []core.Condition{},
		Error: &serviceItemError{Code: code, Message: err.Error(), RiskLevel: risk, Actions: actions},
	}
}

func failedServiceItemAfterRecovery(connection, code string, cause, recovery error) serviceItemResult {
	if recovery == nil {
		return failedServiceItem(connection, code, cause, "low", nil)
	}
	return failedServiceItem(connection, "recovery_blocked",
		errors.Join(cause, fmt.Errorf("automatic recovery failed: %w", recovery)), "high", resolveActions(connection))
}

func renderServiceResults(streams IO, opts options, operation string, results []serviceItemResult) error {
	failed, succeeded := 0, 0
	for _, result := range results {
		if result.Outcome == "failed" {
			failed++
		} else {
			succeeded++
		}
	}
	outcome := "succeeded"
	if opts.DryRun && operation != "status" {
		outcome = "planned"
	}
	if failed > 0 {
		if succeeded > 0 {
			outcome = "partial"
		} else {
			outcome = "failed"
		}
	}
	if opts.JSON {
		if err := writeResponse(streams.Out, map[string]any{
			"outcome": outcome, "operation": operation,
			"results": results, "dryRun": opts.DryRun && operation != "status",
		}); err != nil {
			return err
		}
	} else if operation == "status" {
		rows := sortedStatusRows(results)
		detailed := false
		for _, result := range results {
			detailed = detailed || result.Detailed
			if result.Error != nil {
				fmt.Fprintf(streams.Err, "%s: failed: %s\n", result.Connection, result.Error.Message)
			}
		}
		if len(rows) == 1 && detailed {
			for _, line := range renderStatusDetail(rows[0], outputUsesColor(streams, opts), opts.Language) {
				fmt.Fprintln(streams.Out, line)
			}
		} else {
			for _, line := range renderStatusTable(rows, outputUsesColor(streams, opts), opts.Language) {
				fmt.Fprintln(streams.Out, line)
			}
		}
	} else {
		for _, result := range results {
			if result.Error != nil {
				fmt.Fprintf(streams.Err, "%s: failed: %s\n", result.Connection, result.Error.Message)
				continue
			}
			if result.State != nil {
				row := statusRow{state: *result.State}
				style := displayStatusRow(row, opts.Language)
				if isChinese(opts.Language) {
					fmt.Fprintf(streams.Out, "%s  %s  运行位置：%s  登录自启：%s\n",
						result.Connection, style.glyph+" "+style.label,
						statusRunsAs(row, opts.Language), statusOnLogin(row, opts.Language))
				} else {
					fmt.Fprintf(streams.Out, "%s  %s  runs as: %s  on login: %s\n",
						result.Connection, style.glyph+" "+style.label,
						statusRunsAs(row, opts.Language), statusOnLogin(row, opts.Language))
				}
			} else {
				fmt.Fprintf(streams.Out, "%s: %s\n", result.Connection, result.Outcome)
			}
		}
	}
	if failed > 0 && succeeded > 0 {
		return renderedExit(exitPartial)
	}
	if failed > 0 {
		return renderedExit(1)
	}
	return nil
}

func serviceLogs(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite logs <name> [--lines N] [--follow]")
	}
	name := args[0]
	flags := flag.NewFlagSet("logs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	lines := 100
	var follow bool
	flags.IntVar(&lines, "lines", 100, "number of recent log lines")
	flags.BoolVar(&follow, "follow", false, "follow logs")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 || lines < 1 || lines > 100000 {
		return usageError("logs requires --lines between 1 and 100000")
	}
	adapter, records, config, document, err := serviceDependencies(opts)
	if err != nil {
		return capabilityError(err.Error())
	}
	_, connection := configstore.Find(&document, name)
	if connection == nil {
		return notFoundError(fmt.Sprintf("no such connection: %s", name))
	}
	record, err := loadServiceRecord(records, *connection, config.Path())
	if err != nil {
		return capabilityError(err.Error())
	}
	if opts.JSON && follow {
		var lock sync.Mutex
		stdout := &ndjsonLogWriter{destination: streams.Out, connection: name, stream: "stdout", lock: &lock}
		stderr := &ndjsonLogWriter{destination: streams.Out, connection: name, stream: "stderr", lock: &lock}
		err := adapter.Logs(ctx, record.Reference(), lines, true, service.Streams{Out: stdout, Err: stderr})
		stdout.Flush()
		stderr.Flush()
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if !opts.JSON {
		return adapter.Logs(ctx, record.Reference(), lines, follow, service.Streams{Out: streams.Out, Err: streams.Err})
	}
	var stdout, stderr bytes.Buffer
	if err := adapter.Logs(ctx, record.Reference(), lines, false, service.Streams{Out: &stdout, Err: &stderr}); err != nil {
		return err
	}
	logs := append(splitLogLines(stdout.String()), splitLogLines(stderr.String())...)
	return writeResponse(streams.Out, map[string]any{
		"outcome": "succeeded", "connection": name, "logs": logs,
	})
}

type ndjsonLogWriter struct {
	destination io.Writer
	connection  string
	stream      string
	lock        *sync.Mutex
	pending     []byte
}

func (writer *ndjsonLogWriter) Write(value []byte) (int, error) {
	writer.lock.Lock()
	defer writer.lock.Unlock()
	writer.pending = append(writer.pending, value...)
	for {
		index := bytes.IndexByte(writer.pending, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSuffix(string(writer.pending[:index]), "\r")
		writer.pending = writer.pending[index+1:]
		if err := writer.emit(line); err != nil {
			return 0, err
		}
	}
	return len(value), nil
}

func (writer *ndjsonLogWriter) Flush() {
	writer.lock.Lock()
	defer writer.lock.Unlock()
	if len(writer.pending) == 0 {
		return
	}
	_ = writer.emit(strings.TrimSuffix(string(writer.pending), "\r"))
	writer.pending = nil
}

func (writer *ndjsonLogWriter) emit(line string) error {
	return writeResponse(writer.destination, map[string]any{
		"outcome": "log", "connection": writer.connection, "stream": writer.stream,
		"line": line, "observedAt": time.Now().UTC(),
	})
}

func splitLogLines(value string) []string {
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return []string{}
	}
	return strings.Split(value, "\n")
}
