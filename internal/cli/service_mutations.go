package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/tunlite/tunlite/internal/core"
	"github.com/tunlite/tunlite/internal/service"
)

type stagedServiceMutation struct {
	finish     func()
	commit     func() error
	rollback   func() error
	conditions []condition
	actions    []core.Action
}

func (mutation stagedServiceMutation) rollbackAfter(cause error) error {
	return recoveryBlockedError(cause, mutation.rollback(), mutation.actions)
}

func noServiceMutation() stagedServiceMutation {
	return stagedServiceMutation{
		finish: func() {}, commit: func() error { return nil }, rollback: func() error { return nil },
		conditions: []condition{},
		actions:    []core.Action{},
	}
}

func previewNewConnectionService(ctx context.Context, connection core.Connection, declarationPath string, opts options) ([]condition, error) {
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return []condition{{
			Code: "foreground_only", Severity: "warning", Connection: connection.Metadata.Name,
			Message: "no supported native service manager is available; this connection can only use `tunlite run`",
			Actions: []core.Action{},
		}}, nil
	}
	if !service.SupportsScope(adapter, service.ScopeUser) {
		if err := unboundNativeIdentityConflict(ctx, adapter, adapter.Target(connection.Metadata.Name)); err != nil {
			return nil, err
		}
		return []condition{{
			Code: "foreground_only", Severity: "warning", Connection: connection.Metadata.Name,
			Message: "the user service domain is unavailable; this connection can only use `tunlite run` in the current session",
			Actions: []core.Action{},
		}}, nil
	}
	records, err := serviceRecordStore()
	if err != nil {
		return nil, err
	}
	if records.RunActive(connection.Metadata.Name) {
		return nil, stateConflictError("connection is already running in the foreground")
	}
	if record, exists, err := records.Load(connection.Metadata.Name); err != nil {
		return nil, err
	} else if exists {
		return nil, conflictError(fmt.Sprintf("native service state for %q already exists at %s", connection.Metadata.Name, record.ArtifactPath))
	}
	artifact, err := buildServiceArtifact(ctx, adapter, connection)
	if err != nil {
		return nil, err
	}
	if err := unboundNativeIdentityConflict(ctx, adapter, artifact); err != nil {
		return nil, err
	}
	observation, err := adapter.Inspect(ctx, artifact.Reference())
	if err != nil {
		return nil, err
	}
	if !nativeServiceIdentityAvailable(observation) {
		return nil, conflictError(fmt.Sprintf("native service identity %s is installed, active, enabled, or cannot be proven unused", artifact.UnitName))
	}
	_ = declarationPath
	return []condition{}, nil
}

func previewUpdatedConnectionService(ctx context.Context, before, after core.Connection, declarationPath string, opts options) ([]condition, error) {
	records, err := serviceRecordStore()
	if err != nil {
		return nil, err
	}
	if records.RunActive(before.Metadata.Name) {
		return nil, stateConflictError("connection is running in the foreground; stop that run before changing its configuration")
	}
	record, exists, err := records.Load(before.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return previewNewConnectionService(ctx, after, declarationPath, opts)
	}
	if err := ensureDeclarationOwner(record, declarationPath); err != nil {
		return nil, err
	}
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return nil, err
	}
	oldArtifact, err := buildBoundServiceArtifact(ctx, adapter, before, &record)
	if err != nil {
		return nil, err
	}
	newArtifact, err := buildBoundServiceArtifact(ctx, adapter, after, &record)
	if err != nil {
		return nil, err
	}
	if oldArtifact.PlanDigest == newArtifact.PlanDigest {
		return []condition{}, nil
	}
	state := service.Evaluate(ctx, adapter, record, &oldArtifact)
	if state.ConsistencyState != service.ConsistencyConsistent {
		return nil, stateConflictError("connection has drifted; resolve it before changing its configuration")
	}
	conditions := []condition{}
	if record.Mode == service.ModeBackground || record.Mode == service.ModePersistent {
		conditions = append(conditions, condition{
			Code: "connection_restart_required", Severity: "warning", Connection: before.Metadata.Name,
			Message: "applying this configuration will restart the running native service",
			Actions: []core.Action{},
		})
	}
	return conditions, nil
}

