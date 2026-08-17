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

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	appPaths "github.com/yuanyuanzijin/tunlite/internal/paths"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

var newServiceAdapter = func() (service.Adapter, error) { return service.NewCurrent(nil) }

type serviceSelection struct {
	target  string
	tags    []string
	approve bool
}

type serviceItemError struct {
	Code             string        `json:"code"`
	Message          string        `json:"message"`
	RiskLevel        string        `json:"riskLevel"`
	RequiresApproval bool          `json:"requiresApproval"`
	Actions          []core.Action `json:"actions"`
}

type serviceArtifactResult struct {
	Adapter    string `json:"adapter"`
	UnitName   string `json:"unitName"`
	Path       string `json:"path"`
	PlanDigest string `json:"planDigest"`
}

type serviceItemResult struct {
	Connection string                 `json:"connection"`
	Outcome    string                 `json:"outcome"`
	Changed    bool                   `json:"changed"`
	State      *service.State         `json:"state,omitempty"`
	Artifact   *serviceArtifactResult `json:"artifact,omitempty"`
	Conditions []core.Condition       `json:"conditions"`
	Error      *serviceItemError      `json:"error,omitempty"`
}

func serviceCommand(ctx context.Context, command string, args []string, streams IO, opts options) error {
	selection, err := parseServiceSelection(command, args)
	if err != nil {
		return err
	}
	adapter, records, config, document, err := serviceDependencies(opts)
	if err != nil {
		return err
	}
	targets, err := selectServiceConnections(document, selection, command == "status" || command == "sync")
	if err != nil {
		return err
	}
	results := make([]serviceItemResult, 0, len(targets))
	for _, connection := range targets {
		var release func()
		if command != "status" && !opts.DryRun {
			release, err = records.Lock(ctx, connection.Metadata.Name)
			if err != nil {
				results = append(results, failedServiceItem(connection.Metadata.Name, "service_busy", err, false, "low", nil))
				continue
			}
			fresh, loadErr := config.Load()
			if loadErr != nil {
				release()
				results = append(results, failedServiceItem(connection.Metadata.Name, "declaration_read_failed", loadErr, false, "low", nil))
				continue
			}
			_, current := configstore.Find(&fresh, connection.Metadata.Name)
			if current == nil {
				release()
				results = append(results, failedServiceItem(connection.Metadata.Name, "not_found", errors.New("connection was removed before the service operation started"), false, "low", nil))
				continue
			}
			if len(selection.tags) > 0 && !hasAnyTag(current.Metadata.Labels, selection.tags) {
				release()
				results = append(results, failedServiceItem(connection.Metadata.Name, "selection_changed", errors.New("connection no longer matches the selected tags"), false, "low", nil))
				continue
			}
			connection = cloneConnection(*current)
		}
		var result serviceItemResult
		switch command {
		case "enable":
			result = enableService(ctx, adapter, records, connection, config.Path(), selection.approve, opts)
		case "disable":
			result = disableService(ctx, adapter, records, connection, config.Path(), opts)
		case "restart":
			result = restartService(ctx, adapter, records, connection, config.Path(), opts)
		case "status":
			result = statusService(ctx, adapter, records, connection, config.Path())
		case "sync":
			result = syncService(ctx, adapter, records, connection, config.Path(), selection.approve, opts)
		}
		if release != nil {
			release()
		}
		results = append(results, result)
	}
	return renderServiceResults(streams, opts, command, results)
}

