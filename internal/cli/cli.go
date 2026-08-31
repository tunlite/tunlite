package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tunlite/tunlite/internal/configstore"
	"github.com/tunlite/tunlite/internal/core"
	sshdriver "github.com/tunlite/tunlite/internal/drivers/ssh"
	"github.com/tunlite/tunlite/internal/execution"
	appPaths "github.com/tunlite/tunlite/internal/paths"
	"github.com/tunlite/tunlite/internal/plan"
	"github.com/tunlite/tunlite/internal/service"
)

const (
	ProtocolVersion = 1
)

var Version = "1.0.0-preview.1-dev"

type IO struct {
	Out io.Writer
	Err io.Writer
	In  io.Reader
	// Color and Terminal are optional test/embedding overrides. Normal CLI use
	// detects both from the attached files and respects NO_COLOR.
	Color    *bool
	Terminal *bool
	// PromptTerminal optionally overrides whether stdin and stderr form a
	// visible interactive prompt. It is separate from Terminal because monitor
	// renders on stdout while selection/password prompts are written to stderr.
	PromptTerminal *bool
}

type options struct {
	JSON                   bool
	NonInteractive         bool
	DryRun                 bool
	Language               language
	serviceAdapter         func() (service.Adapter, error)
	lifecycleBudget        *lifecycleObservationBudget
	lifecycleObserver      *lifecycleObserver
	privilegeAuthorizer    privilegeAuthorizer
	systemReplacementBatch map[string]bool
}

type exitError struct {
	code      int
	kind      string
	message   string
	rendered  bool
	riskLevel string
	actions   []core.Action
}

func (e *exitError) Error() string { return e.message }

func Run(args []string, streams IO) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, args, streams)
}

func RunContext(ctx context.Context, args []string, streams IO) int {
	return runContext(ctx, args, streams, func() (service.Adapter, error) { return service.NewCurrent(nil) })
}

func runContext(ctx context.Context, args []string, streams IO, serviceAdapter func() (service.Adapter, error)) int {
	return runContextWithOptions(ctx, args, streams, options{serviceAdapter: serviceAdapter})
}

func runContextWithOptions(ctx context.Context, args []string, streams IO, overrides options) int {
	if streams.Out == nil {
		streams.Out = io.Discard
	}
	if streams.Err == nil {
		streams.Err = io.Discard
	}
	cleaned, opts, err := extractGlobalOptions(args)
	opts.serviceAdapter = overrides.serviceAdapter
	opts.lifecycleBudget = overrides.lifecycleBudget
	opts.privilegeAuthorizer = overrides.privilegeAuthorizer
	opts.systemReplacementBatch = overrides.systemReplacementBatch
	if opts.privilegeAuthorizer == nil {
		opts.privilegeAuthorizer = newSudoPrivilegeAuthorizer()
	}
	if err != nil {
		return renderError(streams, opts, usageError(err.Error()))
	}
	if len(cleaned) == 0 {
		return renderError(streams, opts, usageError("usage: tunlite <command> (run `tunlite help` for commands)"))
	}
	if err := validateGlobalOptionSupport(cleaned, opts); err != nil {
		return renderError(streams, opts, err)
	}
	if cleaned[0] != "help" && containsHelpFlag(cleaned[1:]) {
		if err := commandHelp(cleaned[0], streams, opts); err != nil {
			return renderError(streams, opts, err)
		}
		return 0
	}
	switch cleaned[0] {
	case "help", "--help", "-h":
		err = help(cleaned[1:], streams, opts)
	case "version", "--version", "-v":
		if len(cleaned) != 1 {
			err = usageError("usage: tunlite version")
			break
		}
		if opts.JSON {
			if err := writeResponse(streams.Out, map[string]any{
				"outcome":         "succeeded",
				"protocolVersion": ProtocolVersion,
				"version":         Version,
			}); err != nil {
				return renderError(streams, opts, err)
			}
			return 0
		}
		fmt.Fprintln(streams.Out, Version)
		return 0
	case "run":
		err = run(ctx, cleaned[1:], streams, opts)
	case "add":
		err = add(ctx, cleaned[1:], streams, opts)
	case "list":
		err = list(cleaned[1:], streams, opts)
	case "set":
		err = set(ctx, cleaned[1:], streams, opts)
	case "remove":
		err = remove(ctx, cleaned[1:], streams, opts)
	case "rename":
		err = rename(ctx, cleaned[1:], streams, opts)
	case "export":
		err = exportConnections(ctx, cleaned[1:], streams, opts)
	case "import":
		err = importConnections(ctx, cleaned[1:], streams, opts)
	case "validate":
		err = validateConnections(cleaned[1:], streams, opts)
	case "check":
		err = checkSSH(ctx, cleaned[1:], streams, opts)
	case "doctor":
		err = doctor(ctx, cleaned[1:], streams, opts)
	case "monitor":
		err = monitor(ctx, cleaned[1:], streams, opts)
	case "start", "stop", "enable", "disable", "restart", "status":
		err = serviceCommand(ctx, cleaned[0], cleaned[1:], streams, opts)
	case "logs":
		err = serviceLogs(ctx, cleaned[1:], streams, opts)
	case "manage":
		err = manageConnection(ctx, cleaned[1:], streams, opts)
	case "unmanage":
		err = unmanageConnection(ctx, cleaned[1:], streams, opts)
	case "resolve":
		err = resolveConnection(ctx, cleaned[1:], streams, opts)
	case "schema":
		err = schema(cleaned[1:], streams, opts)
	case "capabilities":
		err = capabilities(cleaned[1:], streams, opts)
	default:
		message := fmt.Sprintf("unknown command %q", cleaned[0])
		if suggestion := suggestCommand(cleaned[0]); suggestion != "" {
			message += fmt.Sprintf("; did you mean %q?", suggestion)
		}
		err = usageError(message)
	}
	if err == nil {
		return 0
	}
	var rendered *exitError
	if errors.As(err, &rendered) && rendered.rendered {
		return rendered.code
	}
	return renderError(streams, opts, err)
}