func previewRenamedConnectionService(oldName, newName, declarationPath string, opts options) error {
	if oldName == newName {
		return nil
	}
	records, err := serviceRecordStore()
	if err != nil {
		return err
	}
	if records.RunActive(oldName) {
		return stateConflictError("connection is running in the foreground; stop that run before renaming it")
	}
	record, exists, err := records.Load(oldName)
	if err != nil || !exists {
		return err
	}
	if err := ensureDeclarationOwner(record, declarationPath); err != nil {
		return err
	}
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return err
	}
	if err := ensureRecordScopeSupported(adapter, record); err != nil {
		return err
	}
	if _, exists, err := records.Load(newName); err != nil {
		return err
	} else if exists {
		return conflictError(fmt.Sprintf("native service state for %q already exists", newName))
	}
	return nil
}

func stageNewConnectionService(ctx context.Context, connection core.Connection, declarationPath string, opts options) (stagedServiceMutation, error) {
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		mutation := noServiceMutation()
		mutation.conditions = []condition{{
			Code: "foreground_only", Severity: "warning", Connection: connection.Metadata.Name,
			Message: "no supported native service manager is available; this connection can only use `tunlite run`",
			Actions: []core.Action{},
		}}
		return mutation, nil
	}
	if !service.SupportsScope(adapter, service.ScopeUser) {
		if err := unboundNativeIdentityConflict(ctx, adapter, adapter.Target(connection.Metadata.Name)); err != nil {
			return stagedServiceMutation{}, err
		}
		mutation := noServiceMutation()
		mutation.conditions = []condition{{
			Code: "foreground_only", Severity: "warning", Connection: connection.Metadata.Name,
			Message: "the user service domain is unavailable; this connection can only use `tunlite run` in the current session",
			Actions: []core.Action{},
		}}
		return mutation, nil
	}
	records, err := serviceRecordStore()
	if err != nil {
		return stagedServiceMutation{}, err
	}
	release, err := records.Lock(ctx, connection.Metadata.Name)
	if err != nil {
		return stagedServiceMutation{}, err
	}
	fail := func(err error) (stagedServiceMutation, error) {
		release()
		return stagedServiceMutation{}, err
	}
	if records.RunActive(connection.Metadata.Name) {
		return fail(stateConflictError("connection is already running in the foreground"))
	}
	if record, exists, err := records.Load(connection.Metadata.Name); err != nil {
		return fail(err)
	} else if exists {
		return fail(conflictError(fmt.Sprintf("native service state for %q already exists at %s", connection.Metadata.Name, record.ArtifactPath)))
	}
	artifact, err := buildServiceArtifact(ctx, adapter, connection)
	if err != nil {
		return fail(err)
	}
	if err := unboundNativeIdentityConflict(ctx, adapter, artifact); err != nil {
		return fail(err)
	}
	observation, err := adapter.Inspect(ctx, artifact.Reference())
	if err != nil {
		return fail(err)
	}
	if !nativeServiceIdentityAvailable(observation) {
		return fail(conflictError(fmt.Sprintf("native service identity %s is installed, active, enabled, or cannot be proven unused", artifact.UnitName)))
	}
	if err := adapter.Materialize(ctx, artifact); err != nil {
		return fail(err)
	}
	record := recordForArtifact(artifact, service.ModeStopped, declarationPath)
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		return fail(recoveryBlockedError(err, adapter.Remove(recoveryCtx, artifact.Reference()), nil))
	}
	return stagedServiceMutation{
		finish: release,
		commit: func() error { return nil },
		rollback: func() error {
			recoveryCtx, cancel := compensationContext(ctx)
			defer cancel()
			removeErr := adapter.Remove(recoveryCtx, artifact.Reference())
			recordErr := records.Remove(connection.Metadata.Name)
			return errors.Join(removeErr, recordErr)
		},
		conditions: []condition{},
		actions:    []core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{"connection": connection.Metadata.Name}}},
	}, nil
}

func nativeServiceIdentityAvailable(observation service.Observation) bool {
	return !observation.Installed && !observation.AutoStart && observation.ProcessState == service.ProcessInactive
}

