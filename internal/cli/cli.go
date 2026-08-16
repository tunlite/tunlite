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

	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/execution"
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
}

type exitError struct {
	code    int
	message string
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
		return renderError(streams, opts, &exitError{code: 2, message: err.Error()})
	}
	if len(cleaned) == 0 {
		return renderError(streams, opts, &exitError{code: 2, message: "usage: tunlite <run|version>"})
	}

	switch cleaned[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(streams.Out, Version)
		return 0
	case "run":
		err = run(ctx, cleaned[1:], streams, opts)
	default:
		err = &exitError{code: 2, message: fmt.Sprintf("unknown command %q", cleaned[0])}
	}
	if err == nil {
		return 0
	}
	return renderError(streams, opts, err)
}

func run(ctx context.Context, args []string, streams IO, opts options) error {
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
		return &exitError{code: 2, message: err.Error()}
	}
	if flags.NArg() != 0 {
		return &exitError{code: 2, message: fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " "))}
	}
	if to == "" {
		return &exitError{code: 2, message: "run requires --to user@host[:port]"}
	}

	host, port, err := sshdriver.ParseTarget(to)
	if err != nil {
		return &exitError{code: 2, message: err.Error()}
	}
	forwards := make([]sshdriver.Forward, 0, len(local)+len(remote)+len(dynamic))
	for _, item := range []struct {
		flag   string
		values []string
	}{{"-L", local}, {"-R", remote}, {"-D", dynamic}} {
		for _, value := range item.values {
			forward, parseErr := sshdriver.ParseForward(item.flag, value)
			if parseErr != nil {
				return &exitError{code: 2, message: parseErr.Error()}
			}
			forwards = append(forwards, forward)
		}
	}
	if len(forwards) == 0 {
		return &exitError{code: 2, message: "run needs at least one forward (-L / -R / -D)"}
	}
	if name == "" {
		name = sanitizeName(host)
	}
	config := sshdriver.Config{Host: host, Port: port, IdentityFile: identity, Forwards: forwards, SSHOptions: sshOptions}
	if jump != "" {
		for _, value := range strings.Split(jump, ",") {
			if value = strings.TrimSpace(value); value != "" {
				config.Jump = append(config.Jump, value)
			}
		}
	}
	rawConfig, _ := json.Marshal(config)
	connection := core.Connection{
		APIVersion: core.APIVersion,
		Kind:       core.Kind,
		Metadata:   core.Metadata{Name: name},
		Spec: core.ConnectionSpec{
			Driver: sshdriver.ID, Lifecycle: "foreground", ConfigVersion: 1, Config: rawConfig,
		},
	}
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
	for _, arg := range args {
		switch arg {
		case "--json":
			opts.JSON = true
		case "--non-interactive":
			opts.NonInteractive = true
		case "--dry-run":
			opts.DryRun = true
		default:
			cleaned = append(cleaned, arg)
		}
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
	if opts.JSON {
		_ = writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         "failed",
			"error": map[string]any{
				"code": "error", "message": message, "requiresApproval": false, "actions": []any{},
			},
		})
	} else {
		fmt.Fprintln(streams.Err, message)
	}
	return code
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
