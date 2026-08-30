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
	target   string
	group    string
	all      bool
	scope    service.Scope
	scopeSet bool
}

type serviceScopeFlag struct {
	value service.Scope
	set   bool
}

func (f *serviceScopeFlag) String() string { return string(f.value) }

func (f *serviceScopeFlag) Set(value string) error {
	if f.set {
		return errors.New("--scope may only be specified once")
	}
	f.set = true
	switch service.Scope(value) {
	case service.ScopeUser, service.ScopeSystem:
		f.value = service.Scope(value)
		return nil
	default:
		return fmt.Errorf("unsupported scope %q; expected user or system", value)
	}
}

type serviceItemError struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	RiskLevel string        `json:"riskLevel"`
	Actions   []core.Action `json:"actions"`
}

type serviceArtifactResult struct {
	Adapter    string        `json:"adapter"`
	Scope      service.Scope `json:"scope"`
	UnitName   string        `json:"unitName"`
	Path       string        `json:"path"`
	PlanDigest string        `json:"planDigest"`
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

type scopePreflightOperation struct {
	requiresAdministrator bool
	from                  string
	to                    service.Scope
	preflight             func() error
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
		renderCommandProgress(streams, opts, append([]string{"status"}, args...))
		results := collectStatusResults(ctx, adapter, records, targets, config.Path(), defaultStatusObservationBudget())
		for index := range results {
			declaration := cloneConnection(targets[index])
			results[index].Declaration = &declaration
			results[index].Detailed = selection.target != ""
		}
		return renderServiceResults(streams, opts, command, results)
	}
	if command == "enable" && selection.scopeSet {
		if !service.SupportsScope(adapter, selection.scope) {
			unsupported := &service.UnsupportedScopeError{Adapter: adapter.Name(), Scope: selection.scope}
			results := make([]serviceItemResult, 0, len(targets))
			for _, connection := range targets {
				results = append(results, failedServiceItem(connection.Metadata.Name, unsupported.Code(), unsupported, "low", nil))
			}
			return renderServiceResults(streams, opts, command, results)
		}
		var preflightErr error
		preflights := make([]scopePreflightOperation, 0, len(targets))
		for _, connection := range targets {
			if records.RunActive(connection.Metadata.Name) {
				preflightErr = errors.New("connection is running in the foreground; stop that run before changing its service scope")
				break
			}
			record, exists, loadErr := records.Load(connection.Metadata.Name)
			if loadErr != nil {
				preflightErr = loadErr
				break
			}
			if !exists {
				targetArtifact, buildErr := buildServiceArtifactScoped(ctx, adapter, connection, selection.scope)
				if buildErr != nil {
					preflightErr = buildErr
					break
				}
				if preflightErr = unboundNativeIdentityConflict(ctx, adapter, targetArtifact); preflightErr == nil {
					preflightErr = service.CheckScopeInstall(ctx, adapter, targetArtifact)
				}
				if preflightErr == nil {
					artifact := targetArtifact
					preflights = append(preflights, scopePreflightOperation{
						requiresAdministrator: service.NormalizeScope(artifact.Scope) == service.ScopeSystem,
						from:                  "absent",
						to:                    service.NormalizeScope(artifact.Scope),
						preflight:             func() error { return service.PreflightScopeInstall(ctx, adapter, artifact) },
					})
				}
			} else {
				if ownerErr := ensureDeclarationOwner(record, config.Path()); ownerErr != nil {
					preflightErr = ownerErr
					break
				}
				sourceArtifact, buildErr := buildBoundServiceArtifact(ctx, adapter, connection, &record)
				if buildErr != nil {
					preflightErr = buildErr
					break
				}
				if sourceState := service.Evaluate(ctx, adapter, record, &sourceArtifact); sourceState.ConsistencyState != service.ConsistencyConsistent {
					preflightErr = errors.New("connection has drifted; resolve it before changing service scope")
					break
				}
				if service.NormalizeScope(record.Scope) == selection.scope {
					continue
				}
				targetArtifact, buildErr := buildScopeTransitionArtifact(ctx, adapter, connection, record, selection.scope)
				if buildErr != nil {
					preflightErr = buildErr
					break
				}
				preflightErr = service.CheckScopeTransition(ctx, adapter, record.Reference(), targetArtifact)
				if preflightErr == nil {
					source := record.Reference()
					artifact := targetArtifact
					preflights = append(preflights, scopePreflightOperation{
						requiresAdministrator: service.NormalizeScope(source.Scope) == service.ScopeSystem ||
							service.NormalizeScope(artifact.Scope) == service.ScopeSystem,
						from:      string(service.NormalizeScope(source.Scope)),
						to:        service.NormalizeScope(artifact.Scope),
						preflight: func() error { return service.PreflightScopeTransition(ctx, adapter, source, artifact) },
					})
				}
			}
			if preflightErr != nil {
				break
			}
		}
		if preflightErr == nil && !opts.DryRun {
			requiresAdministrator := false
			request := privilegeRequest{}
			for _, operation := range preflights {
				requiresAdministrator = requiresAdministrator || operation.requiresAdministrator
				if !operation.requiresAdministrator {
					continue
				}
				request.connections++
				switch {
				case operation.from == string(service.ScopeUser) && operation.to == service.ScopeSystem:
					request.userToSystem++
				case operation.from == string(service.ScopeSystem) && operation.to == service.ScopeUser:
					request.systemToUser++
				case operation.from == "absent" && operation.to == service.ScopeSystem:
					request.installsToSystem++
				}
			}
			if requiresAdministrator && !opts.NonInteractive {
				preflightErr = opts.privilegeAuthorizer.Validate(ctx, streams, opts.Language, request)
			}
			if preflightErr == nil {
				for _, operation := range preflights {
					if preflightErr = operation.preflight(); preflightErr != nil {
						break
					}
				}
			}
		}
		if preflightErr != nil {
			results := make([]serviceItemResult, 0, len(targets))
			for _, connection := range targets {
				results = append(results, failedServiceOperation(connection.Metadata.Name, preflightErr, "scope_transition_failed"))
			}
			return renderServiceResults(streams, opts, command, results)
		}
	}
	if command == "enable" {
		preflightErr := preflightSystemReenable(ctx, adapter, records, targets, config.Path(), streams, opts)
		if preflightErr != nil {
			results := make([]serviceItemResult, 0, len(targets))
			for _, connection := range targets {
				results = append(results, failedServiceOperation(connection.Metadata.Name, preflightErr, "scope_transition_failed"))
			}
			return renderServiceResults(streams, opts, command, results)
		}
	}
	if command == "disable" {
		preflightErr := preflightSystemDisable(ctx, adapter, records, targets, config.Path(), streams, opts)
		if preflightErr != nil {
			results := make([]serviceItemResult, 0, len(targets))
			for _, connection := range targets {
				results = append(results, failedServiceOperation(connection.Metadata.Name, preflightErr, "service_disable_failed"))
			}
			return renderServiceResults(streams, opts, command, results)
		}
	}
	renderCommandProgress(streams, opts, append([]string{command}, args...))
	if command == "start" || command == "enable" || command == "restart" {
		lifecycleObserverFor(&opts)
	}
	results := make([]serviceItemResult, 0, len(targets))
	for index, connection := range targets {
		batchCurrent, total := index+1, len(targets)
		renderBatchProgress(streams, opts, batchCurrent, total,
			fmt.Sprintf("%s %q…", strings.ToUpper(command[:1])+command[1:], connection.Metadata.Name),
			fmt.Sprintf("正在对“%s”执行 %s…", connection.Metadata.Name, command))
		completeProgress := func(outcome string) {
			chineseOutcome := map[string]string{
				"applied": "已完成", "planned": "预演完成", "starting": "启动中", "failed": "失败", "succeeded": "已完成",
			}[outcome]
			if chineseOutcome == "" {
				chineseOutcome = outcome
			}
			renderBatchProgress(streams, opts, batchCurrent, total,
				fmt.Sprintf("%q %s.", connection.Metadata.Name, outcome),
				fmt.Sprintf("“%s”%s。", connection.Metadata.Name, chineseOutcome))
		}
		release := func() {}
		if !opts.DryRun {
			var lockErr error
			release, lockErr = records.Lock(ctx, connection.Metadata.Name)
			if lockErr != nil {
				results = append(results, failedServiceItem(connection.Metadata.Name, "service_busy", lockErr, "low", nil))
				completeProgress("failed")
				continue
			}
		}
		if records.RunActive(connection.Metadata.Name) {
			release()
			results = append(results, failedServiceItem(connection.Metadata.Name, "foreground_running",
				errors.New("connection is running in the foreground; stop that run before changing its lifecycle"), "low", nil))
			completeProgress("failed")
			continue
		}
		fresh, loadErr := config.Load()
		if loadErr != nil {
			release()
			results = append(results, failedServiceItem(connection.Metadata.Name, "declaration_read_failed", loadErr, "low", nil))
			completeProgress("failed")
			continue
		}
		_, current := configstore.Find(&fresh, connection.Metadata.Name)
		if current == nil {
			release()
			results = append(results, failedServiceItem(connection.Metadata.Name, "not_found",
				errors.New("connection was removed before the service operation started"), "low", nil))
			completeProgress("failed")
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
			result = enableService(ctx, adapter, records, connection, config.Path(), selection.scope, selection.scopeSet, streams, opts)
		case "disable":
			result = disableService(ctx, adapter, records, connection, config.Path(), opts)
		case "restart":
			result = restartService(ctx, adapter, records, connection, config.Path(), opts)
		}
		release()
		results = append(results, result)
		completeProgress(result.Outcome)
	}
	return renderServiceResults(streams, opts, command, results)
}