func run(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite run <name>")
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if err := flags.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError("usage: tunlite run <name>")
	}
	renderCommandProgress(streams, opts, append([]string{"run", args[0]}, args[1:]...))
	return runNamed(ctx, args[0], streams, opts)
}

func runNamed(ctx context.Context, name string, streams IO, opts options) error {
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	_, stored := configstore.Find(&document, name)
	if stored == nil {
		return notFoundError(fmt.Sprintf("no such connection: %s", name))
	}
	connection := cloneConnection(*stored)
	records, err := serviceRecordStore()
	if err != nil {
		return err
	}
	if opts.DryRun {
		if records.RunActive(name) {
			return stateConflictError("connection is already running in the foreground")
		}
		if err := ensureForegroundNativeState(ctx, records, connection, opts); err != nil {
			return err
		}
		return executeConnection(ctx, connection, streams, opts)
	}
	releaseService, err := records.Lock(ctx, name)
	if err != nil {
		return err
	}
	if err := ensureForegroundNativeState(ctx, records, connection, opts); err != nil {
		releaseService()
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	releaseRun, err := records.LockRun(lockCtx, name)
	cancel()
	releaseService()
	if err != nil {
		return stateConflictError(err.Error())
	}
	defer releaseRun()
	return executeConnection(ctx, connection, streams, opts)
}

func ensureForegroundNativeState(ctx context.Context, records *service.Store, connection core.Connection, opts options) error {
	name := connection.Metadata.Name
	record, exists, err := records.Load(name)
	if err != nil || !exists {
		return err
	}
	if record.Mode != service.ModeStopped {
		return stateConflictError(fmt.Sprintf("connection %q is %s; stop or disable it before running in the foreground", name, record.Mode))
	}
	adapter, err := opts.newServiceAdapter()
	if err != nil {
		return capabilityError(fmt.Sprintf("cannot verify the stopped native service for %q: %v", name, err))
	}
	artifact, err := buildBoundServiceArtifact(ctx, adapter, connection, &record)
	if err != nil {
		return err
	}
	state := service.Evaluate(ctx, adapter, record, &artifact)
	if state.ConsistencyState != service.ConsistencyConsistent || state.ProcessState != service.ProcessInactive {
		return stateConflictError(fmt.Sprintf("connection %q is not provably stopped and consistent; inspect status and resolve it before running in the foreground", name))
	}
	return nil
}

func (opts options) newServiceAdapter() (service.Adapter, error) {
	if opts.serviceAdapter == nil {
		return service.NewCurrent(nil)
	}
	return opts.serviceAdapter()
}

func compensationContext(parent context.Context) (context.Context, context.CancelFunc) {
	// Recovery must still run after the initiating request was canceled, but it
	// remains bounded so a broken service manager cannot hang the CLI forever.
	return context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
}

func recoveryBlockedError(cause, recovery error, actions []core.Action) error {
	if recovery == nil {
		return cause
	}
	if actions == nil {
		actions = []core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{}}}
	}
	return &exitError{
		code: 1, kind: "recovery_blocked", riskLevel: "high", actions: actions,
		message: errors.Join(cause, fmt.Errorf("automatic recovery failed: %w", recovery)).Error(),
	}
}

