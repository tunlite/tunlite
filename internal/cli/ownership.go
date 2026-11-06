package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tunlite/tunlite/internal/configstore"
	"github.com/tunlite/tunlite/internal/core"
	sshdriver "github.com/tunlite/tunlite/internal/drivers/ssh"
	"github.com/tunlite/tunlite/internal/service"
)

func manageConnection(ctx context.Context, args []string, streams IO, opts options) error {
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("manage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var name string
	flags.StringVar(&name, "name", "", "Tunlite connection name")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError("usage: tunlite manage [native-service] [--name name]")
	}
	if target == "" && (opts.NonInteractive || !streamsCanPromptOnStderr(streams)) {
		return interactionRequiredError("manage without an explicit native service requires an interactive terminal")
	}
	progressArgs := []string{"manage"}
	if target != "" {
		progressArgs = append(progressArgs, target)
	}
	renderCommandProgress(streams, opts, progressArgs)
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return capabilityError(err.Error())
	}
	candidates, err := adapter.Discover(ctx)
	if err != nil {
		return err
	}
	records, err := serviceRecordStore()
	if err != nil {
		return err
	}
	candidates, err = filterManageCandidates(candidates, records, target == "")
	if err != nil {
		return err
	}
	candidate, err := chooseCandidate(candidates, target, streams, opts)
	if err != nil {
		return err
	}
	if name == "" {
		name = candidate.Reference.Connection
	}
	sshConfig, err := sshdriver.ConfigFromPlan(candidate.Plan)
	if err != nil {
		return capabilityError(fmt.Sprintf("native service %s cannot be managed safely: %v", candidate.Reference.UnitName, err))
	}
	raw, err := json.Marshal(sshConfig)
	if err != nil {
		return err
	}
	connection := core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: name},
		Spec: core.ConnectionSpec{Driver: sshdriver.ID, Config: raw},
	}
	if err := core.ValidateConnection(connection); err != nil {
		return usageError(err.Error())
	}
	canonicalPlan, err := sshdriver.NewWithLookup(func(string) (string, error) {
		return candidate.Plan.Exec.Program, nil
	}).Plan(ctx, connection, core.PlanOptions{NonInteractive: true})
	if err != nil {
		return err
	}
	conditions := planRiskConditions(connection, canonicalPlan)
	config, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := config.Load()
	if err != nil {
		return err
	}
	if _, existing := configstore.Find(&document, name); existing != nil {
		return conflictError(fmt.Sprintf("a connection named %q already exists", name))
	}
	if bound, exists, err := records.FindBinding(candidate.Reference, name); err != nil {
		return err
	} else if exists {
		return conflictError(fmt.Sprintf("native service %s is already managed as connection %q", candidate.Reference.UnitName, bound.Connection))
	}
	if opts.DryRun {
		return renderConnectionMutationWithConditions(streams, opts, "manage", "planned", connection, conditions)
	}
	release := func() {}
	if !opts.DryRun {
		release, err = records.Lock(ctx, name)
		if err != nil {
			return err
		}
	}
	defer release()
	releaseBindings, err := records.LockBindings(ctx)
	if err != nil {
		return err
	}
	defer releaseBindings()
	if _, exists, err := records.Load(name); err != nil {
		return err
	} else if exists {
		return conflictError(fmt.Sprintf("service state for %q already exists", name))
	}
	if bound, exists, err := records.FindBinding(candidate.Reference, name); err != nil {
		return err
	} else if exists {
		return conflictError(fmt.Sprintf("native service %s is already managed as connection %q", candidate.Reference.UnitName, bound.Connection))
	}
	refreshed, err := findCandidateByReference(ctx, adapter, candidate.Reference)
	if err != nil {
		return err
	}
	oldPlanDigest, oldDigestErr := service.PlanDigest(candidate.Plan)
	newPlanDigest, newDigestErr := service.PlanDigest(refreshed.Plan)
	if oldDigestErr != nil || newDigestErr != nil {
		return errors.Join(oldDigestErr, newDigestErr)
	}
	if oldPlanDigest != newPlanDigest || service.ContentDigest(candidate.Observation.Artifact) != service.ContentDigest(refreshed.Observation.Artifact) ||
		observedMode(candidate.Observation) != observedMode(refreshed.Observation) || candidateScope(candidate) != candidateScope(refreshed) {
		return stateConflictError("native service changed while it was being selected; retry manage")
	}
	candidate = refreshed
	digest, err := service.PlanDigest(canonicalPlan)
	if err != nil {
		return err
	}
	record := service.Record{
		RecordRevision: service.CurrentRecordRevision, Connection: name, ServiceKey: candidate.Reference.Connection,
		Adapter: adapter.Name(), Scope: candidateScope(candidate), Mode: observedMode(candidate.Observation),
		ArtifactPath: candidate.Reference.ArtifactPath, UnitName: candidate.Reference.UnitName,
		LogPaths: append([]string(nil), candidate.Reference.LogPaths...), PlanDigest: digest,
		BaselineDigest: service.ContentDigest(candidate.Observation.Artifact), DeclarationPath: config.Path(), Origin: "system",
	}
	if err := records.Write(ctx, record); err != nil {
		return err
	}
	if err := config.Update(ctx, func(document *configstore.Document) error {
		if _, existing := configstore.Find(document, name); existing != nil {
			return conflictError(fmt.Sprintf("a connection named %q already exists", name))
		}
		document.Items = append(document.Items, connection)
		return nil
	}); err != nil {
		return recoveryBlockedError(err, records.Remove(name), []core.Action{{
			Type: "inspect_service_manager", Parameters: map[string]any{"connection": name},
		}})
	}
	return renderConnectionMutationWithConditions(streams, opts, "manage", "applied", connection, conditions)
}