func preflightSystemReenable(
	ctx context.Context,
	adapter service.Adapter,
	records *service.Store,
	targets []core.Connection,
	declarationPath string,
	streams IO,
	opts options,
) error {
	artifacts := make([]service.Artifact, 0)
	for _, connection := range targets {
		record, exists, err := records.Load(connection.Metadata.Name)
		if err != nil {
			return err
		}
		if !exists || service.NormalizeScope(record.Scope) != service.ScopeSystem || record.Mode != service.ModeStopped {
			continue
		}
		if err := ensureDeclarationOwner(record, declarationPath); err != nil {
			return err
		}
		artifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &record)
		if err != nil {
			return err
		}
		observation, err := adapter.Inspect(ctx, record.Reference())
		if err != nil {
			return err
		}
		if observation.Installed {
			continue
		}
		if err := service.CheckScopeInstall(ctx, adapter, artifact); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
	}
	if len(artifacts) == 0 || opts.DryRun {
		return nil
	}
	if !opts.NonInteractive {
		if err := opts.privilegeAuthorizer.Validate(ctx, streams, opts.Language, privilegeRequest{
			connections: len(artifacts), installsToSystem: len(artifacts),
		}); err != nil {
			return err
		}
	}
	for _, artifact := range artifacts {
		if err := service.PreflightScopeInstall(ctx, adapter, artifact); err != nil {
			return err
		}
	}
	return nil
}