func executeConnection(ctx context.Context, connection core.Connection, streams IO, opts options) error {
	registry, err := core.NewRegistry(sshdriver.New())
	if err != nil {
		return err
	}
	executionPlan, err := core.NewPlanner(registry).Plan(ctx, connection, core.PlanOptions{NonInteractive: opts.NonInteractive})
	if err != nil {
		return err
	}
	conditions := planRiskConditions(connection, executionPlan)
	if opts.DryRun {
		return renderPlan(streams, opts, connection, executionPlan, conditions)
	}
	if opts.JSON {
		_ = writeResponse(streams.Out, map[string]any{
			"outcome": "starting", "command": "run", "connection": connection.Metadata.Name,
			"stream": "execution", "plan": executionPlan,
		})
	} else {
		fmt.Fprintf(streams.Err, "starting %s via %s\n", connection.Metadata.Name, executionPlan.Exec.Program)
	}
	err = execution.Run(ctx, executionPlan, execution.IO{In: streams.In, Out: streams.Out, Err: streams.Err})
	if ctx.Err() != nil {
		renderStopped(streams, opts, connection.Metadata.Name, "cancelled")
		return nil
	}
	if err != nil {
		return fmt.Errorf("provider exited: %w", err)
	}
	renderStopped(streams, opts, connection.Metadata.Name, "provider-exited")
	return nil
}

func renderStopped(streams IO, opts options, connection, reason string) {
	if opts.JSON {
		_ = writeResponse(streams.Out, map[string]any{
			"outcome":    "stopped",
			"connection": connection,
			"reason":     reason,
		})
		return
	}
	fmt.Fprintf(streams.Err, "stopped %s (%s)\n", connection, reason)
}

func renderPlan(streams IO, opts options, connection core.Connection, executionPlan plan.ExecutionPlan, conditions []core.Condition) error {
	if opts.JSON {
		return writeResponse(streams.Out, map[string]any{
			"outcome":    "planned",
			"connection": connection,
			"plan":       executionPlan,
			"conditions": conditions,
			"dryRun":     true,
		})
	}
	fmt.Fprintf(streams.Out, "%s", executionPlan.Exec.Program)
	for _, arg := range executionPlan.Exec.Args {
		fmt.Fprintf(streams.Out, " %q", arg)
	}
	fmt.Fprintln(streams.Out)
	return nil
}

func extractGlobalOptions(args []string) ([]string, options, error) {
	cleaned := make([]string, 0, len(args))
	opts := options{Language: languageEnglish}
	explicitLanguage := ""
	languageSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// Everything after -- belongs to the command or Driver. In
			// particular, an SSH target named like a global option must never be
			// consumed by this parser.
			cleaned = append(cleaned, args[i:]...)
			break
		}
		switch arg {
		case "--json":
			opts.JSON = true
		case "--non-interactive":
			opts.NonInteractive = true
		case "--dry-run":
			opts.DryRun = true
		default:
			switch {
			case arg == "--lang":
				if languageSet {
					return cleaned, opts, errors.New("--lang may only be specified once")
				}
				if i+1 >= len(args) {
					return cleaned, opts, errors.New("--lang requires en or zh-CN")
				}
				i++
				explicitLanguage = args[i]
			case strings.HasPrefix(arg, "--lang="):
				if languageSet {
					return cleaned, opts, errors.New("--lang may only be specified once")
				}
				explicitLanguage = strings.TrimPrefix(arg, "--lang=")
			default:
				cleaned = append(cleaned, arg)
				continue
			}
			languageSet = true
		}
	}
	lang, err := resolveLanguage(explicitLanguage)
	if err != nil {
		return cleaned, opts, err
	}
	opts.Language = lang
	return cleaned, opts, nil
}