// filterManageCandidates keeps the interactive picker honest: it only shows
// native services that the built-in Driver can reconstruct without guessing,
// and it hides services that Tunlite already owns. An explicit target is kept
// long enough to return the more useful "already managed" conflict below.
func filterManageCandidates(candidates []service.Candidate, records *service.Store, excludeManaged bool) ([]service.Candidate, error) {
	result := make([]service.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := sshdriver.ConfigFromPlan(candidate.Plan); err != nil {
			continue
		}
		if excludeManaged {
			if _, exists, err := records.FindBinding(candidate.Reference, ""); err != nil {
				return nil, err
			} else if exists {
				continue
			}
		}
		result = append(result, candidate)
	}
	return result, nil
}

func chooseCandidate(candidates []service.Candidate, target string, streams IO, opts options) (service.Candidate, error) {
	if target != "" {
		for _, candidate := range candidates {
			if target == candidate.Reference.UnitName || target == candidate.Reference.ArtifactPath || target == candidate.Reference.Connection {
				return candidate, nil
			}
		}
		return service.Candidate{}, notFoundError(fmt.Sprintf("no manageable native service matches %q", target))
	}
	if len(candidates) == 0 {
		return service.Candidate{}, notFoundError("no manageable native services were found")
	}
	if opts.NonInteractive || !streamsCanPromptOnStderr(streams) {
		return service.Candidate{}, interactionRequiredError("manage without an explicit native service requires an interactive terminal")
	}
	for index, candidate := range candidates {
		fmt.Fprintf(streams.Err, "%d. %s (%s)\n", index+1, candidate.Reference.UnitName, observedMode(candidate.Observation))
	}
	fmt.Fprint(streams.Err, "select a service to manage: ")
	scanner := bufio.NewScanner(streams.In)
	if !scanner.Scan() {
		return service.Candidate{}, errors.New("no service selected")
	}
	choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || choice < 1 || choice > len(candidates) {
		return service.Candidate{}, usageError("invalid service selection")
	}
	return candidates[choice-1], nil
}