func preflightSystemDisable(
	ctx context.Context,
	adapter service.Adapter,
	records *service.Store,
	targets []core.Connection,
	declarationPath string,
	streams IO,
	opts options,
) error {
	references := make([]service.Reference, 0)
	for _, connection := range targets {
		record, exists, err := records.Load(connection.Metadata.Name)
		if err != nil {
			return err
		}
		if !exists || service.NormalizeScope(record.Scope) != service.ScopeSystem {
			continue
		}
		if err := ensureRecordScopeSupported(adapter, record); err != nil {
			return err
		}
		if err := ensureDeclarationOwner(record, declarationPath); err != nil {
			return err
		}
		if record.Mode == service.ModePersistent {
			references = append(references, record.Reference())
			continue
		}
		observation, err := adapter.Inspect(ctx, record.Reference())
		if err != nil {
			return err
		}
		if observation.Installed || observation.AutoStart || observation.ProcessState != service.ProcessInactive {
			references = append(references, record.Reference())
		}
	}
	if len(references) == 0 || opts.DryRun {
		return nil
	}
	if !opts.NonInteractive {
		if err := opts.privilegeAuthorizer.Validate(ctx, streams, opts.Language, privilegeRequest{connections: len(references)}); err != nil {
			return err
		}
	}
	for _, reference := range references {
		if err := service.PreflightDisable(ctx, adapter, reference); err != nil {
			return err
		}
	}
	return nil
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
			Connection: connection.Metadata.Name, Scope: service.ScopeUser, ServiceState: service.ServiceUnsupported,
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
	scopeFlag := serviceScopeFlag{}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		selection.target = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&selection.group, "group", "", "select connections by group")
	flags.BoolVar(&selection.all, "all", false, "select all connections")
	flags.Var(&scopeFlag, "scope", "native service scope: user or system")
	if err := flags.Parse(args); err != nil {
		return selection, usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return selection, usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	groupSet := false
	flags.Visit(func(item *flag.Flag) {
		groupSet = groupSet || item.Name == "group"
	})
	selection.scope, selection.scopeSet = scopeFlag.value, scopeFlag.set
	if selection.scopeSet && command != "enable" {
		return selection, usageError("--scope is only valid with enable")
	}
	if groupSet {
		var err error
		selection.group, err = parseGroup(selection.group)
		if err != nil {
			return selection, err
		}
	}
	selectors := 0
	if selection.target != "" {
		selectors++
	}
	if groupSet {
		selectors++
	}
	if selection.all {
		selectors++
	}
	if selectors > 1 {
		return selection, usageError("choose exactly one of a connection name, --group, or --all")
	}
	if selectors == 0 && command != "status" {
		return selection, usageError(commandUsage(command))
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
	if !selection.all && selection.group == "" && !defaultAll {
		return nil, usageError("a connection selector is required")
	}
	results := make([]core.Connection, 0, len(document.Items))
	for _, connection := range document.Items {
		if selection.group == "" || connection.Metadata.Group == selection.group {
			results = append(results, cloneConnection(connection))
		}
	}
	if len(results) == 0 && selection.group != "" {
		return nil, notFoundError(fmt.Sprintf("no connections in group %q", selection.group))
	}
	return results, nil
}

func buildServiceArtifact(ctx context.Context, adapter service.Adapter, connection core.Connection) (service.Artifact, error) {
	return buildServiceArtifactScoped(ctx, adapter, connection, service.ScopeUser)
}

func serviceExecutionPlan(ctx context.Context, connection core.Connection) (plan.ExecutionPlan, error) {
	registry, err := core.NewRegistry(sshdriver.New())
	if err != nil {
		return plan.ExecutionPlan{}, err
	}
	executionPlan, err := core.NewPlanner(registry).Plan(ctx, connection, core.PlanOptions{NonInteractive: true})
	if err != nil {
		return plan.ExecutionPlan{}, err
	}
	return executionPlan, nil
}

func buildServiceArtifactScoped(ctx context.Context, adapter service.Adapter, connection core.Connection, scope service.Scope) (service.Artifact, error) {
	executionPlan, err := serviceExecutionPlan(ctx, connection)
	if err != nil {
		return service.Artifact{}, err
	}
	return service.BuildScoped(adapter, scope, connection.Metadata.Name, executionPlan)
}

func buildBoundServiceArtifact(ctx context.Context, adapter service.Adapter, connection core.Connection, record *service.Record) (service.Artifact, error) {
	if record != nil {
		if err := ensureRecordScopeSupported(adapter, *record); err != nil {
			return service.Artifact{}, err
		}
	}
	executionPlan, err := serviceExecutionPlan(ctx, connection)
	if err != nil {
		return service.Artifact{}, err
	}
	if record != nil {
		return adapter.BuildBound(record.Reference(), executionPlan)
	}
	return service.BuildScoped(adapter, service.ScopeUser, connection.Metadata.Name, executionPlan)
}

func ensureRecordScopeSupported(adapter service.Adapter, record service.Record) error {
	scope := service.NormalizeScope(record.Scope)
	if !service.SupportsScope(adapter, scope) {
		return &service.UnsupportedScopeError{Adapter: adapter.Name(), Scope: scope}
	}
	return nil
}

func buildScopeTransitionArtifact(ctx context.Context, adapter service.Adapter, connection core.Connection, record service.Record, target service.Scope) (service.Artifact, error) {
	for _, scope := range []service.Scope{service.NormalizeScope(record.Scope), service.NormalizeScope(target)} {
		if !service.SupportsScope(adapter, scope) {
			return service.Artifact{}, &service.UnsupportedScopeError{Adapter: adapter.Name(), Scope: scope}
		}
	}
	executionPlan, err := serviceExecutionPlan(ctx, connection)
	if err != nil {
		return service.Artifact{}, err
	}
	reference := record.Reference()
	reference.Scope = target
	reference.ArtifactPath = ""
	return adapter.BuildBound(reference, executionPlan)
}

type unboundNativeIdentityError struct {
	connection string
	unit       string
	scopes     []service.Scope
}

func (e *unboundNativeIdentityError) Error() string {
	values := make([]string, 0, len(e.scopes))
	for _, scope := range e.scopes {
		values = append(values, string(service.NormalizeScope(scope)))
	}
	if len(values) == 0 {
		values = append(values, "unknown")
	}
	return fmt.Sprintf("native service %s exists in %s scope without a Tunlite management record; resolve or manage it before creating another native service", e.unit, strings.Join(values, " and "))
}

func (e *unboundNativeIdentityError) Code() string { return "scope_conflict" }

func (e *unboundNativeIdentityError) Actions() []core.Action {
	actions := []core.Action{{Type: "manage_native_service", Parameters: map[string]any{"service": e.unit}}}
	if e.connection != "" {
		actions = append(actions, resolveActions(e.connection)...)
	}
	return actions
}

func nativeIdentityCandidates(ctx context.Context, adapter service.Adapter, expected service.Artifact) ([]service.Candidate, error) {
	if !adapter.Capabilities().Discovery {
		return []service.Candidate{}, nil
	}
	candidates, err := adapter.Discover(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]service.Candidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.Reference.UnitName == expected.UnitName {
			matches = append(matches, candidate)
		}
	}
	return matches, nil
}