func ensureServiceRemovable(ctx context.Context, name string, lock bool) (func(), error) {
	stateDir, err := appPaths.StateDir()
	if err != nil {
		return nil, err
	}
	records := service.NewStore(filepath.Join(stateDir, "services"))
	release := func() {}
	if lock {
		release, err = records.Lock(ctx, name)
		if err != nil {
			return nil, err
		}
	}
	record, exists, err := records.Load(name)
	if err != nil || !exists {
		if err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
	if record.DesiredState == service.DesiredEnabled {
		release()
		return nil, conflictError(fmt.Sprintf("connection %q is enabled; disable it before removing or renaming it", name))
	}
	adapter, err := newServiceAdapter()
	if err != nil {
		release()
		return nil, fmt.Errorf("cannot verify native service cleanup for %q: %w", name, err)
	}
	observation, err := adapter.Inspect(ctx, name)
	if err != nil {
		release()
		return nil, fmt.Errorf("cannot verify native service cleanup for %q: %w", name, err)
	}
	if observation.Installed || observation.Enabled || observation.ProcessState == service.ProcessRunning || observation.ProcessState == service.ProcessStarting {
		release()
		return nil, conflictError(fmt.Sprintf("native service for %q is still present or running; run `tunlite disable %s` again", name, name))
	}
	return release, nil
}

func removeServiceRecord(name string) error {
	stateDir, err := appPaths.StateDir()
	if err != nil {
		return err
	}
	return service.NewStore(filepath.Join(stateDir, "services")).Remove(name)
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
	if command == "enable" || command == "sync" {
		flags.BoolVar(&selection.approve, "approve", false, "approve publish or public-listen risk")
	}
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
	if selection.target == "" && len(selection.tags) == 0 && command != "status" && command != "sync" {
		return selection, usageError(fmt.Sprintf("usage: tunlite %s <name|all> [--tag tag]", command))
	}
	return selection, nil
}

func serviceDependencies(opts options) (service.Adapter, *service.Store, *configstore.Store, configstore.Document, error) {
	adapter, err := newServiceAdapter()
	if err != nil {
		return nil, nil, nil, configstore.Document{}, err
	}
	stateDir, err := appPaths.StateDir()
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
	return adapter, service.NewStore(filepath.Join(stateDir, "services")), config, document, nil
}

func selectServiceConnections(document configstore.Document, selection serviceSelection, defaultAll bool) ([]core.Connection, error) {
	target := selection.target
	if target == "" && defaultAll {
		target = "all"
	}
	if target != "" && target != "all" {
		_, connection := configstore.Find(&document, target)
		if connection == nil {
			return nil, notFoundError(fmt.Sprintf("no such connection: %s", target))
		}
		if len(selection.tags) > 0 && !hasAnyTag(connection.Metadata.Labels, selection.tags) {
			return nil, notFoundError(fmt.Sprintf("connection %q does not match the selected tags", target))
		}
		return []core.Connection{cloneConnection(*connection)}, nil
	}
	results := make([]core.Connection, 0, len(document.Items))
	for _, connection := range document.Items {
		if len(selection.tags) == 0 || hasAnyTag(connection.Metadata.Labels, selection.tags) {
			results = append(results, cloneConnection(connection))
		}
	}
	if len(results) == 0 && len(selection.tags) > 0 {
		return nil, notFoundError(fmt.Sprintf("no connections tagged %s", quoteList(selection.tags)))
	}
	return results, nil
}

func buildServiceArtifact(ctx context.Context, adapter service.Adapter, connection core.Connection) (service.Artifact, error) {
	if connection.Spec.Lifecycle != "persistent" {
		return service.Artifact{}, fmt.Errorf("connection %q has foreground lifecycle and cannot be enabled", connection.Metadata.Name)
	}
	registry, err := core.NewRegistry(sshdriver.New())
	if err != nil {
		return service.Artifact{}, err
	}
	executionPlan, err := core.NewPlanner(registry).Plan(ctx, connection, core.PlanOptions{NonInteractive: true})
	if err != nil {
		return service.Artifact{}, err
	}
	return adapter.Build(connection.Metadata.Name, executionPlan)
}

func enableService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, approved bool, opts options) serviceItemResult {
	stored, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, false, "low", nil)
	}
	if err := ensureDeclarationOwner(stored, exists, declarationPath); err != nil {
		return failedServiceItem(connection.Metadata.Name, "declaration_conflict", err, false, "low", nil)
	}
	artifact, err := buildServiceArtifact(ctx, adapter, connection)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "execution_plan_failed", err, false, "low", nil)
	}
	conditions := approvalConditions(connection, artifact)
	approvalNeeded := len(conditions) > 0 && stored.ApprovedPlanDigest != artifact.PlanDigest
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: conditions, Artifact: artifactResult(artifact)}
	if approvalNeeded && !approved && !opts.DryRun {
		return failedServiceItem(connection.Metadata.Name, "approval_required", errors.New("persistent publish or public-listen access requires explicit --approve"), true, "high",
			[]core.Action{{Type: "approve_connection", Parameters: map[string]any{"connection": connection.Metadata.Name, "operation": "enable"}}})
	}
	record := recordForArtifact(artifact, service.DesiredEnabled, declarationPath)
	if len(conditions) > 0 && (approved || stored.ApprovedPlanDigest == artifact.PlanDigest) {
		record.ApprovedPlanDigest = artifact.PlanDigest
	}
	if exists && stored.DesiredState == service.DesiredEnabled {
		current := service.Evaluate(ctx, adapter, stored, &artifact)
		if current.SyncState == service.SyncSynced && (current.ProcessState == service.ProcessRunning || current.ProcessState == service.ProcessStarting) {
			if approvalNeeded && approved && !opts.DryRun {
				if err := records.Write(ctx, record); err != nil {
					return failedServiceItem(connection.Metadata.Name, "service_state_write_failed", err, false, "low", nil)
				}
			}
			result.Outcome, result.Changed, result.State = "succeeded", false, &current
			return result
		}
	}
	if opts.DryRun {
		result.Outcome = "planned"
		return result
	}
	if err := records.Write(ctx, record); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_write_failed", err, false, "low", nil)
	}
	if err := adapter.Enable(ctx, artifact); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_enable_failed", err, false, "low",
			[]core.Action{{Type: "sync_connection", Parameters: map[string]any{"connection": connection.Metadata.Name}}})
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	result.State = &state
	return result
}