func unmanageConnection(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) != 1 {
		return usageError("usage: tunlite unmanage <name>")
	}
	name := args[0]
	config, err := connectionStore(opts)
	if err != nil {
		return err
	}
	records, err := serviceRecordStore()
	if err != nil {
		return err
	}
	release := func() {}
	if !opts.DryRun {
		release, err = records.Lock(ctx, name)
		if err != nil {
			return err
		}
	}
	defer release()
	if records.RunActive(name) {
		return stateConflictError("connection is running in the foreground; stop that run before unmanaging it")
	}
	document, err := config.Load()
	if err != nil {
		return err
	}
	connectionIndex, connection := configstore.Find(&document, name)
	if connection == nil {
		return notFoundError(fmt.Sprintf("no such connection: %s", name))
	}
	record, exists, err := records.Load(name)
	if err != nil {
		return err
	}
	if !exists {
		return capabilityError("connection is foreground-only and has no native service to hand back")
	}
	if err := ensureDeclarationOwner(record, config.Path()); err != nil {
		return err
	}
	renderCommandProgress(streams, opts, []string{"unmanage", name})
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return capabilityError(err.Error())
	}
	artifact, err := buildBoundServiceArtifact(ctx, adapter, *connection, &record)
	if err != nil {
		return err
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	if state.ConsistencyState != service.ConsistencyConsistent {
		return stateConflictError("connection is not consistent; resolve it before unmanaging it")
	}
	if opts.DryRun {
		return renderUnmanageResult(streams, opts, "planned", *connection)
	}
	removed := cloneConnection(*connection)
	if err := config.Update(ctx, func(document *configstore.Document) error {
		currentIndex, current := configstore.Find(document, name)
		if current == nil {
			return notFoundError(fmt.Sprintf("no such connection: %s", name))
		}
		if !connectionsEqual(*current, removed) {
			return configstore.ErrChanged
		}
		document.Items = append(document.Items[:currentIndex], document.Items[currentIndex+1:]...)
		return nil
	}); err != nil {
		return classifyConcurrentChange(err)
	}
	if err := completeUnmanage(ctx, config, name, connectionIndex, removed, records.Remove); err != nil {
		return err
	}
	return renderUnmanageResult(streams, opts, "applied", removed)
}

func renderUnmanageResult(streams IO, opts options, outcome string, connection core.Connection) error {
	if opts.JSON {
		return renderConnectionMutation(streams, opts, "unmanage", outcome, connection)
	}
	if opts.DryRun {
		fmt.Fprintf(streams.Out, localized(opts.Language,
			"Would stop managing %q. The native service would remain installed; its running state and autostart setting would stay unchanged.\n",
			"将停止管理“%s”。原生后台服务仍会保持安装，运行状态和自动启动设置不变。\n"), connection.Metadata.Name)
		return nil
	}
	fmt.Fprintf(streams.Out, localized(opts.Language,
		"Stopped managing %q. The native service remains installed; its running state and autostart setting are unchanged.\n",
		"已停止管理“%s”。原生后台服务仍保持安装，运行状态和自动启动设置不变。\n"), connection.Metadata.Name)
	return nil
}

func completeUnmanage(ctx context.Context, config *configstore.Store, name string, connectionIndex int, removed core.Connection, removeRecord func(string) error) error {
	if err := removeRecord(name); err != nil {
		// Restore the declaration because management has not actually been handed back.
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		restoreErr := config.Update(recoveryCtx, func(document *configstore.Document) error {
			if _, current := configstore.Find(document, name); current != nil {
				return configstore.ErrChanged
			}
			index := connectionIndex
			if index > len(document.Items) {
				index = len(document.Items)
			}
			document.Items = append(document.Items, core.Connection{})
			copy(document.Items[index+1:], document.Items[index:])
			document.Items[index] = removed
			return nil
		})
		return recoveryBlockedError(err, restoreErr, resolveActions(name))
	}
	return nil
}