func unboundNativeIdentityConflict(ctx context.Context, adapter service.Adapter, expected service.Artifact) error {
	matches, err := nativeIdentityCandidates(ctx, adapter, expected)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}
	scopes := make([]service.Scope, 0, len(matches))
	for _, candidate := range matches {
		scopes = append(scopes, candidateScope(candidate))
	}
	return &unboundNativeIdentityError{connection: expected.Connection, unit: expected.UnitName, scopes: scopes}
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
	if err := unboundNativeIdentityConflict(ctx, adapter, artifact); err != nil {
		return service.Record{}, service.Artifact{}, false, err
	}
	return recordForArtifact(artifact, service.ModeStopped, declarationPath), artifact, true, nil
}

func materializeLifecycleRecord(ctx context.Context, adapter service.Adapter, records *service.Store, record service.Record, artifact service.Artifact, missing bool) error {
	if !missing {
		return nil
	}
	if err := unboundNativeIdentityConflict(ctx, adapter, artifact); err != nil {
		return err
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
		return failedServiceOperation(connection.Metadata.Name, err, "service_state_read_failed")
	}
	if record.Mode == service.ModePersistent {
		return failedServiceItem(connection.Metadata.Name, "persistent_connection", errors.New("connection is persistent; start cannot change it to temporary background mode"), "low", nil)
	}
	if service.NormalizeScope(record.Scope) == service.ScopeSystem {
		return failedServiceItem(connection.Metadata.Name, "system_scope_requires_enable", errors.New("system-scope connections cannot run temporarily; use enable"), "low", nil)
	}
	conditions := []core.Condition{}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	if !missing && before.ConsistencyState != service.ConsistencyConsistent {
		return failedServiceItem(connection.Metadata.Name, "connection_drifted", errors.New("connection has drifted; resolve it before starting"), "low", resolveActions(connection.Metadata.Name))
	}
	if record.Mode == service.ModeBackground && (before.ProcessState == service.ProcessRunning || before.ProcessState == service.ProcessStarting) {
		return observeLifecycleResult(ctx, adapter, record, artifact, "start", succeededServiceItem(connection.Metadata.Name, before), opts)
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
	return observeLifecycleResult(ctx, adapter, record, artifact, "start", result, opts)
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
		return failedServiceOperation(connection.Metadata.Name, err, "execution_plan_failed")
	}
	if service.NormalizeScope(record.Scope) == service.ScopeSystem {
		return failedServiceItem(connection.Metadata.Name, "system_scope_requires_enable", errors.New("system-scope connections are persistent; use disable instead of stop"), "low", nil)
	}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	if record.Mode == service.ModeStopped && before.ProcessState == service.ProcessInactive &&
		(service.NormalizeScope(record.Scope) != service.ScopeSystem || before.ServiceState == service.ServiceAbsent) {
		return succeededServiceItem(connection.Metadata.Name, before)
	}
	if before.ConsistencyState != service.ConsistencyConsistent &&
		!(service.NormalizeScope(record.Scope) == service.ScopeSystem && record.Mode == service.ModeStopped) {
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

func enableService(
	ctx context.Context,
	adapter service.Adapter,
	records *service.Store,
	connection core.Connection,
	declarationPath string,
	targetScope service.Scope,
	targetScopeSet bool,
	streams IO,
	opts options,
) serviceItemResult {
	if targetScopeSet {
		if existing, exists, loadErr := records.Load(connection.Metadata.Name); loadErr != nil {
			return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", loadErr, "low", nil)
		} else if exists && service.NormalizeScope(existing.Scope) != targetScope {
			return migrateAndEnableService(ctx, adapter, records, connection, declarationPath, existing, targetScope, streams, opts)
		} else if !exists && targetScope == service.ScopeSystem {
			return installAndEnableScopedService(ctx, adapter, records, connection, declarationPath, targetScope, streams, opts)
		}
	}
	record, artifact, missing, err := lifecycleRecordAndArtifact(ctx, adapter, records, connection, declarationPath)
	if err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "service_state_read_failed")
	}
	conditions := []core.Condition{}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	if service.NormalizeScope(record.Scope) == service.ScopeSystem && record.Mode == service.ModeStopped && before.ServiceState == service.ServiceAbsent {
		return reenableSystemService(ctx, adapter, records, record, artifact, connection.Metadata.Name, before, opts)
	}
	if !missing && before.ConsistencyState != service.ConsistencyConsistent {
		return failedServiceItem(connection.Metadata.Name, "connection_drifted", errors.New("connection has drifted; resolve it before enabling"), "low", resolveActions(connection.Metadata.Name))
	}
	if record.Mode == service.ModePersistent && (before.ProcessState == service.ProcessRunning || before.ProcessState == service.ProcessStarting) {
		return observeLifecycleResult(ctx, adapter, record, artifact, "enable", succeededServiceItem(connection.Metadata.Name, before), opts)
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
	return observeLifecycleResult(ctx, adapter, record, artifact, "enable", result, opts)
}

func reenableSystemService(
	ctx context.Context,
	adapter service.Adapter,
	records *service.Store,
	oldRecord service.Record,
	artifact service.Artifact,
	connection string,
	before service.State,
	opts options,
) serviceItemResult {
	result := serviceItemResult{
		Connection: connection, Outcome: "applied", Changed: true,
		Conditions: []core.Condition{}, Artifact: artifactResult(artifact),
	}
	if opts.DryRun {
		result.Outcome = "planned"
		result.State = &before
		return result
	}
	transition, err := service.StageScopeInstall(ctx, adapter, artifact, service.ModePersistent, transitionHealthVerifier(artifact))
	if err != nil {
		return failedServiceOperation(connection, err, "scope_transition_failed")
	}
	newRecord := updatedRecordForArtifact(oldRecord, artifact, service.ModePersistent)
	if err := records.Write(ctx, newRecord); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection, "service_state_write_failed", err, transition.Rollback(recoveryCtx))
	}
	if err := transition.Commit(ctx); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		recordErr := records.Write(recoveryCtx, oldRecord)
		return failedServiceItemAfterRecovery(connection, "scope_transition_failed", err,
			errors.Join(recordErr, transition.Rollback(recoveryCtx)))
	}
	return observeLifecycleResult(ctx, adapter, newRecord, artifact, "enable", result, opts)
}