func stageUpdatedConnectionService(ctx context.Context, before, after core.Connection, declarationPath string, opts options, streamArgs ...IO) (stagedServiceMutation, error) {
	streams := IO{In: nil, Out: io.Discard, Err: io.Discard}
	if len(streamArgs) > 0 {
		streams = streamArgs[0]
	}
	records, err := serviceRecordStore()
	if err != nil {
		return stagedServiceMutation{}, err
	}
	release, err := records.Lock(ctx, before.Metadata.Name)
	if err != nil {
		return stagedServiceMutation{}, err
	}
	fail := func(err error) (stagedServiceMutation, error) {
		release()
		return stagedServiceMutation{}, err
	}
	if records.RunActive(before.Metadata.Name) {
		return fail(stateConflictError("connection is running in the foreground; stop that run before changing its configuration"))
	}
	record, exists, err := records.Load(before.Metadata.Name)
	if err != nil {
		return fail(err)
	}
	if !exists {
		release()
		return stageNewConnectionService(ctx, after, declarationPath, opts)
	}
	if err := ensureDeclarationOwner(record, declarationPath); err != nil {
		return fail(err)
	}
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return fail(err)
	}
	oldArtifact, err := buildBoundServiceArtifact(ctx, adapter, before, &record)
	if err != nil {
		return fail(err)
	}
	newArtifact, err := buildBoundServiceArtifact(ctx, adapter, after, &record)
	if err != nil {
		return fail(err)
	}
	if oldArtifact.PlanDigest == newArtifact.PlanDigest {
		return stagedServiceMutation{
			finish: release, commit: func() error { return nil }, rollback: func() error { return nil }, conditions: []condition{},
		}, nil
	}
	state := service.Evaluate(ctx, adapter, record, &oldArtifact)
	if state.ConsistencyState != service.ConsistencyConsistent {
		return fail(stateConflictError("connection has drifted; resolve it before changing its configuration"))
	}
	observation, err := adapter.Inspect(ctx, record.Reference())
	if err != nil {
		return fail(err)
	}
	if service.NormalizeScope(record.Scope) == service.ScopeSystem && record.Mode == service.ModeStopped && !observation.Installed {
		oldRecord := record
		record.PlanDigest = newArtifact.PlanDigest
		record.BaselineDigest = service.ContentDigest(newArtifact.Content)
		record.ArtifactPath = ""
		if err := records.Write(ctx, record); err != nil {
			return fail(err)
		}
		return stagedServiceMutation{
			finish: release,
			commit: func() error { return nil },
			rollback: func() error {
				recoveryCtx, cancel := compensationContext(ctx)
				defer cancel()
				return records.Write(recoveryCtx, oldRecord)
			},
			conditions: []condition{}, actions: resolveActions(before.Metadata.Name),
		}, nil
	}
	if service.NormalizeScope(record.Scope) == service.ScopeSystem {
		if record.Mode != service.ModePersistent {
			return fail(stateConflictError("system-scope service has an invalid temporary lifecycle mode"))
		}
		if err := service.CheckDefinitionReplace(ctx, adapter, record.Reference(), newArtifact); err != nil {
			return fail(err)
		}
		batchPreflighted := opts.systemReplacementBatch != nil && opts.systemReplacementBatch[before.Metadata.Name]
		if !batchPreflighted {
			renderSystemDefinitionProgress(streams, opts, before.Metadata.Name, "authorize")
		}
		if !batchPreflighted && !opts.NonInteractive {
			if opts.privilegeAuthorizer == nil {
				return fail(&privilegeRequiredError{message: "administrator authorization is required to replace the system service definition"})
			}
			if err := opts.privilegeAuthorizer.Validate(ctx, streams, opts.Language, privilegeRequest{connections: 1}); err != nil {
				return fail(err)
			}
		}
		if !batchPreflighted {
			if err := service.PreflightDefinitionReplace(ctx, adapter, record.Reference(), newArtifact); err != nil {
				return fail(err)
			}
		}
		renderSystemDefinitionProgress(streams, opts, before.Metadata.Name, "replace")
		transition, err := service.StageDefinitionReplace(ctx, adapter, record.Reference(), newArtifact, service.ModePersistent,
			transitionHealthVerifierWithProgress(newArtifact, streams, opts, before.Metadata.Name))
		if err != nil {
			return fail(err)
		}
		oldRecord := record
		record.PlanDigest = newArtifact.PlanDigest
		record.BaselineDigest = service.ContentDigest(newArtifact.Content)
		if err := records.Write(ctx, record); err != nil {
			recoveryCtx, cancel := compensationContext(ctx)
			defer cancel()
			return fail(recoveryBlockedError(err, transition.Rollback(recoveryCtx), resolveActions(before.Metadata.Name)))
		}
		renderSystemDefinitionProgress(streams, opts, before.Metadata.Name, "ready")
		return stagedServiceMutation{
			finish: release,
			commit: func() error { return transition.Commit(ctx) },
			rollback: func() error {
				recoveryCtx, cancel := compensationContext(ctx)
				defer cancel()
				return errors.Join(transition.Rollback(recoveryCtx), records.Write(recoveryCtx, oldRecord))
			},
			conditions: []condition{}, actions: resolveActions(before.Metadata.Name),
		}, nil
	}
	rollbackArtifact := oldArtifact
	rollbackArtifact.Content = append([]byte(nil), observation.Artifact...)
	if err := adapter.Materialize(ctx, newArtifact); err != nil {
		return fail(err)
	}
	if record.Mode == service.ModeBackground || record.Mode == service.ModePersistent {
		if err := adapter.Restart(ctx, record.Reference()); err != nil {
			recoveryCtx, cancel := compensationContext(ctx)
			defer cancel()
			materializeErr := adapter.Materialize(recoveryCtx, rollbackArtifact)
			restartErr := adapter.Restart(recoveryCtx, record.Reference())
			return fail(recoveryBlockedError(err, errors.Join(materializeErr, restartErr), resolveActions(before.Metadata.Name)))
		}
	}
	oldRecord := record
	record.PlanDigest = newArtifact.PlanDigest
	record.BaselineDigest = service.ContentDigest(newArtifact.Content)
	if err := records.Write(ctx, record); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		materializeErr := adapter.Materialize(recoveryCtx, rollbackArtifact)
		var restartErr error
		if oldRecord.Mode == service.ModeBackground || oldRecord.Mode == service.ModePersistent {
			restartErr = adapter.Restart(recoveryCtx, oldRecord.Reference())
		}
		return fail(recoveryBlockedError(err, errors.Join(materializeErr, restartErr), resolveActions(before.Metadata.Name)))
	}
	return stagedServiceMutation{
		finish: release,
		commit: func() error { return nil },
		rollback: func() error {
			recoveryCtx, cancel := compensationContext(ctx)
			defer cancel()
			materializeErr := adapter.Materialize(recoveryCtx, rollbackArtifact)
			var restartErr error
			if oldRecord.Mode == service.ModeBackground || oldRecord.Mode == service.ModePersistent {
				restartErr = adapter.Restart(recoveryCtx, oldRecord.Reference())
			}
			recordErr := records.Write(recoveryCtx, oldRecord)
			return errors.Join(materializeErr, restartErr, recordErr)
		},
		conditions: []condition{},
		actions:    resolveActions(before.Metadata.Name),
	}, nil
}