func resolveConnection(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite resolve <name> --use tunlite|system")
	}
	name := args[0]
	flags := flag.NewFlagSet("resolve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var source string
	flags.StringVar(&source, "use", "", "source of truth: tunlite or system")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 || (source != "tunlite" && source != "system") {
		return usageError("usage: tunlite resolve <name> --use tunlite|system")
	}
	adapter, records, config, _, err := serviceDependencies(opts)
	if err != nil {
		return capabilityError(err.Error())
	}
	release := func() {}
	if !opts.DryRun {
		release, err = records.Lock(ctx, name)
		if err != nil {
			return err
		}
	}
	defer release()
	if records.RunActive(name) {
		return stateConflictError("connection is running in the foreground; stop that run before resolving it")
	}
	document, err := config.Load()
	if err != nil {
		return err
	}
	_, connection := configstore.Find(&document, name)
	if connection == nil {
		return notFoundError(fmt.Sprintf("no such connection: %s", name))
	}
	renderCommandProgress(streams, opts, []string{"resolve", name})
	record, err := resolvableServiceRecord(ctx, adapter, records, *connection, config.Path(), source)
	if err != nil {
		return err
	}
	if source == "tunlite" {
		artifact, err := buildBoundServiceArtifact(ctx, adapter, *connection, &record)
		if err != nil {
			return err
		}
		conditions := planRiskConditions(*connection, artifact.Plan)
		if opts.DryRun {
			return renderConnectionMutationWithConditions(streams, opts, "resolve", "planned", *connection, conditions)
		}
		before, err := adapter.Inspect(ctx, record.Reference())
		if err != nil {
			return err
		}
		if service.NormalizeScope(record.Scope) == service.ScopeSystem && record.Mode == service.ModeStopped && !before.Installed {
			record.PlanDigest = artifact.PlanDigest
			record.BaselineDigest = service.ContentDigest(artifact.Content)
			record.ArtifactPath = ""
			if err := records.Write(ctx, record); err != nil {
				return err
			}
			return renderConnectionMutation(streams, opts, "resolve", "applied", *connection)
		}
		if service.NormalizeScope(record.Scope) == service.ScopeSystem && record.Mode == service.ModeStopped && before.Installed {
			if !opts.NonInteractive {
				if err := opts.privilegeAuthorizer.Validate(ctx, streams, opts.Language, privilegeRequest{connections: 1}); err != nil {
					return err
				}
			}
			if err := service.PreflightDisable(ctx, adapter, record.Reference()); err != nil {
				return err
			}
			previousArtifact := service.Artifact{
				Connection: record.Reference().Connection, Adapter: record.Adapter, Scope: service.ScopeSystem,
				UnitName: record.UnitName, Path: record.ArtifactPath, Content: append([]byte(nil), before.Artifact...),
				PlanDigest: record.PlanDigest, LogPaths: append([]string(nil), record.LogPaths...),
			}
			if err := adapter.Disable(ctx, record.Reference()); err != nil {
				return err
			}
			oldRecord := record
			record.PlanDigest = artifact.PlanDigest
			record.BaselineDigest = service.ContentDigest(artifact.Content)
			record.ArtifactPath = ""
			if err := records.Write(ctx, record); err != nil {
				recoveryCtx, cancel := compensationContext(ctx)
				defer cancel()
				return recoveryBlockedError(err,
					errors.Join(adapter.Materialize(recoveryCtx, previousArtifact), records.Write(recoveryCtx, oldRecord)),
					resolveActions(name))
			}
			return renderConnectionMutation(streams, opts, "resolve", "applied", *connection)
		}
		previousArtifact := service.Artifact{
			Connection: record.Reference().Connection, Adapter: record.Adapter, Scope: service.NormalizeScope(record.Scope),
			UnitName: record.UnitName, Path: record.ArtifactPath, Content: append([]byte(nil), before.Artifact...),
			PlanDigest: record.PlanDigest, LogPaths: append([]string(nil), record.LogPaths...),
		}
		previousMode := observedMode(before)
		if service.NormalizeScope(record.Scope) == service.ScopeSystem && record.Mode == service.ModePersistent {
			if err := service.CheckDefinitionReplace(ctx, adapter, record.Reference(), artifact); err != nil {
				return err
			}
			renderSystemDefinitionProgress(streams, opts, name, "authorize")
			if !opts.NonInteractive {
				if err := opts.privilegeAuthorizer.Validate(ctx, streams, opts.Language, privilegeRequest{connections: 1}); err != nil {
					return err
				}
			}
			if err := service.PreflightDefinitionReplace(ctx, adapter, record.Reference(), artifact); err != nil {
				return err
			}
			renderSystemDefinitionProgress(streams, opts, name, "replace")
			transition, err := service.StageDefinitionReplace(ctx, adapter, record.Reference(), artifact, service.ModePersistent,
				transitionHealthVerifierWithProgress(artifact, streams, opts, name))
			if err != nil {
				return err
			}
			oldRecord := record
			record.PlanDigest = artifact.PlanDigest
			record.BaselineDigest = service.ContentDigest(artifact.Content)
			if err := records.Write(ctx, record); err != nil {
				recoveryCtx, cancel := service.DefinitionReplaceRecoveryContext(ctx)
				defer cancel()
				return recoveryBlockedError(err, transition.Rollback(recoveryCtx), resolveActions(name))
			}
			renderSystemDefinitionProgress(streams, opts, name, "ready")
			if err := transition.Commit(ctx); err != nil {
				recoveryCtx, cancel := compensationContext(ctx)
				defer cancel()
				return recoveryBlockedError(err, records.Write(recoveryCtx, oldRecord), resolveActions(name))
			}
			return renderConnectionMutation(streams, opts, "resolve", "applied", *connection)
		}
		rollback := func(cause error) error {
			recoveryCtx, cancel := compensationContext(ctx)
			defer cancel()
			var restoreErr error
			if before.Installed {
				restoreErr = adapter.Materialize(recoveryCtx, previousArtifact)
			} else {
				restoreErr = adapter.Remove(recoveryCtx, record.Reference())
			}
			var modeErr error
			if before.Installed {
				modeErr = applyNativeMode(recoveryCtx, adapter, record.Reference(), previousMode)
			}
			return recoveryBlockedError(cause, errors.Join(restoreErr, modeErr), resolveActions(name))
		}
		if err := adapter.Materialize(ctx, artifact); err != nil {
			return err
		}
		if err := applyNativeMode(ctx, adapter, record.Reference(), record.Mode); err != nil {
			return rollback(err)
		}
		record.PlanDigest = artifact.PlanDigest
		record.BaselineDigest = service.ContentDigest(artifact.Content)
		if err := records.Write(ctx, record); err != nil {
			return rollback(err)
		}
		return renderConnectionMutation(streams, opts, "resolve", "applied", *connection)
	}
	candidate, err := findBoundCandidate(ctx, adapter, record)
	if err != nil {
		return err
	}
	sshConfig, err := sshdriver.ConfigFromPlan(candidate.Plan)
	if err != nil {
		return capabilityError(fmt.Sprintf("system definition cannot be mapped losslessly: %v", err))
	}
	raw, err := json.Marshal(sshConfig)
	if err != nil {
		return err
	}
	updated := cloneConnection(*connection)
	updated.Spec.Config = raw
	canonicalArtifact, err := buildBoundServiceArtifact(ctx, adapter, updated, &record)
	if err != nil {
		return err
	}
	if opts.DryRun {
		return renderConnectionMutation(streams, opts, "resolve", "planned", updated)
	}
	previous := cloneConnection(*connection)
	if err := config.Update(ctx, func(document *configstore.Document) error {
		_, current := configstore.Find(document, name)
		if current == nil {
			return notFoundError(fmt.Sprintf("no such connection: %s", name))
		}
		if !connectionsEqual(*current, previous) {
			return configstore.ErrChanged
		}
		*current = updated
		return nil
	}); err != nil {
		return classifyConcurrentChange(err)
	}
	record.Mode = observedMode(candidate.Observation)
	record.PlanDigest = canonicalArtifact.PlanDigest
	record.BaselineDigest = service.ContentDigest(candidate.Observation.Artifact)
	if err := writeResolvedRecord(ctx, config, name, previous, updated, record, records.Write); err != nil {
		return err
	}
	return renderConnectionMutation(streams, opts, "resolve", "applied", updated)
}