func installAndEnableScopedService(
	ctx context.Context,
	adapter service.Adapter,
	records *service.Store,
	connection core.Connection,
	declarationPath string,
	targetScope service.Scope,
	streams IO,
	opts options,
) serviceItemResult {
	artifact, err := buildServiceArtifactScoped(ctx, adapter, connection, targetScope)
	if err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "execution_plan_failed")
	}
	if err := unboundNativeIdentityConflict(ctx, adapter, artifact); err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "scope_transition_failed")
	}
	record := recordForArtifact(artifact, service.ModePersistent, declarationPath)
	result := serviceItemResult{
		Connection: connection.Metadata.Name, Outcome: "applied", Changed: true,
		Conditions: []core.Condition{}, Artifact: artifactResult(artifact),
	}
	renderScopeChangeProgress(streams, opts, connection.Metadata.Name, "absent", targetScope, "start")
	if opts.DryRun {
		result.Outcome = "planned"
		state := service.State{
			Connection: record.Connection, Adapter: adapter.Name(), Scope: targetScope,
			ServiceState: service.ServiceAbsent, Mode: service.ModeStopped,
			ProcessState: service.ProcessInactive, Health: service.HealthUnknown,
			ConsistencyState: service.ConsistencyMissing, ObservedAt: time.Now().UTC(),
			Endpoints: append([]plan.Endpoint(nil), artifact.Plan.Endpoints...), Conditions: []core.Condition{},
		}
		result.State = &state
		return result
	}
	transition, err := service.StageScopeInstall(ctx, adapter, artifact, service.ModePersistent, transitionHealthVerifier(artifact))
	if err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "scope_transition_failed")
	}
	renderScopeChangeProgress(streams, opts, connection.Metadata.Name, "absent", targetScope, "target_ready")
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_state_write_failed", err,
			transition.Rollback(recoveryCtx))
	}
	if err := transition.Commit(ctx); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, serviceOperationErrorCode(err, "scope_transition_failed"), err,
			errors.Join(records.Remove(connection.Metadata.Name), transition.Rollback(recoveryCtx)))
	}
	renderScopeChangeProgress(streams, opts, connection.Metadata.Name, "absent", targetScope, "complete")
	return observeLifecycleResult(ctx, adapter, record, artifact, "enable", result, opts)
}

func migrateAndEnableService(
	ctx context.Context,
	adapter service.Adapter,
	records *service.Store,
	connection core.Connection,
	declarationPath string,
	oldRecord service.Record,
	targetScope service.Scope,
	streams IO,
	opts options,
) serviceItemResult {
	if err := ensureDeclarationOwner(oldRecord, declarationPath); err != nil {
		return failedServiceItem(connection.Metadata.Name, "declaration_conflict", err, "low", nil)
	}
	sourceArtifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &oldRecord)
	if err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "execution_plan_failed")
	}
	before := service.Evaluate(ctx, adapter, oldRecord, &sourceArtifact)
	if before.ConsistencyState != service.ConsistencyConsistent {
		return failedServiceItem(connection.Metadata.Name, "connection_drifted",
			errors.New("connection has drifted; resolve it before changing service scope"), "low", resolveActions(connection.Metadata.Name))
	}
	targetArtifact, err := buildScopeTransitionArtifact(ctx, adapter, connection, oldRecord, targetScope)
	if err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "execution_plan_failed")
	}
	if _, err := service.PlanScopeTransition(adapter, oldRecord, targetScope); err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "scope_transition_failed")
	}
	result := serviceItemResult{
		Connection: connection.Metadata.Name, Outcome: "applied", Changed: true,
		Conditions: []core.Condition{}, Artifact: artifactResult(targetArtifact),
	}
	renderScopeChangeProgress(streams, opts, connection.Metadata.Name, string(service.NormalizeScope(oldRecord.Scope)), targetScope, "start")
	if opts.DryRun {
		result.Outcome = "planned"
		result.State = &before
		return result
	}
	transition, err := service.StageScopeTransition(
		ctx, adapter, oldRecord.Reference(), targetArtifact, service.ModePersistent, transitionHealthVerifier(targetArtifact),
	)
	if err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "scope_transition_failed")
	}
	renderScopeChangeProgress(streams, opts, connection.Metadata.Name, string(service.NormalizeScope(oldRecord.Scope)), targetScope, "target_ready")
	newRecord := updatedRecordForArtifact(oldRecord, targetArtifact, service.ModePersistent)
	if err := records.Write(ctx, newRecord); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_state_write_failed", err, transition.Rollback(recoveryCtx))
	}
	if err := transition.Commit(ctx); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		recordErr := records.Write(recoveryCtx, oldRecord)
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "scope_transition_failed", err,
			errors.Join(recordErr, transition.Rollback(recoveryCtx)))
	}
	renderScopeChangeProgress(streams, opts, connection.Metadata.Name, string(service.NormalizeScope(oldRecord.Scope)), targetScope, "complete")
	return observeLifecycleResult(ctx, adapter, newRecord, targetArtifact, "enable", result, opts)
}

