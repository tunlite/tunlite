package service

import (
	"context"
	"fmt"

	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

// MacOSSudoProgram is the trusted system sudo used for every macOS privilege
// boundary. Callers must not resolve sudo through PATH.
const MacOSSudoProgram = "/usr/bin/sudo"

type UnsupportedScopeError struct {
	Adapter string
	Scope   Scope
}

func (e *UnsupportedScopeError) Error() string {
	return fmt.Sprintf("%s does not support %s service scope", e.Adapter, NormalizeScope(e.Scope))
}

func (e *UnsupportedScopeError) Code() string { return "scope_unsupported" }

type PrivilegeError struct {
	Operation string
	Cause     error
}

func (e *PrivilegeError) Error() string {
	if e.Operation == "" {
		return "system service scope requires administrator privileges"
	}
	if e.Cause == nil {
		return fmt.Sprintf("%s requires administrator privileges", e.Operation)
	}
	return fmt.Sprintf("%s requires administrator privileges: %v", e.Operation, e.Cause)
}

func (e *PrivilegeError) Unwrap() error { return e.Cause }
func (e *PrivilegeError) Code() string  { return "privilege_required" }

type ScopeConflictError struct {
	UnitName string
}

func (e *ScopeConflictError) Error() string {
	return fmt.Sprintf("native service %s exists in both user and system scopes", e.UnitName)
}

func (e *ScopeConflictError) Code() string { return "scope_conflict" }

type ScopeMismatchError struct {
	UnitName string
	Expected Scope
	Actual   Scope
}

func (e *ScopeMismatchError) Error() string {
	return fmt.Sprintf("native service %s exists in %s scope, not requested %s scope", e.UnitName, NormalizeScope(e.Actual), NormalizeScope(e.Expected))
}

func (e *ScopeMismatchError) Code() string { return "scope_mismatch" }

type RecoveryBlockedError struct {
	Operation string
	Cause     error
	Recovery  error
}

func (e *RecoveryBlockedError) Error() string {
	if e.Operation == "" {
		return fmt.Sprintf("operation failed: %v; recovery blocked: %v", e.Cause, e.Recovery)
	}
	return fmt.Sprintf("%s failed: %v; recovery blocked: %v", e.Operation, e.Cause, e.Recovery)
}

func (e *RecoveryBlockedError) Unwrap() []error {
	return []error{e.Cause, e.Recovery}
}

func (e *RecoveryBlockedError) Code() string { return "recovery_blocked" }

// ScopeTransition is a prepared native-service migration. StageScopeTransition
// has already stopped the source, installed and started the target, and run the
// caller's bounded health verification. Commit removes the old definition;
// Rollback removes the new definition and restores the exact source mode.
type ScopeTransition interface {
	Commit(context.Context) error
	Rollback(context.Context) error
}

type scopeTransitioner interface {
	PreflightScopeTransition(context.Context, Reference, Artifact) error
	StageScopeTransition(context.Context, Reference, Artifact, string, func(context.Context, Reference) error) (ScopeTransition, error)
}

type scopeInstaller interface {
	PreflightScopeInstall(context.Context, Artifact) error
	StageScopeInstall(context.Context, Artifact, string, func(context.Context, Reference) error) (ScopeTransition, error)
}

type scopeReadOnlyPreflighter interface {
	CheckScopeInstall(context.Context, Artifact) error
	CheckScopeTransition(context.Context, Reference, Artifact) error
}

type privilegedDisablePreflighter interface {
	PreflightDisable(context.Context, Reference) error
}

type definitionReplacer interface {
	CheckDefinitionReplace(context.Context, Reference, Artifact) error
	PreflightDefinitionReplace(context.Context, Reference, Artifact) error
	StageDefinitionReplace(context.Context, Reference, Artifact, string, func(context.Context, Reference) error) (ScopeTransition, error)
}

func CheckDefinitionReplace(ctx context.Context, adapter Adapter, source Reference, target Artifact) error {
	replacer, ok := adapter.(definitionReplacer)
	if !ok {
		return &UnsupportedScopeError{Adapter: adapter.Name(), Scope: source.Scope}
	}
	return replacer.CheckDefinitionReplace(ctx, source, target)
}

func PreflightDefinitionReplace(ctx context.Context, adapter Adapter, source Reference, target Artifact) error {
	replacer, ok := adapter.(definitionReplacer)
	if !ok {
		return &UnsupportedScopeError{Adapter: adapter.Name(), Scope: source.Scope}
	}
	return replacer.PreflightDefinitionReplace(ctx, source, target)
}

func StageDefinitionReplace(
	ctx context.Context,
	adapter Adapter,
	source Reference,
	target Artifact,
	mode string,
	verify func(context.Context, Reference) error,
) (ScopeTransition, error) {
	replacer, ok := adapter.(definitionReplacer)
	if !ok {
		return nil, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: source.Scope}
	}
	return replacer.StageDefinitionReplace(ctx, source, target, mode, verify)
}

