package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tunlite/tunlite/internal/configstore"
	"github.com/tunlite/tunlite/internal/core"
	sshdriver "github.com/tunlite/tunlite/internal/drivers/ssh"
	"github.com/tunlite/tunlite/internal/service"
)

const exitPartial = 5

type itemResult struct {
	Connection string        `json:"connection"`
	Outcome    string        `json:"outcome"`
	Code       string        `json:"code,omitempty"`
	Message    string        `json:"message,omitempty"`
	RiskLevel  string        `json:"riskLevel,omitempty"`
	Actions    []core.Action `json:"actions,omitempty"`
}

func (result itemResult) MarshalJSON() ([]byte, error) {
	type wireResult struct {
		Connection string         `json:"connection"`
		Outcome    string         `json:"outcome"`
		Code       string         `json:"code,omitempty"`
		Message    string         `json:"message,omitempty"`
		RiskLevel  string         `json:"riskLevel,omitempty"`
		Actions    *[]core.Action `json:"actions,omitempty"`
	}
	wire := wireResult{
		Connection: result.Connection, Outcome: result.Outcome,
		Code: result.Code, Message: result.Message, RiskLevel: result.RiskLevel,
	}
	if result.Outcome == "failed" {
		actions := result.Actions
		if actions == nil {
			actions = []core.Action{}
		}
		wire.Actions = &actions
	}
	return json.Marshal(wire)
}

func failedImportItem(connection, code string, err error) itemResult {
	return itemResult{
		Connection: connection, Outcome: "failed", Code: code, Message: err.Error(),
		RiskLevel: "low", Actions: []core.Action{},
	}
}

func recoveryBlockedImportItem(connection string, cause, recovery error) itemResult {
	return itemResult{
		Connection: connection, Outcome: "failed", Code: "recovery_blocked",
		Message:   errors.Join(cause, fmt.Errorf("automatic recovery failed: %w", recovery)).Error(),
		RiskLevel: "high", Actions: resolveActions(connection),
	}
}