func writeResolvedRecord(ctx context.Context, config *configstore.Store, name string, previous, updated core.Connection, record service.Record, writeRecord func(context.Context, service.Record) error) error {
	if err := writeRecord(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		restoreErr := config.Update(recoveryCtx, func(document *configstore.Document) error {
			_, current := configstore.Find(document, name)
			if current == nil || !connectionsEqual(*current, updated) {
				return configstore.ErrChanged
			}
			*current = previous
			return nil
		})
		return recoveryBlockedError(err, restoreErr, resolveActions(name))
	}
	return nil
}

func resolvableServiceRecord(ctx context.Context, adapter service.Adapter, records *service.Store, connection core.Connection, declarationPath, source string) (service.Record, error) {
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return service.Record{}, err
	}
	if exists {
		if err := ensureDeclarationOwner(record, declarationPath); err != nil {
			return service.Record{}, err
		}
		if record.Adapter != "" && record.Adapter != adapter.Name() {
			return service.Record{}, stateConflictError(fmt.Sprintf("connection is bound to %s but this machine uses %s", record.Adapter, adapter.Name()))
		}
		return record, nil
	}
	artifact, err := buildServiceArtifact(ctx, adapter, connection)
	if err != nil {
		return service.Record{}, err
	}
	candidates, err := nativeIdentityCandidates(ctx, adapter, artifact)
	if err != nil {
		return service.Record{}, err
	}
	if len(candidates) > 1 {
		scopes := make([]service.Scope, 0, len(candidates))
		for _, candidate := range candidates {
			scopes = append(scopes, candidateScope(candidate))
		}
		return service.Record{}, &unboundNativeIdentityError{connection: connection.Metadata.Name, unit: artifact.UnitName, scopes: scopes}
	}
	if len(candidates) == 1 {
		candidate := candidates[0]
		scope := candidateScope(candidate)
		if !service.SupportsScope(adapter, scope) {
			return service.Record{}, &service.UnsupportedScopeError{Adapter: adapter.Name(), Scope: scope}
		}
		if bound, found, findErr := records.FindBinding(candidate.Reference, connection.Metadata.Name); findErr != nil {
			return service.Record{}, findErr
		} else if found {
			return service.Record{}, conflictError(fmt.Sprintf("native service %s is already managed as connection %q", candidate.Reference.UnitName, bound.Connection))
		}
		serviceKey := candidate.Reference.Connection
		if serviceKey == "" {
			serviceKey = connection.Metadata.Name
		}
		return service.Record{
			RecordRevision: service.CurrentRecordRevision, Connection: connection.Metadata.Name, ServiceKey: serviceKey,
			Adapter: adapter.Name(), Scope: scope, Mode: observedMode(candidate.Observation),
			ArtifactPath: candidate.Reference.ArtifactPath, UnitName: candidate.Reference.UnitName,
			LogPaths:       append([]string(nil), candidate.Reference.LogPaths...),
			BaselineDigest: service.ContentDigest(candidate.Observation.Artifact), DeclarationPath: declarationPath, Origin: "recovered",
		}, nil
	}
	observation, err := adapter.Inspect(ctx, artifact.Reference())
	if err != nil {
		return service.Record{}, err
	}
	if source == "system" && !observation.Installed {
		return service.Record{}, capabilityError("native service is missing; there is no system definition to use")
	}
	if bound, found, err := records.FindBinding(artifact.Reference(), connection.Metadata.Name); err != nil {
		return service.Record{}, err
	} else if found {
		return service.Record{}, conflictError(fmt.Sprintf("native service %s is already managed as connection %q", artifact.UnitName, bound.Connection))
	}
	record = recordForArtifact(artifact, service.ModeStopped, declarationPath)
	record.Origin = "recovered"
	if observation.Installed {
		record.Mode = observedMode(observation)
		record.BaselineDigest = service.ContentDigest(observation.Artifact)
	}
	return record, nil
}