func disableService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, opts options) serviceItemResult {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, false, "low", nil)
	}
	if err := ensureDeclarationOwner(record, exists, declarationPath); err != nil {
		return failedServiceItem(connection.Metadata.Name, "declaration_conflict", err, false, "low", nil)
	}
	target := adapter.Target(connection.Metadata.Name)
	record.APIVersion, record.Connection, record.Adapter = service.RecordVersion, connection.Metadata.Name, adapter.Name()
	record.DesiredState, record.ArtifactPath, record.UnitName = service.DesiredDisabled, target.Path, target.UnitName
	record.DeclarationPath = declarationPath
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: []core.Condition{}, Artifact: artifactResult(target)}
	if opts.DryRun {
		result.Outcome = "planned"
		return result
	}
	if err := records.Write(ctx, record); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_write_failed", err, false, "low", nil)
	}
	if err := adapter.Disable(ctx, connection.Metadata.Name); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_disable_failed", err, false, "low",
			[]core.Action{{Type: "sync_connection", Parameters: map[string]any{"connection": connection.Metadata.Name}}})
	}
	state := service.Evaluate(ctx, adapter, record, nil)
	result.State = &state
	return result
}

func restartService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, opts options) serviceItemResult {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, false, "low", nil)
	}
	if err := ensureDeclarationOwner(record, exists, declarationPath); err != nil {
		return failedServiceItem(connection.Metadata.Name, "declaration_conflict", err, false, "low", nil)
	}
	if record.DesiredState != service.DesiredEnabled {
		return failedServiceItem(connection.Metadata.Name, "connection_disabled", errors.New("connection is disabled; enable it before restarting"), false, "low",
			[]core.Action{{Type: "enable_connection", Parameters: map[string]any{"connection": connection.Metadata.Name}}})
	}
	artifact, err := buildServiceArtifact(ctx, adapter, connection)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "execution_plan_failed", err, false, "low", nil)
	}
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: []core.Condition{}, Artifact: artifactResult(artifact)}
	if opts.DryRun {
		result.Outcome = "planned"
		return result
	}
	if err := adapter.Restart(ctx, connection.Metadata.Name); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_restart_failed", err, false, "low", nil)
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	result.State = &state
	return result
}