func updatedRecordForArtifact(record service.Record, artifact service.Artifact, mode string) service.Record {
	record.Adapter = artifact.Adapter
	record.Scope = service.NormalizeScope(artifact.Scope)
	record.Mode = mode
	record.ArtifactPath = artifact.Path
	record.UnitName = artifact.UnitName
	record.LogPaths = append([]string(nil), artifact.LogPaths...)
	record.PlanDigest = artifact.PlanDigest
	record.BaselineDigest = service.ContentDigest(artifact.Content)
	return record
}

func renderScopeChangeProgress(streams IO, opts options, connection, from string, to service.Scope, phase string) {
	if opts.JSON || !terminalProgressEnabled(streams) {
		return
	}
	toLabel := strings.ToUpper(string(to))
	fromLabel := strings.ToUpper(from)
	switch phase {
	case "start":
		if isChinese(opts.Language) {
			if from == "absent" {
				fmt.Fprintf(streams.Err, "正在把“%s”安装到 %s 范围…\n", connection, toLabel)
			} else {
				fmt.Fprintf(streams.Err, "正在把“%s”从 %s 范围迁移到 %s 范围…\n", connection, fromLabel, toLabel)
			}
		} else {
			if from == "absent" {
				fmt.Fprintf(streams.Err, "Installing %q in %s scope…\n", connection, toLabel)
			} else {
				fmt.Fprintf(streams.Err, "Moving %q from %s scope to %s scope…\n", connection, fromLabel, toLabel)
			}
		}
	case "target_ready":
		fmt.Fprintln(streams.Err, localized(opts.Language, "Target process is stable; saving the new scope…", "目标进程已稳定，正在保存新的范围…"))
	case "complete":
		fmt.Fprintln(streams.Err, localized(opts.Language, "Service scope change complete.", "服务范围变更完成。"))
	}
}

func transitionHealthVerifier(artifact service.Artifact) func(context.Context, service.Reference) error {
	return func(ctx context.Context, _ service.Reference) error {
		for _, check := range artifact.Plan.HealthChecks {
			probeCtx, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutMS)*time.Millisecond)
			connection, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", check.Address)
			cancel()
			if err != nil {
				return fmt.Errorf("endpoint %s did not become healthy: %w", check.Address, err)
			}
			_ = connection.Close()
		}
		return nil
	}
}

func transitionHealthVerifierWithProgress(artifact service.Artifact, streams IO, opts options, connection string) func(context.Context, service.Reference) error {
	verify := transitionHealthVerifier(artifact)
	var once sync.Once
	return func(ctx context.Context, reference service.Reference) error {
		once.Do(func() { renderSystemDefinitionProgress(streams, opts, connection, "health") })
		return verify(ctx, reference)
	}
}

func disableService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, opts options) serviceItemResult {
	record, err := loadServiceRecord(records, connection, declarationPath)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, "low", nil)
	}
	artifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &record)
	if err != nil {
		return failedServiceOperation(connection.Metadata.Name, err, "execution_plan_failed")
	}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	systemScope := service.NormalizeScope(record.Scope) == service.ScopeSystem
	if record.Mode == service.ModeStopped && before.ProcessState == service.ProcessInactive {
		if !systemScope {
			return succeededServiceItem(connection.Metadata.Name, before)
		}
		observation, inspectErr := adapter.Inspect(ctx, record.Reference())
		if inspectErr != nil {
			return failedServiceOperation(connection.Metadata.Name, inspectErr, "service_state_read_failed")
		}
		if !observation.Installed && !observation.AutoStart && observation.ProcessState == service.ProcessInactive &&
			before.ServiceState == service.ServiceAbsent {
			return succeededServiceItem(connection.Metadata.Name, before)
		}
	}
	if record.Mode == service.ModeBackground {
		return failedServiceItem(connection.Metadata.Name, "background_connection", errors.New("connection is in background mode; use stop"), "low", nil)
	}
	if before.ConsistencyState != service.ConsistencyConsistent && !(systemScope && record.Mode == service.ModeStopped) {
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
	if service.NormalizeScope(record.Scope) == service.ScopeSystem {
		record.ArtifactPath = ""
	}
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		var recoveryErr error
		if service.NormalizeScope(record.Scope) == service.ScopeSystem {
			materializeErr := adapter.Materialize(recoveryCtx, artifact)
			enableErr := error(nil)
			if materializeErr == nil {
				enableErr = adapter.Enable(recoveryCtx, record.Reference())
			}
			recoveryErr = errors.Join(materializeErr, enableErr)
		} else {
			recoveryErr = adapter.Enable(recoveryCtx, record.Reference())
		}
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_state_write_failed", err,
			recoveryErr)
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
		return failedServiceOperation(connection.Metadata.Name, err, "execution_plan_failed")
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
		if service.NormalizeScope(record.Scope) == service.ScopeSystem {
			// System restart is intentionally a verified, non-privileged KeepAlive
			// replacement. Never turn a verification/convergence failure into a
			// privileged Disable/Enable fallback.
			return failedServiceOperation(connection.Metadata.Name, err, "service_restart_failed")
		}
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return failedServiceItemAfterRecovery(connection.Metadata.Name, "service_restart_failed", err,
			applyNativeMode(recoveryCtx, adapter, record.Reference(), record.Mode))
	}
	return observeLifecycleResult(ctx, adapter, record, artifact, "restart", result, opts)
}