func validateGlobalOptionSupport(args []string, opts options) error {
	if len(args) == 0 || !opts.DryRun {
		return nil
	}
	command := args[0]
	if command == "--help" || command == "-h" {
		command = "help"
	}
	if command == "--version" || command == "-v" {
		command = "version"
	}
	// A command help request is itself a read-only help operation, even when
	// the named command normally supports planning.
	if command != "help" && containsHelpFlag(args[1:]) {
		command = "help"
	}
	unsupported := map[string]bool{
		"capabilities": true, "check": true, "doctor": true, "help": true,
		"list": true, "logs": true, "monitor": true, "status": true,
		"validate": true, "version": true,
	}
	if unsupported[command] {
		return usageError(fmt.Sprintf("%s does not support --dry-run", command))
	}
	return nil
}

func renderError(streams IO, opts options, err error) int {
	code := 1
	message := err.Error()
	var typed *exitError
	if errors.As(err, &typed) {
		code = typed.code
	}
	kind := "internal_error"
	if typed != nil && typed.kind != "" {
		kind = typed.kind
	} else {
		kind = serviceOperationErrorCode(err, kind)
	}
	if kind == "privilege_required" || kind == "interaction_required" {
		code = 4
	}
	if opts.JSON {
		riskLevel := "low"
		actions := []core.Action{}
		if typed != nil {
			if typed.riskLevel != "" {
				riskLevel = typed.riskLevel
			}
			if typed.actions != nil {
				actions = typed.actions
			}
		}
		var actionable interface{ Actions() []core.Action }
		if errors.As(err, &actionable) {
			actions = actionable.Actions()
		}
		if kind == "recovery_blocked" || kind == "scope_conflict" {
			riskLevel = "high"
			if len(actions) == 0 {
				actions = []core.Action{{Type: "inspect_service_manager", Parameters: map[string]any{}}}
			}
		}
		_ = writeResponse(streams.Out, map[string]any{
			"outcome": "failed",
			"error": map[string]any{
				"code": kind, "driver": nil, "connection": nil, "message": message,
				"riskLevel": riskLevel, "actions": actions,
			},
		})
	} else {
		fmt.Fprintln(streams.Err, localizeHumanError(kind, message, opts.Language))
	}
	return code
}

func usageError(message string) error {
	return &exitError{code: 2, kind: "invalid_arguments", message: message}
}

func interactionRequiredError(message string) error {
	return &exitError{code: 4, kind: "interaction_required", message: message}
}

func notFoundError(message string) error {
	return &exitError{code: 3, kind: "not_found", message: message}
}

func conflictError(message string) error {
	return &exitError{code: 2, kind: "already_exists", message: message}
}

func stateConflictError(message string) error {
	return &exitError{code: 1, kind: "state_conflict", message: message}
}

func classifyConcurrentChange(err error) error {
	if errors.Is(err, configstore.ErrChanged) {
		return stateConflictError(err.Error() + "; retry after inspecting the latest Connection")
	}
	return err
}

func capabilityError(message string) error {
	return &exitError{code: 1, kind: "capability_unsupported", message: message}
}

func renderedExit(code int) error {
	return &exitError{code: code, rendered: true}
}

func connectionStore(opts options) (*configstore.Store, error) {
	_ = opts
	path, err := appPaths.ConfigFile()
	if err != nil {
		return nil, err
	}
	return configstore.New(path), nil
}

func sanitizeName(host string) string {
	value := strings.NewReplacer("@", "-", ":", "-", "[", "", "]", "").Replace(host)
	value = strings.Trim(value, "-.")
	if value == "" {
		return "connection"
	}
	if len(value) > 63 {
		return value[:63]
	}
	return value
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeResponse(writer io.Writer, value map[string]any) error {
	response := make(map[string]any, len(value)+1)
	for key, item := range value {
		response[key] = item
	}
	response["protocolVersion"] = ProtocolVersion
	return writeJSON(writer, response)
}