func renderSystemDefinitionProgress(streams IO, opts options, connection, phase string) {
	if opts.JSON || !terminalProgressEnabled(streams) {
		return
	}
	switch phase {
	case "authorize":
		fmt.Fprintln(streams.Err, localized(opts.Language,
			fmt.Sprintf("Validated %q; preparing administrator authorization…", connection),
			fmt.Sprintf("“%s”已完成只读校验，正在准备管理员授权…", connection)))
	case "replace":
		fmt.Fprintln(streams.Err, localized(opts.Language,
			fmt.Sprintf("Replacing the system service definition for %q and waiting for it to stabilize…", connection),
			fmt.Sprintf("正在替换“%s”的系统服务定义并等待进程稳定…", connection)))
	case "health":
		fmt.Fprintln(streams.Err, localized(opts.Language,
			fmt.Sprintf("Replacement process for %q is running; verifying endpoints…", connection),
			fmt.Sprintf("“%s”的替换进程已运行，正在验证端点…", connection)))
	case "ready":
		fmt.Fprintln(streams.Err, localized(opts.Language,
			fmt.Sprintf("System service %q is stable; saving the new baseline…", connection),
			fmt.Sprintf("系统服务“%s”已稳定，正在保存新基线…", connection)))
	}
}

func stageRemovedConnectionService(ctx context.Context, connection core.Connection, declarationPath string, opts options) (stagedServiceMutation, error) {
	records, err := serviceRecordStore()
	if err != nil {
		return stagedServiceMutation{}, err
	}
	release, err := records.Lock(ctx, connection.Metadata.Name)
	if err != nil {
		return stagedServiceMutation{}, err
	}
	fail := func(err error) (stagedServiceMutation, error) {
		release()
		return stagedServiceMutation{}, err
	}
	if records.RunActive(connection.Metadata.Name) {
		return fail(stateConflictError("connection is running in the foreground; stop that run before removing it"))
	}
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil {
		return fail(err)
	}
	if !exists {
		adapter, adapterErr := opts.newServiceAdapter()
		if adapterErr != nil {
			return stagedServiceMutation{
				finish: release, commit: func() error { return nil }, rollback: func() error { return nil }, conditions: []condition{},
			}, nil
		}
		artifact, buildErr := buildServiceArtifact(ctx, adapter, connection)
		if buildErr != nil {
			return fail(buildErr)
		}
		if conflictErr := unboundNativeIdentityConflict(ctx, adapter, artifact); conflictErr != nil {
			return fail(conflictErr)
		}
		observation, inspectErr := adapter.Inspect(ctx, artifact.Reference())
		if inspectErr != nil {
			return fail(inspectErr)
		}
		if observation.Installed || observation.ProcessState == service.ProcessRunning || observation.ProcessState == service.ProcessStarting || observation.AutoStart {
			return fail(stateConflictError("native service exists but its management binding is missing; resolve it before removing the connection"))
		}
		return stagedServiceMutation{
			finish: release, commit: func() error { return nil }, rollback: func() error { return nil }, conditions: []condition{},
		}, nil
	}
	if err := ensureDeclarationOwner(record, declarationPath); err != nil {
		return fail(err)
	}
	if record.Mode != service.ModeStopped {
		return fail(stateConflictError(fmt.Sprintf("connection %q is %s; stop or disable it before removing it", connection.Metadata.Name, record.Mode)))
	}
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return fail(err)
	}
	artifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &record)
	if err != nil {
		return fail(err)
	}
	observation, err := adapter.Inspect(ctx, record.Reference())
	if err != nil {
		return fail(err)
	}
	if observation.ProcessState == service.ProcessRunning || observation.ProcessState == service.ProcessStarting || observation.AutoStart {
		return fail(stateConflictError("native service is still running or persistent; resolve its state before removing it"))
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	if state.ConsistencyState != service.ConsistencyConsistent {
		return fail(stateConflictError("connection has drifted; resolve it or unmanage it before removing it"))
	}
	if service.NormalizeScope(record.Scope) == service.ScopeSystem {
		if observation.Installed {
			return fail(stateConflictError("system service definition still exists; use disable before removing the connection"))
		}
		return stagedServiceMutation{
			finish: release,
			commit: func() error { return records.Remove(connection.Metadata.Name) },
			rollback: func() error {
				recoveryCtx, cancel := compensationContext(ctx)
				defer cancel()
				return records.Write(recoveryCtx, record)
			},
			conditions: []condition{},
			actions:    resolveActions(connection.Metadata.Name),
		}, nil
	}
	rollbackArtifact := artifact
	rollbackArtifact.Content = append([]byte(nil), observation.Artifact...)
	if err := adapter.Remove(ctx, record.Reference()); err != nil {
		recoveryCtx, cancel := compensationContext(ctx)
		defer cancel()
		materializeErr := adapter.Materialize(recoveryCtx, rollbackArtifact)
		modeErr := applyNativeMode(recoveryCtx, adapter, record.Reference(), record.Mode)
		return fail(recoveryBlockedError(err, errors.Join(materializeErr, modeErr), resolveActions(connection.Metadata.Name)))
	}
	return stagedServiceMutation{
		finish: release,
		commit: func() error { return records.Remove(connection.Metadata.Name) },
		rollback: func() error {
			recoveryCtx, cancel := compensationContext(ctx)
			defer cancel()
			materializeErr := adapter.Materialize(recoveryCtx, rollbackArtifact)
			recordErr := records.Write(recoveryCtx, record)
			return errors.Join(materializeErr, recordErr)
		},
		conditions: []condition{},
		actions:    resolveActions(connection.Metadata.Name),
	}, nil
}