func exportConnections(ctx context.Context, args []string, streams IO, opts options) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var output string
	var force bool
	flags.StringVar(&output, "output", "", "output file")
	flags.BoolVar(&force, "force", false, "replace output file")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	if opts.DryRun && (output == "" || output == "-") {
		return usageError("export --dry-run requires --output <file>")
	}
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	if output == "" {
		if opts.JSON {
			return writeResponse(streams.Out, map[string]any{
				"outcome":  "succeeded",
				"document": document,
			})
		}
		return writeJSON(streams.Out, document)
	}
	if output == "-" {
		if opts.JSON {
			return usageError("--json cannot be combined with --output -")
		}
		return writeJSON(streams.Out, document)
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(absolute); err == nil {
			return conflictError(fmt.Sprintf("config file already exists: %s (use --force to replace it)", absolute))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if opts.DryRun {
		return renderDeclarationWrite(streams, opts, "planned", absolute, len(document.Items))
	}
	if err := configstore.New(absolute).Write(ctx, document, force); err != nil {
		if errors.Is(err, configstore.ErrExists) {
			return conflictError(err.Error() + " (use --force to replace it)")
		}
		return err
	}
	return renderDeclarationWrite(streams, opts, "applied", absolute, len(document.Items))
}

func importConnections(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite import <file> [--force]")
	}
	sourceArgument := args[0]
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var force bool
	flags.BoolVar(&force, "force", false, "replace same-name declarations")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	sourcePath, err := filepath.Abs(sourceArgument)
	if err != nil {
		return err
	}
	if _, err := os.Stat(sourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notFoundError(fmt.Sprintf("import file not found: %s", sourcePath))
		}
		return err
	}
	renderStageProgress(streams, opts,
		fmt.Sprintf("Reading and validating import file %s…", sourcePath),
		fmt.Sprintf("正在读取并校验导入文件 %s…", sourcePath))
	source, sourceFormat, sourceConditions, err := loadImportDocument(sourcePath)
	if err != nil {
		return err
	}
	for _, imported := range source.Items {
		if imported.Spec.Driver != sshdriver.ID || validateKnownDriver(imported) != nil {
			continue
		}
		risks, riskErr := connectionRiskConditions(ctx, imported, opts.NonInteractive)
		if riskErr != nil {
			return riskErr
		}
		sourceConditions = append(sourceConditions, risks...)
	}
	target, err := connectionStore(opts)
	if err != nil {
		return err
	}
	renderCommandProgress(streams, opts, []string{"import", sourcePath})
	results := make([]itemResult, 0, len(source.Items))
	if opts.DryRun {
		document, err := target.Load()
		if err != nil {
			return err
		}
		for _, imported := range source.Items {
			name := imported.Metadata.Name
			if err := validateKnownDriver(imported); err != nil {
				results = append(results, failedImportItem(name, "invalid_driver_config", err))
				continue
			}
			_, existing := configstore.Find(&document, name)
			if existing != nil && !force {
				results = append(results, failedImportItem(name, "already_exists", errors.New("same-name connection already exists")))
				continue
			}
			if existing != nil && existing.Spec.Driver != imported.Spec.Driver {
				results = append(results, failedImportItem(name, "driver_change_unsupported", errors.New("remove or unmanage the existing connection before changing its driver")))
				continue
			}
			var serviceConditions []condition
			if imported.Spec.Driver == sshdriver.ID {
				if existing == nil {
					serviceConditions, err = previewNewConnectionService(ctx, imported, target.Path(), opts)
				} else {
					serviceConditions, err = previewUpdatedConnectionService(ctx, cloneConnection(*existing), imported, target.Path(), opts)
				}
				if err != nil {
					results = append(results, failedImportItem(name, "service_update_failed", err))
					continue
				}
				sourceConditions = append(sourceConditions, serviceConditions...)
			}
			outcome := "added"
			if existing != nil {
				outcome = "updated"
			}
			results = append(results, itemResult{Connection: name, Outcome: outcome})
		}
		return renderImportResult(streams, opts, sourcePath, sourceFormat, sourceConditions, results)
	}
	approvedSystemReplacements, err := preflightImportSystemReplacements(ctx, source, target, force, streams, opts)
	if err != nil {
		return err
	}
	if len(approvedSystemReplacements) > 0 {
		opts.systemReplacementBatch = approvedSystemReplacements
	}
	for _, imported := range source.Items {
		name := imported.Metadata.Name
		if err := validateKnownDriver(imported); err != nil {
			results = append(results, failedImportItem(name, "invalid_driver_config", err))
			continue
		}
		document, err := target.Load()
		if err != nil {
			results = append(results, failedImportItem(name, "declaration_read_failed", err))
			continue
		}
		index, existing := configstore.Find(&document, name)
		if existing != nil && !force {
			results = append(results, failedImportItem(name, "already_exists", errors.New("same-name connection already exists")))
			continue
		}
		if existing != nil && existing.Spec.Driver != imported.Spec.Driver {
			results = append(results, failedImportItem(name, "driver_change_unsupported", errors.New("remove or unmanage the existing connection before changing its driver")))
			continue
		}
		copy := cloneConnection(imported)
		mutation := noServiceMutation()
		if copy.Spec.Driver == sshdriver.ID {
			if existing == nil {
				mutation, err = stageNewConnectionService(ctx, copy, target.Path(), opts)
			} else {
				mutation, err = stageUpdatedConnectionService(ctx, cloneConnection(*existing), copy, target.Path(), opts, streams)
			}
			if err != nil {
				results = append(results, failedImportItem(name, serviceOperationErrorCode(err, "service_update_failed"), err))
				continue
			}
			sourceConditions = append(sourceConditions, mutation.conditions...)
		}
		applyErr := target.Update(ctx, func(current *configstore.Document) error {
			currentIndex, currentExisting := configstore.Find(current, name)
			if existing == nil {
				if currentExisting != nil {
					return configstore.ErrChanged
				}
				current.Items = append(current.Items, copy)
				return nil
			}
			if currentExisting == nil || !connectionsEqual(*currentExisting, *existing) {
				return configstore.ErrChanged
			}
			current.Items[currentIndex] = copy
			return nil
		})
		if applyErr != nil {
			applyErr = mutation.rollbackAfter(classifyConcurrentChange(applyErr))
			mutation.finish()
			var typed *exitError
			if errors.As(applyErr, &typed) && typed.kind == "recovery_blocked" {
				results = append(results, itemResult{
					Connection: name, Outcome: "failed", Code: typed.kind, Message: typed.Error(),
					RiskLevel: typed.riskLevel, Actions: typed.actions,
				})
			} else {
				code := "import_apply_failed"
				if errors.As(applyErr, &typed) && typed.kind == "state_conflict" {
					code = typed.kind
				}
				results = append(results, failedImportItem(name, code, applyErr))
			}
			continue
		}
		commitErr := mutation.commit()
		if commitErr != nil {
			recoveryCtx, cancel := compensationContext(ctx)
			restoreErr := target.Update(recoveryCtx, func(current *configstore.Document) error {
				currentIndex, currentValue := configstore.Find(current, name)
				if currentValue == nil || !connectionsEqual(*currentValue, copy) {
					return configstore.ErrChanged
				}
				if existing == nil {
					current.Items = append(current.Items[:currentIndex], current.Items[currentIndex+1:]...)
				} else {
					current.Items[currentIndex] = cloneConnection(*existing)
				}
				return nil
			})
			recoveryErr := errors.Join(restoreErr, mutation.rollback())
			cancel()
			if recoveryErr != nil {
				results = append(results, recoveryBlockedImportItem(name, commitErr, recoveryErr))
			} else {
				results = append(results, failedImportItem(name, "service_state_commit_failed", commitErr))
			}
			mutation.finish()
			continue
		}
		mutation.finish()
		outcome := "added"
		if index >= 0 {
			outcome = "updated"
		}
		results = append(results, itemResult{Connection: name, Outcome: outcome})
	}
	return renderImportResult(streams, opts, sourcePath, sourceFormat, sourceConditions, results)
}