func statusService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string) serviceItemResult {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, false, "low", nil)
	}
	if err := ensureDeclarationOwner(record, exists, declarationPath); err != nil {
		return failedServiceItem(connection.Metadata.Name, "declaration_conflict", err, false, "low", nil)
	}
	artifact, planErr := buildServiceArtifact(ctx, adapter, connection)
	var expected *service.Artifact
	if planErr == nil {
		expected = &artifact
	}
	state := service.Evaluate(ctx, adapter, record, expected)
	if planErr != nil && record.DesiredState == service.DesiredEnabled {
		state.SyncState = service.SyncBlocked
		state.Conditions = append(state.Conditions, core.Condition{
			Code: "execution_plan_blocked", Severity: "error", Message: planErr.Error(), Connection: connection.Metadata.Name,
			Driver: connection.Spec.Driver, Actions: []core.Action{},
		})
	}
	return serviceItemResult{Connection: connection.Metadata.Name, Outcome: "succeeded", Changed: false, State: &state, Conditions: []core.Condition{}}
}

func syncService(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath string, approved bool, opts options) serviceItemResult {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_read_failed", err, false, "low", nil)
	}
	if err := ensureDeclarationOwner(record, exists, declarationPath); err != nil {
		return failedServiceItem(connection.Metadata.Name, "declaration_conflict", err, false, "low", nil)
	}
	if record.DesiredState == service.DesiredDisabled {
		before := service.Evaluate(ctx, adapter, record, nil)
		if before.SyncState == service.SyncSynced {
			return serviceItemResult{Connection: connection.Metadata.Name, Outcome: "succeeded", Changed: false, State: &before, Conditions: []core.Condition{}}
		}
		result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: []core.Condition{}, Artifact: artifactResult(adapter.Target(connection.Metadata.Name))}
		if opts.DryRun {
			result.Outcome = "planned"
			result.State = &before
			return result
		}
		if err := adapter.Disable(ctx, connection.Metadata.Name); err != nil {
			return failedServiceItem(connection.Metadata.Name, "service_sync_failed", err, false, "low", nil)
		}
		after := service.Evaluate(ctx, adapter, record, nil)
		result.State = &after
		return result
	}
	artifact, err := buildServiceArtifact(ctx, adapter, connection)
	if err != nil {
		return failedServiceItem(connection.Metadata.Name, "execution_plan_failed", err, false, "low", nil)
	}
	before := service.Evaluate(ctx, adapter, record, &artifact)
	approval := approvalConditions(connection, artifact)
	approvalNeeded := len(approval) > 0 && record.ApprovedPlanDigest != artifact.PlanDigest
	if approvalNeeded && !approved && !opts.DryRun {
		return failedServiceItem(connection.Metadata.Name, "approval_required", errors.New("the execution plan requires explicit --approve before sync"), true, "high",
			[]core.Action{{Type: "approve_connection", Parameters: map[string]any{"connection": connection.Metadata.Name, "operation": "sync"}}})
	}
	if before.SyncState == service.SyncSynced && (before.ProcessState == service.ProcessRunning || before.ProcessState == service.ProcessStarting) {
		if approvalNeeded {
			result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, State: &before, Conditions: approval, Artifact: artifactResult(artifact)}
			if opts.DryRun {
				result.Outcome = "planned"
				return result
			}
			record.ApprovedPlanDigest = artifact.PlanDigest
			if err := records.Write(ctx, record); err != nil {
				return failedServiceItem(connection.Metadata.Name, "service_state_write_failed", err, false, "low", nil)
			}
			return result
		}
		return serviceItemResult{Connection: connection.Metadata.Name, Outcome: "succeeded", Changed: false, State: &before, Conditions: []core.Condition{}}
	}
	result := serviceItemResult{Connection: connection.Metadata.Name, Outcome: "applied", Changed: true, Conditions: approval, Artifact: artifactResult(artifact)}
	if opts.DryRun {
		result.Outcome = "planned"
		result.State = &before
		return result
	}
	approvedDigest := record.ApprovedPlanDigest
	record = recordForArtifact(artifact, service.DesiredEnabled, declarationPath)
	if len(approval) > 0 && (approved || approvedDigest == artifact.PlanDigest) {
		record.ApprovedPlanDigest = artifact.PlanDigest
	}
	if err := records.Write(ctx, record); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_state_write_failed", err, false, "low", nil)
	}
	if err := adapter.Enable(ctx, artifact); err != nil {
		return failedServiceItem(connection.Metadata.Name, "service_sync_failed", err, false, "low", nil)
	}
	after := service.Evaluate(ctx, adapter, record, &artifact)
	result.State = &after
	return result
}