func applyNativeMode(ctx context.Context, adapter service.Adapter, reference service.Reference, mode string) error {
	switch mode {
	case service.ModeStopped:
		observation, err := adapter.Inspect(ctx, reference)
		if err != nil {
			return err
		}
		if observation.ProcessState == service.ProcessInactive && !observation.AutoStart {
			return nil
		}
		return adapter.Disable(ctx, reference)
	case service.ModeBackground:
		if err := adapter.Disable(ctx, reference); err != nil {
			return err
		}
		return adapter.Start(ctx, reference)
	case service.ModePersistent:
		if err := adapter.Disable(ctx, reference); err != nil {
			return err
		}
		return adapter.Enable(ctx, reference)
	default:
		return fmt.Errorf("unsupported native mode %q", mode)
	}
}

func findBoundCandidate(ctx context.Context, adapter service.Adapter, record service.Record) (service.Candidate, error) {
	return findCandidateByReference(ctx, adapter, record.Reference())
}

func findCandidateByReference(ctx context.Context, adapter service.Adapter, reference service.Reference) (service.Candidate, error) {
	candidates, err := adapter.Discover(ctx)
	if err != nil {
		return service.Candidate{}, err
	}
	for _, candidate := range candidates {
		if candidate.Reference.UnitName == reference.UnitName && candidate.Reference.ArtifactPath == reference.ArtifactPath &&
			candidateScope(candidate) == service.NormalizeScope(reference.Scope) {
			return candidate, nil
		}
	}
	return service.Candidate{}, capabilityError("system definition cannot be mapped losslessly; restore the Tunlite definition or unmanage it")
}

func candidateScope(candidate service.Candidate) service.Scope {
	if candidate.Reference.Scope != "" {
		return service.NormalizeScope(candidate.Reference.Scope)
	}
	return service.NormalizeScope(candidate.Observation.Scope)
}

func observedMode(observation service.Observation) string {
	if observation.AutoStart {
		return service.ModePersistent
	}
	if observation.ProcessState == service.ProcessRunning || observation.ProcessState == service.ProcessStarting || observation.ProcessState == service.ProcessFailed {
		return service.ModeBackground
	}
	return service.ModeStopped
}