type plannedSystemReplacement struct {
	name      string
	reference service.Reference
	artifact  service.Artifact
}

type systemReplacementCandidate struct {
	existing core.Connection
	imported core.Connection
	record   service.Record
}

func preflightImportSystemReplacements(
	ctx context.Context,
	source configstore.Document,
	target *configstore.Store,
	force bool,
	streams IO,
	opts options,
) (map[string]bool, error) {
	if !force {
		return map[string]bool{}, nil
	}
	document, err := target.Load()
	if err != nil {
		return nil, err
	}
	records, err := serviceRecordStore()
	if err != nil {
		return nil, err
	}
	candidates := make([]systemReplacementCandidate, 0)
	for _, imported := range source.Items {
		_, existing := configstore.Find(&document, imported.Metadata.Name)
		if existing == nil || existing.Spec.Driver != sshdriver.ID || imported.Spec.Driver != sshdriver.ID {
			continue
		}
		record, exists, err := records.Load(imported.Metadata.Name)
		if err != nil {
			return nil, err
		}
		if !exists || service.NormalizeScope(record.Scope) != service.ScopeSystem || record.Mode != service.ModePersistent {
			continue
		}
		if err := ensureDeclarationOwner(record, target.Path()); err != nil {
			return nil, err
		}
		newPlan, err := serviceExecutionPlan(ctx, imported)
		if err != nil {
			return nil, err
		}
		newDigest, err := service.PlanDigest(newPlan)
		if err != nil {
			return nil, err
		}
		if record.PlanDigest == newDigest {
			continue
		}
		candidates = append(candidates, systemReplacementCandidate{
			existing: cloneConnection(*existing), imported: cloneConnection(imported), record: record,
		})
	}
	if len(candidates) == 0 {
		return map[string]bool{}, nil
	}
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return nil, err
	}
	planned := make([]plannedSystemReplacement, 0, len(candidates))
	for _, candidate := range candidates {
		oldArtifact, err := buildBoundServiceArtifact(ctx, adapter, candidate.existing, &candidate.record)
		if err != nil {
			return nil, err
		}
		newArtifact, err := buildBoundServiceArtifact(ctx, adapter, candidate.imported, &candidate.record)
		if err != nil {
			return nil, err
		}
		if oldArtifact.PlanDigest == newArtifact.PlanDigest {
			continue
		}
		if state := service.Evaluate(ctx, adapter, candidate.record, &oldArtifact); state.ConsistencyState != service.ConsistencyConsistent {
			return nil, stateConflictError("connection has drifted; resolve it before importing configuration")
		}
		if err := service.CheckDefinitionReplace(ctx, adapter, candidate.record.Reference(), newArtifact); err != nil {
			return nil, err
		}
		planned = append(planned, plannedSystemReplacement{
			name: candidate.imported.Metadata.Name, reference: candidate.record.Reference(), artifact: newArtifact,
		})
	}
	if len(planned) == 0 {
		return map[string]bool{}, nil
	}
	if !opts.NonInteractive {
		if err := opts.privilegeAuthorizer.Validate(ctx, streams, opts.Language, privilegeRequest{connections: len(planned)}); err != nil {
			return nil, err
		}
	}
	for _, item := range planned {
		if err := service.PreflightDefinitionReplace(ctx, adapter, item.reference, item.artifact); err != nil {
			return nil, err
		}
	}
	approved := make(map[string]bool, len(planned))
	for _, item := range planned {
		approved[item.name] = true
	}
	return approved, nil
}

func loadImportDocument(path string) (configstore.Document, string, []core.Condition, error) {
	document, err := configstore.New(path).Load()
	if err != nil {
		return configstore.Document{}, "", nil, err
	}
	return document, "tunlite-v1", []core.Condition{}, nil
}