// PreflightDisable verifies that an already-authorized system disable can run
// before the adapter mutates registration or its root-owned artifact.
func PreflightDisable(ctx context.Context, adapter Adapter, reference Reference) error {
	if NormalizeScope(reference.Scope) != ScopeSystem {
		return nil
	}
	if preflighter, ok := adapter.(privilegedDisablePreflighter); ok {
		return preflighter.PreflightDisable(ctx, reference)
	}
	return fmt.Errorf("%s cannot safely preflight system service disable", adapter.Name())
}

// CheckScopeInstall performs only read-only identity and conflict checks. It is
// suitable for dry-run: implementations must not authorize or mutate native
// service state.
func CheckScopeInstall(ctx context.Context, adapter Adapter, target Artifact) error {
	if checker, ok := adapter.(scopeReadOnlyPreflighter); ok {
		return checker.CheckScopeInstall(ctx, target)
	}
	if !SupportsScope(adapter, target.Scope) {
		return &UnsupportedScopeError{Adapter: adapter.Name(), Scope: target.Scope}
	}
	observation, err := adapter.Inspect(ctx, target.Reference())
	if err != nil {
		return err
	}
	if observation.Installed || observation.ProcessState != ProcessInactive {
		return &ScopeConflictError{UnitName: target.UnitName}
	}
	return nil
}

// CheckScopeTransition is the read-only part of migration preflight.
func CheckScopeTransition(ctx context.Context, adapter Adapter, source Reference, target Artifact) error {
	if checker, ok := adapter.(scopeReadOnlyPreflighter); ok {
		return checker.CheckScopeTransition(ctx, source, target)
	}
	if !SupportsScope(adapter, source.Scope) {
		return &UnsupportedScopeError{Adapter: adapter.Name(), Scope: source.Scope}
	}
	return CheckScopeInstall(ctx, adapter, target)
}

func PreflightScopeInstall(ctx context.Context, adapter Adapter, target Artifact) error {
	installer, ok := adapter.(scopeInstaller)
	if !ok {
		if NormalizeScope(target.Scope) != ScopeUser {
			return &UnsupportedScopeError{Adapter: adapter.Name(), Scope: target.Scope}
		}
		observation, err := adapter.Inspect(ctx, target.Reference())
		if err != nil {
			return err
		}
		if observation.Installed || observation.ProcessState != ProcessInactive {
			return &ScopeConflictError{UnitName: target.UnitName}
		}
		return nil
	}
	return installer.PreflightScopeInstall(ctx, target)
}

func StageScopeInstall(
	ctx context.Context,
	adapter Adapter,
	target Artifact,
	mode string,
	verify func(context.Context, Reference) error,
) (ScopeTransition, error) {
	installer, ok := adapter.(scopeInstaller)
	if !ok {
		return nil, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: target.Scope}
	}
	return installer.StageScopeInstall(ctx, target, mode, verify)
}