func statusService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string) serviceItemResult {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, "low", nil)
	}
	if !exists {
		artifact, planErr := buildServiceArtifact(ctx, adapter, connection)
		identityArtifact := artifact
		if planErr != nil {
			identityArtifact = adapter.Target(connection.Metadata.Name)
		}
		var identityMatches []service.Candidate
		var identityErr error
		if identityArtifact.UnitName != "" {
			identityMatches, identityErr = nativeIdentityCandidates(ctx, adapter, identityArtifact)
		}
		consistency := service.ConsistencyMissing
		serviceState := service.ServiceAbsent
		mode := service.ModeStopped
		processState := service.ProcessInactive
		scope := service.ScopeUser
		endpoints := []plan.Endpoint{}
		statusCondition := core.Condition{
			Code: "service_missing", Severity: "warning", Message: "native service has not been materialized on this machine",
			Connection: connection.Metadata.Name, Actions: []core.Action{{Type: "start_connection", Parameters: map[string]any{"connection": connection.Metadata.Name}}},
		}
		if identityErr != nil {
			consistency = service.ConsistencyBlocked
			serviceState = service.ServiceUnknown
			processState = service.ProcessUnknown
			if actual, ok := scopeMismatchActual(identityErr); ok {
				scope = actual
				serviceState = service.ServiceRegistered
			}
			statusCondition = nativeStatusErrorCondition(identityErr, connection.Metadata.Name, adapter.Name())
		} else if len(identityMatches) > 0 {
			candidate := identityMatches[0]
			scopes := make([]service.Scope, 0, len(identityMatches))
			for _, match := range identityMatches {
				scopes = append(scopes, candidateScope(match))
			}
			conflict := &unboundNativeIdentityError{connection: connection.Metadata.Name, unit: identityArtifact.UnitName, scopes: scopes}
			consistency = service.ConsistencyBlocked
			serviceState = service.ServiceRegistered
			mode = observedMode(candidate.Observation)
			processState = candidate.Observation.ProcessState
			scope = candidateScope(candidate)
			endpoints = append([]plan.Endpoint(nil), candidate.Plan.Endpoints...)
			statusCondition = core.Condition{
				Code: conflict.Code(), Severity: "error", Message: conflict.Error(), Connection: connection.Metadata.Name,
				Actions: conflict.Actions(),
			}
		} else if planErr != nil && serviceOperationErrorCode(planErr, "") != "scope_unsupported" {
			consistency = service.ConsistencyBlocked
			statusCondition = core.Condition{
				Code: "execution_plan_blocked", Severity: "error", Message: planErr.Error(), Connection: connection.Metadata.Name,
				Driver: connection.Spec.Driver, Actions: []core.Action{},
			}
		} else if planErr == nil {
			observation, inspectErr := adapter.Inspect(ctx, artifact.Reference())
			if inspectErr != nil {
				consistency = service.ConsistencyBlocked
				serviceState = service.ServiceUnknown
				processState = service.ProcessUnknown
				if actual, ok := scopeMismatchActual(inspectErr); ok {
					scope = actual
					serviceState = service.ServiceRegistered
				}
				statusCondition = nativeStatusErrorCondition(inspectErr, connection.Metadata.Name, adapter.Name())
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
		}
		state := service.State{
			Connection: connection.Metadata.Name, Adapter: adapter.Name(), Scope: scope, ServiceState: serviceState,
			Mode: mode, ProcessState: processState, Health: service.HealthUnknown,
			ConsistencyState: consistency, ObservedAt: time.Now().UTC(),
			Endpoints: endpoints, Conditions: []core.Condition{statusCondition},
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
	if planErr != nil && serviceOperationErrorCode(planErr, "") == "scope_unsupported" {
		state := service.State{
			Connection: connection.Metadata.Name, Adapter: adapter.Name(), Scope: service.NormalizeScope(record.Scope),
			ServiceState: service.ServiceUnknown, Mode: record.Mode, ProcessState: service.ProcessUnknown,
			Health: service.HealthUnknown, ConsistencyState: service.ConsistencyBlocked, ObservedAt: time.Now().UTC(),
			Endpoints: []plan.Endpoint{}, Conditions: []core.Condition{{
				Code: "scope_unsupported", Severity: "error", Message: planErr.Error(), Connection: connection.Metadata.Name,
				Driver: connection.Spec.Driver, Actions: []core.Action{},
			}},
		}
		addRecordIdentity(&state, record)
		return serviceItemResult{Connection: connection.Metadata.Name, Outcome: "succeeded", Changed: false, State: &state, Conditions: []core.Condition{}}
	}
	var expected *service.Artifact
	if planErr == nil {
		expected = &artifact
	}
	state := service.Evaluate(ctx, adapter, record, expected)
	addRecordIdentity(&state, record)
	if records.RunActive(connection.Metadata.Name) {
		state.Mode, state.ProcessState = service.ModeForeground, service.ProcessRunning
	}
	if planErr != nil {
		state.ConsistencyState = service.ConsistencyBlocked
		state.Conditions = append(state.Conditions, core.Condition{
			Code: serviceOperationErrorCode(planErr, "execution_plan_blocked"), Severity: "error", Message: planErr.Error(), Connection: connection.Metadata.Name,
			Driver: connection.Spec.Driver, Actions: []core.Action{},
		})
	}
	return serviceItemResult{Connection: connection.Metadata.Name, Outcome: "succeeded", Changed: false, State: &state, Conditions: []core.Condition{}}
}

func addRecordIdentity(state *service.State, record service.Record) {
	if state.Platform == nil {
		state.Platform = map[string]any{}
	}
	serviceKey := record.ServiceKey
	if serviceKey == "" {
		serviceKey = record.Connection
	}
	state.Platform["serviceKey"] = serviceKey
	state.Platform["unitName"] = record.UnitName
	state.Platform["nativeIdentity"] = record.UnitName
	if state.Platform["nativeIdentity"] == "" {
		state.Platform["nativeIdentity"] = serviceKey
	}
}

func nativeStatusErrorCondition(err error, connection, adapter string) core.Condition {
	code := serviceOperationErrorCode(err, "service_manager_unavailable")
	actions := []core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{"adapter": adapter}}}
	if code == "scope_conflict" || code == "scope_mismatch" {
		actions = resolveActions(connection)
	}
	return core.Condition{
		Code: code, Severity: "error", Message: err.Error(), Connection: connection, Actions: actions,
	}
}