func checkConnectionRemovable(ctx context.Context, connection core.Connection, declarationPath string, opts options) error {
	records, err := serviceRecordStore()
	if err != nil {
		return err
	}
	if records.RunActive(connection.Metadata.Name) {
		return stateConflictError("connection is running in the foreground; stop that run before removing it")
	}
	record, exists, err := records.Load(connection.Metadata.Name)
	if err != nil || !exists {
		if err != nil {
			return err
		}
		adapter, adapterErr := opts.newServiceAdapter()
		if adapterErr != nil {
			return nil
		}
		artifact, buildErr := buildServiceArtifact(ctx, adapter, connection)
		if buildErr != nil {
			return buildErr
		}
		if conflictErr := unboundNativeIdentityConflict(ctx, adapter, artifact); conflictErr != nil {
			return conflictErr
		}
		observation, inspectErr := adapter.Inspect(ctx, artifact.Reference())
		if inspectErr != nil {
			return inspectErr
		}
		if observation.Installed || observation.ProcessState == service.ProcessRunning || observation.ProcessState == service.ProcessStarting || observation.AutoStart {
			return stateConflictError("native service exists but its management binding is missing; resolve it before removing the connection")
		}
		return nil
	}
	if err := ensureDeclarationOwner(record, declarationPath); err != nil {
		return err
	}
	if record.Mode != service.ModeStopped {
		return stateConflictError(fmt.Sprintf("connection %q is %s; stop or disable it before removing it", connection.Metadata.Name, record.Mode))
	}
	adapter, err := opts.newServiceAdapter()
	if err != nil {
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
	if observation.ProcessState == service.ProcessRunning || observation.ProcessState == service.ProcessStarting || observation.AutoStart {
		return stateConflictError("native service is still running or persistent; resolve its state before removing it")
	}
	if state := service.Evaluate(ctx, adapter, record, &artifact); state.ConsistencyState != service.ConsistencyConsistent {
		return stateConflictError("connection has drifted; resolve it or unmanage it before removing it")
	}
	if service.NormalizeScope(record.Scope) == service.ScopeSystem && observation.Installed {
		return stateConflictError("system service definition still exists; use disable before removing the connection")
	}
	return nil
}

func stageRenamedConnectionService(ctx context.Context, oldName, newName, declarationPath string, opts options) (stagedServiceMutation, error) {
	if oldName == newName {
		return noServiceMutation(), nil
	}
	records, err := serviceRecordStore()
	if err != nil {
		return stagedServiceMutation{}, err
	}
	names := []string{oldName, newName}
	sort.Strings(names)
	releases := make([]func(), 0, len(names))
	for _, name := range names {
		releaseName, lockErr := records.Lock(ctx, name)
		if lockErr != nil {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
			return stagedServiceMutation{}, lockErr
		}
		releases = append(releases, releaseName)
	}
	release := func() {
		for index := len(releases) - 1; index >= 0; index-- {
			releases[index]()
		}
	}
	fail := func(err error) (stagedServiceMutation, error) {
		release()
		return stagedServiceMutation{}, err
	}
	if records.RunActive(oldName) {
		return fail(stateConflictError("connection is running in the foreground; stop that run before renaming it"))
	}
	record, exists, err := records.Load(oldName)
	if err != nil {
		return fail(err)
	}
	if !exists {
		return stagedServiceMutation{
			finish: release, commit: func() error { return nil }, rollback: func() error { return nil }, conditions: []condition{},
		}, nil
	}
	if err := ensureDeclarationOwner(record, declarationPath); err != nil {
		return fail(err)
	}
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return fail(err)
	}
	if err := ensureRecordScopeSupported(adapter, record); err != nil {
		return fail(err)
	}
	if _, exists, err := records.Load(newName); err != nil {
		return fail(err)
	} else if exists {
		return fail(conflictError(fmt.Sprintf("native service state for %q already exists", newName)))
	}
	oldRecord := record
	newRecord := record
	newRecord.Connection = newName
	if err := records.Write(ctx, newRecord); err != nil {
		return fail(err)
	}
	return stagedServiceMutation{
		finish: release,
		commit: func() error { return records.Remove(oldName) },
		rollback: func() error {
			recoveryCtx, cancel := compensationContext(ctx)
			defer cancel()
			// Keep at least one valid binding. Only remove the new record after
			// the original record has definitely been restored.
			if err := records.Write(recoveryCtx, oldRecord); err != nil {
				return err
			}
			return records.Remove(newName)
		},
		conditions: []condition{},
		actions:    resolveActions(oldName),
	}, nil
}