func approvalConditions(connection core.Connection, artifact service.Artifact) []core.Condition {
	var reasons []string
	for _, endpoint := range artifact.Plan.Endpoints {
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
		Code: "approval_required", Severity: "warning", Connection: connection.Metadata.Name, Driver: connection.Spec.Driver,
		Message: "persistent connection " + strings.Join(reasons, " and "),
		Actions: []core.Action{{Type: "approve_connection", Parameters: map[string]any{"connection": connection.Metadata.Name, "operation": "enable", "riskLevel": "high"}}},
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

func recordForArtifact(artifact service.Artifact, desired, declarationPath string) service.Record {
	return service.Record{
		APIVersion: service.RecordVersion, Connection: artifact.Connection, Adapter: artifact.Adapter,
		DesiredState: desired, ArtifactPath: artifact.Path, UnitName: artifact.UnitName, PlanDigest: artifact.PlanDigest,
		DeclarationPath: declarationPath,
	}
}

func ensureDeclarationOwner(record service.Record, exists bool, declarationPath string) error {
	if exists && record.DeclarationPath != "" && record.DeclarationPath != declarationPath {
		return fmt.Errorf("connection name %q is already owned by declarations at %s", record.Connection, record.DeclarationPath)
	}
	return nil
}

func artifactResult(artifact service.Artifact) *serviceArtifactResult {
	return &serviceArtifactResult{Adapter: artifact.Adapter, UnitName: artifact.UnitName, Path: artifact.Path, PlanDigest: artifact.PlanDigest}
}

func failedServiceItem(connection, code string, err error, approval bool, risk string, actions []core.Action) serviceItemResult {
	if actions == nil {
		actions = []core.Action{}
	}
	return serviceItemResult{
		Connection: connection, Outcome: "failed", Changed: false, Conditions: []core.Condition{},
		Error: &serviceItemError{Code: code, Message: err.Error(), RiskLevel: risk, RequiresApproval: approval, Actions: actions},
	}
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
	effectiveDryRun := opts.DryRun && operation != "status"
	outcome := "succeeded"
	if effectiveDryRun {
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
		if err := writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion, "outcome": outcome, "operation": operation,
			"results": results, "dryRun": effectiveDryRun,
		}); err != nil {
			return err
		}
	} else {
		for _, result := range results {
			if result.Error != nil {
				fmt.Fprintf(streams.Err, "%s: failed: %s\n", result.Connection, result.Error.Message)
				continue
			}
			if result.State != nil {
				fmt.Fprintf(streams.Out, "%s\t%s\t%s\t%s\t%s\n", result.Connection, result.State.DesiredState, result.State.ProcessState, result.State.Health, result.State.SyncState)
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
	if opts.JSON && follow {
		return usageError("--json cannot be combined with --follow")
	}
	adapter, _, _, document, err := serviceDependencies(opts)
	if err != nil {
		return err
	}
	if _, connection := configstore.Find(&document, name); connection == nil {
		return notFoundError(fmt.Sprintf("no such connection: %s", name))
	}
	if !opts.JSON {
		return adapter.Logs(ctx, name, lines, follow, service.Streams{Out: streams.Out, Err: streams.Err})
	}
	var stdout, stderr bytes.Buffer
	if err := adapter.Logs(ctx, name, lines, false, service.Streams{Out: &stdout, Err: &stderr}); err != nil {
		return err
	}
	logs := append(splitLogLines(stdout.String()), splitLogLines(stderr.String())...)
	return writeJSON(streams.Out, map[string]any{
		"contractVersion": ContractVersion, "outcome": "succeeded", "connection": name, "logs": logs,
	})
}

func splitLogLines(value string) []string {
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return []string{}
	}
	return strings.Split(value, "\n")
}