func scopeMismatchActual(err error) (service.Scope, bool) {
	var mismatch *service.ScopeMismatchError
	if !errors.As(err, &mismatch) {
		return "", false
	}
	return service.NormalizeScope(mismatch.Actual), true
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
		Scope: service.NormalizeScope(artifact.Scope), Mode: mode, ArtifactPath: artifact.Path, UnitName: artifact.UnitName,
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
	return &serviceArtifactResult{Adapter: artifact.Adapter, Scope: service.NormalizeScope(artifact.Scope), UnitName: artifact.UnitName, Path: artifact.Path, PlanDigest: artifact.PlanDigest}
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

func serviceOperationErrorCode(err error, fallback string) string {
	var coded interface{ Code() string }
	if errors.As(err, &coded) && coded.Code() != "" {
		return coded.Code()
	}
	return fallback
}

func failedServiceOperation(connection string, err error, fallback string) serviceItemResult {
	code := serviceOperationErrorCode(err, fallback)
	risk := "low"
	var actions []core.Action
	if code == "recovery_blocked" || code == "scope_conflict" {
		risk = "high"
		var actionable interface{ Actions() []core.Action }
		if errors.As(err, &actionable) {
			actions = actionable.Actions()
		} else {
			actions = resolveActions(connection)
		}
	}
	return failedServiceItem(connection, code, err, risk, actions)
}

func failedServiceItemAfterRecovery(connection, code string, cause, recovery error) serviceItemResult {
	if recovery == nil {
		return failedServiceOperation(connection, cause, code)
	}
	return failedServiceItem(connection, "recovery_blocked",
		errors.Join(cause, fmt.Errorf("automatic recovery failed: %w", recovery)), "high", resolveActions(connection))
}

func renderServiceResults(streams IO, opts options, operation string, results []serviceItemResult) error {
	failed, succeeded, starting := 0, 0, 0
	interactionFailuresOnly := len(results) > 0
	for _, result := range results {
		if result.Outcome == "failed" {
			failed++
			if result.Error == nil || (result.Error.Code != "privilege_required" && result.Error.Code != "interaction_required") {
				interactionFailuresOnly = false
			}
		} else if result.Outcome == "starting" {
			starting++
			interactionFailuresOnly = false
		} else {
			succeeded++
			interactionFailuresOnly = false
		}
	}
	outcome := "succeeded"
	if opts.DryRun && operation != "status" {
		outcome = "planned"
	}
	if starting > 0 {
		outcome = "starting"
	}
	if failed > 0 {
		if succeeded > 0 || starting > 0 {
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
		if len(rows) == 0 {
			fmt.Fprintln(streams.Out, localized(opts.Language,
				"No connections yet. Create one with: tunlite add <name> [SSH options] <destination>",
				"还没有连接。请使用 tunlite add <name> [SSH 选项] <destination> 创建。"))
		} else if len(rows) == 1 && detailed {
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
					fmt.Fprintf(streams.Out, "%s  %s  运行位置：%s  自动启动：%s  范围：%s\n",
						result.Connection, style.glyph+" "+style.label,
						statusRunsAs(row, opts.Language), statusAutostart(row, opts.Language), statusScope(row, opts.Language))
				} else {
					fmt.Fprintf(streams.Out, "%s  %s  runs as: %s  autostart: %s  scope: %s\n",
						result.Connection, style.glyph+" "+style.label,
						statusRunsAs(row, opts.Language), statusAutostart(row, opts.Language), statusScope(row, opts.Language))
				}
			} else {
				fmt.Fprintf(streams.Out, "%s: %s\n", result.Connection, result.Outcome)
			}
		}
	}
	if failed > 0 && (succeeded > 0 || starting > 0) {
		return renderedExit(exitPartial)
	}
	if failed > 0 {
		if interactionFailuresOnly {
			return renderedExit(4)
		}
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
	renderCommandProgress(streams, opts, append([]string{"logs", name}, args[1:]...))
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
	if err := ensureRecordScopeSupported(adapter, record); err != nil {
		return err
	}
	if opts.JSON && follow {
		if err := writeResponse(streams.Out, map[string]any{
			"outcome": "starting", "command": "logs", "connection": name, "stream": "logs",
		}); err != nil {
			return err
		}
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
	logs := append(logRecords("stdout", stdout.String()), logRecords("stderr", stderr.String())...)
	return writeResponse(streams.Out, map[string]any{
		"outcome": "succeeded", "command": "logs", "connection": name,
		"stream": "snapshot", "logs": logs,
	})
}

type logRecord struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}

func logRecords(stream, value string) []logRecord {
	lines := splitLogLines(value)
	result := make([]logRecord, 0, len(lines))
	for _, line := range lines {
		result = append(result, logRecord{Stream: stream, Line: line})
	}
	return result
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