func validateConnections(args []string, streams IO, opts options) error {
	if len(args) > 1 {
		return usageError("usage: tunlite validate [file]")
	}
	var store *configstore.Store
	if len(args) == 1 {
		path, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return notFoundError(fmt.Sprintf("validation file not found: %s", path))
			}
			return err
		}
		store = configstore.New(path)
	} else {
		var err error
		store, err = connectionStore(opts)
		if err != nil {
			return err
		}
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	conditions := make([]condition, 0)
	failed := false
	for _, connection := range document.Items {
		if err := validateKnownDriver(connection); err != nil {
			failed = true
			conditions = append(conditions, condition{
				Code: "invalid_driver_config", Severity: "error", Connection: connection.Metadata.Name,
				Driver: connection.Spec.Driver, Message: err.Error(), Actions: []core.Action{},
			})
		} else if connection.Spec.Driver != sshdriver.ID {
			conditions = append(conditions, condition{
				Code: "driver_not_installed", Severity: "warning", Connection: connection.Metadata.Name,
				Message: fmt.Sprintf("driver %q is not installed; declaration was not executed", connection.Spec.Driver),
				Driver:  connection.Spec.Driver,
				Actions: []core.Action{{Type: "inspect_driver", Parameters: map[string]any{"driver": connection.Spec.Driver}}},
			})
		}
	}
	sort.Slice(conditions, func(i, j int) bool {
		if conditions[i].Connection == conditions[j].Connection {
			return conditions[i].Code < conditions[j].Code
		}
		return conditions[i].Connection < conditions[j].Connection
	})
	outcome := "succeeded"
	if failed {
		outcome = "failed"
	}
	if opts.JSON {
		_ = writeResponse(streams.Out, map[string]any{
			"outcome": outcome, "path": store.Path(), "connections": len(document.Items), "conditions": conditions,
		})
	} else {
		fmt.Fprintf(streams.Out, "%s: %d connection(s)\n", store.Path(), len(document.Items))
		for _, item := range conditions {
			fmt.Fprintf(streams.Out, "%s: %s: %s\n", item.Severity, item.Connection, item.Message)
		}
	}
	if failed {
		return renderedExit(1)
	}
	return nil
}

func validateKnownDriver(connection core.Connection) error {
	if connection.Spec.Driver != sshdriver.ID {
		return nil
	}
	config, err := sshdriver.DecodeConfig(connection.Spec.Config)
	if err != nil {
		return fmt.Errorf("connection %q: %w", connection.Metadata.Name, err)
	}
	if err := sshdriver.Validate(config); err != nil {
		return fmt.Errorf("connection %q: %w", connection.Metadata.Name, err)
	}
	return nil
}

func renderDeclarationWrite(streams IO, opts options, outcome, path string, count int) error {
	if opts.JSON {
		return writeResponse(streams.Out, map[string]any{
			"outcome": outcome, "path": path,
			"connections": count, "dryRun": opts.DryRun,
		})
	}
	verb := "exported"
	if opts.DryRun {
		verb = "would export"
	}
	fmt.Fprintf(streams.Out, "%s %d connection(s) to %s\n", verb, count, path)
	return nil
}

func renderImportResult(streams IO, opts options, source, sourceFormat string, conditions []core.Condition, results []itemResult) error {
	failed := 0
	applied := 0
	interactionFailuresOnly := len(results) > 0
	for _, result := range results {
		if result.Outcome == "failed" {
			failed++
			if result.Code != "privilege_required" && result.Code != "interaction_required" {
				interactionFailuresOnly = false
			}
		} else {
			applied++
			interactionFailuresOnly = false
		}
	}
	outcome := "applied"
	if opts.DryRun {
		outcome = "planned"
	}
	if failed > 0 {
		if applied > 0 {
			outcome = "partial"
		} else {
			outcome = "failed"
		}
	}
	if opts.JSON {
		_ = writeResponse(streams.Out, map[string]any{
			"outcome": outcome, "source": source, "sourceFormat": sourceFormat,
			"conditions": conditions, "results": results, "dryRun": opts.DryRun,
		})
	} else {
		for _, result := range results {
			fmt.Fprintf(streams.Out, "%s: %s", result.Connection, result.Outcome)
			if result.Message != "" {
				fmt.Fprintf(streams.Out, " (%s)", result.Message)
			}
			fmt.Fprintln(streams.Out)
		}
		for _, item := range conditions {
			if item.Connection == "" {
				fmt.Fprintf(streams.Out, "%s: %s\n", item.Severity, item.Message)
			} else {
				fmt.Fprintf(streams.Out, "%s: %s: %s\n", item.Severity, item.Connection, item.Message)
			}
		}
	}
	if failed > 0 && applied > 0 {
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
