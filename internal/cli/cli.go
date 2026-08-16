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
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/execution"
	appPaths "github.com/yuanyuanzijin/tunlite/internal/paths"
	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

const (
	Version         = "1.0.0-preview.1-dev"
	ContractVersion = "tunlite.cli/v1"
)

type IO struct {
	Out io.Writer
	Err io.Writer
	In  io.Reader
}

type options struct {
	JSON           bool
	NonInteractive bool
	DryRun         bool
	ConfigFile     string
}

type exitError struct {
	code     int
	kind     string
	message  string
	rendered bool
}

func (e *exitError) Error() string { return e.message }

func Run(args []string, streams IO) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, args, streams)
}

func RunContext(ctx context.Context, args []string, streams IO) int {
	if streams.Out == nil {
		streams.Out = io.Discard
	}
	if streams.Err == nil {
		streams.Err = io.Discard
	}
	cleaned, opts, err := extractGlobalOptions(args)
	if err != nil {
		return renderError(streams, opts, usageError(err.Error()))
	}
	if len(cleaned) == 0 {
		return renderError(streams, opts, usageError("usage: tunlite <command> (run `tunlite help` for commands)"))
	}

	switch cleaned[0] {
	case "help", "--help", "-h":
		err = help(streams, opts)
	case "version", "--version", "-v":
		if opts.JSON {
			if err := writeJSON(streams.Out, map[string]any{
				"contractVersion": ContractVersion,
				"outcome":         "succeeded",
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
	case "rm":
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
	case "schema":
		err = schema(cleaned[1:], streams)
	case "capabilities":
		err = capabilities(cleaned[1:], streams, opts)
	default:
		err = usageError(fmt.Sprintf("unknown command %q", cleaned[0]))
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
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if len(args) != 1 {
			return usageError("usage: tunlite run <name> or tunlite run --to user@host -L/-R/-D ...")
		}
		return runNamed(ctx, args[0], streams, opts)
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var to, identity, jump, name string
	var local, remote, dynamic, sshOptions stringsFlag
	flags.StringVar(&to, "to", "", "SSH target")
	flags.StringVar(&identity, "i", "", "identity file")
	flags.StringVar(&jump, "jump", "", "comma-separated jump hosts")
	flags.StringVar(&name, "name", "", "connection name")
	flags.Var(&local, "L", "local forward")
	flags.Var(&remote, "R", "remote forward")
	flags.Var(&dynamic, "D", "dynamic forward")
	flags.Var(&sshOptions, "ssh-opt", "OpenSSH Key=Value option")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	if to == "" {
		return usageError("run requires --to user@host[:port]")
	}

	host, port, err := sshdriver.ParseTarget(to)
	if err != nil {
		return usageError(err.Error())
	}
	forwards := make([]sshdriver.Forward, 0, len(local)+len(remote)+len(dynamic))
	for _, item := range []struct {
		flag   string
		values []string
	}{{"-L", local}, {"-R", remote}, {"-D", dynamic}} {
		for _, value := range item.values {
			forward, parseErr := sshdriver.ParseForward(item.flag, value)
			if parseErr != nil {
				return usageError(parseErr.Error())
			}
			forwards = append(forwards, forward)
		}
	}
	if len(forwards) == 0 {
		return usageError("run needs at least one forward (-L / -R / -D)")
	}
	if name == "" {
		name = sanitizeName(host)
	}
	jumpHosts, err := sshdriver.ParseJump(jump)
	if err != nil {
		return usageError(err.Error())
	}
	config := sshdriver.Config{Host: host, Port: port, IdentityFile: identity, Jump: jumpHosts, Forwards: forwards, SSHOptions: sshOptions}
	rawConfig, _ := json.Marshal(config)
	connection := core.Connection{
		APIVersion: core.APIVersion,
		Kind:       core.Kind,
		Metadata:   core.Metadata{Name: name},
		Spec: core.ConnectionSpec{
			Driver: sshdriver.ID, Lifecycle: "foreground", ConfigVersion: 1, Config: rawConfig,
		},
	}
	return executeConnection(ctx, connection, streams, opts)
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
	connection.Spec.Lifecycle = "foreground"
	return executeConnection(ctx, connection, streams, opts)
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
	if opts.DryRun {
		return renderPlan(streams, opts, connection, executionPlan)
	}

	if opts.JSON {
		_ = writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion, "outcome": "starting", "connection": connection.Metadata.Name,
			"plan": executionPlan,
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
		_ = writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         "stopped",
			"connection":      connection,
			"reason":          reason,
		})
		return
	}
	fmt.Fprintf(streams.Err, "stopped %s (%s)\n", connection, reason)
}

func renderPlan(streams IO, opts options, connection core.Connection, executionPlan plan.ExecutionPlan) error {
	if opts.JSON {
		return writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         "planned",
			"connection":      connection,
			"plan":            executionPlan,
			"dryRun":          true,
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
	var opts options
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			opts.JSON = true
		case "--non-interactive":
			opts.NonInteractive = true
		case "--dry-run":
			opts.DryRun = true
		case "--config":
			if i+1 >= len(args) {
				return nil, opts, errors.New("--config needs a file path")
			}
			i++
			opts.ConfigFile = args[i]
		default:
			if value, found := strings.CutPrefix(arg, "--config="); found {
				opts.ConfigFile = value
			} else {
				cleaned = append(cleaned, arg)
			}
		}
	}
	if opts.ConfigFile != "" {
		absolute, err := filepath.Abs(opts.ConfigFile)
		if err != nil {
			return nil, opts, err
		}
		opts.ConfigFile = absolute
	}
	return cleaned, opts, nil
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
	}
	var legacy *configstore.LegacyConfigError
	if errors.As(err, &legacy) {
		kind = "migration_required"
	}
	if opts.JSON {
		_ = writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         "failed",
			"error": map[string]any{
				"code": kind, "driver": nil, "connection": nil, "message": message,
				"riskLevel": "low", "requiresApproval": false, "actions": []any{},
			},
		})
	} else {
		fmt.Fprintln(streams.Err, message)
	}
	return code
}

func usageError(message string) error {
	return &exitError{code: 2, kind: "invalid_arguments", message: message}
}

func notFoundError(message string) error {
	return &exitError{code: 3, kind: "not_found", message: message}
}

func conflictError(message string) error {
	return &exitError{code: 2, kind: "already_exists", message: message}
}

func renderedExit(code int) error {
	return &exitError{code: code, rendered: true}
}

func connectionStore(opts options) (*configstore.Store, error) {
	path := opts.ConfigFile
	if path == "" {
		var err error
		path, err = appPaths.ConfigFile()
		if err != nil {
			return nil, err
		}
	}
	return configstore.New(path), nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
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

type stringsFlag []string

func (values *stringsFlag) String() string { return strings.Join(*values, ",") }

func (values *stringsFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}