func PreflightScopeTransition(ctx context.Context, adapter Adapter, source Reference, target Artifact) error {
	transitioner, ok := adapter.(scopeTransitioner)
	if !ok {
		return &UnsupportedScopeError{Adapter: adapter.Name(), Scope: target.Scope}
	}
	return transitioner.PreflightScopeTransition(ctx, source, target)
}

func StageScopeTransition(
	ctx context.Context,
	adapter Adapter,
	source Reference,
	target Artifact,
	mode string,
	verify func(context.Context, Reference) error,
) (ScopeTransition, error) {
	transitioner, ok := adapter.(scopeTransitioner)
	if !ok {
		return nil, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: target.Scope}
	}
	return transitioner.StageScopeTransition(ctx, source, target, mode, verify)
}

func SupportsScope(adapter Adapter, scope Scope) bool {
	scope = NormalizeScope(scope)
	capabilities := adapter.Capabilities()
	switch scope {
	case ScopeUser:
		return capabilities.UserScope
	case ScopeSystem:
		return capabilities.SystemScope
	default:
		return false
	}
}

type scopedBuilder interface {
	TargetScoped(Scope, string) (Artifact, error)
	BuildScoped(Scope, string, plan.ExecutionPlan) (Artifact, error)
}

func TargetScoped(adapter Adapter, scope Scope, connection string) (Artifact, error) {
	scope = NormalizeScope(scope)
	if !SupportsScope(adapter, scope) {
		return Artifact{}, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: scope}
	}
	if scoped, ok := adapter.(scopedBuilder); ok {
		return scoped.TargetScoped(scope, connection)
	}
	if scope != ScopeUser {
		return Artifact{}, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: scope}
	}
	artifact := adapter.Target(connection)
	artifact.Scope = ScopeUser
	return artifact, nil
}

func BuildScoped(adapter Adapter, scope Scope, connection string, executionPlan plan.ExecutionPlan) (Artifact, error) {
	scope = NormalizeScope(scope)
	if !SupportsScope(adapter, scope) {
		return Artifact{}, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: scope}
	}
	if scoped, ok := adapter.(scopedBuilder); ok {
		return scoped.BuildScoped(scope, connection, executionPlan)
	}
	if scope != ScopeUser {
		return Artifact{}, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: scope}
	}
	artifact, err := adapter.Build(connection, executionPlan)
	artifact.Scope = ScopeUser
	return artifact, err
}

type ScopeTransitionStep struct {
	Name              string `json:"name"`
	RequiresPrivilege bool   `json:"requiresPrivilege"`
}

type ScopeTransitionPlan struct {
	Connection        string                `json:"connection"`
	From              Scope                 `json:"from"`
	To                Scope                 `json:"to"`
	RequiresPrivilege bool                  `json:"requiresPrivilege"`
	Steps             []ScopeTransitionStep `json:"steps"`
}

func PlanScopeTransition(adapter Adapter, record Record, target Scope) (ScopeTransitionPlan, error) {
	from := NormalizeScope(record.Scope)
	target = NormalizeScope(target)
	if !SupportsScope(adapter, from) {
		return ScopeTransitionPlan{}, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: from}
	}
	if !SupportsScope(adapter, target) {
		return ScopeTransitionPlan{}, &UnsupportedScopeError{Adapter: adapter.Name(), Scope: target}
	}
	result := ScopeTransitionPlan{Connection: record.Connection, From: from, To: target}
	if from == target {
		return result, nil
	}
	result.RequiresPrivilege = from == ScopeSystem || target == ScopeSystem
	result.Steps = []ScopeTransitionStep{
		{Name: "preflight", RequiresPrivilege: target == ScopeSystem},
		{Name: "stop_source", RequiresPrivilege: from == ScopeSystem},
		{Name: "install_target", RequiresPrivilege: target == ScopeSystem},
		{Name: "start_target", RequiresPrivilege: target == ScopeSystem},
		{Name: "observe_target", RequiresPrivilege: target == ScopeSystem},
		{Name: "record_scope"},
		{Name: "remove_source", RequiresPrivilege: from == ScopeSystem},
	}
	return result, nil
}
