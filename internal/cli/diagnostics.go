package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
)

func checkSSH(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite check <connection-or-user@host> [-i key] [--jump host] [--timeout seconds]")
	}
	target := args[0]
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var identity, jump, timeoutValue string
	flags.StringVar(&identity, "i", "", "identity file")
	flags.StringVar(&jump, "jump", "", "comma-separated jump hosts")
	flags.StringVar(&timeoutValue, "timeout", "12", "timeout in seconds")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	timeout, err := parseTimeout(timeoutValue)
	if err != nil {
		return err
	}
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	var connection core.Connection
	if _, stored := configstore.Find(&document, target); stored != nil {
		connection = cloneConnection(*stored)
		connection.Spec.Lifecycle = "foreground"
		if identity != "" || jump != "" {
			if connection.Spec.Driver != sshdriver.ID || connection.Spec.ConfigVersion != 1 {
				return usageError("SSH overrides can only be used with an SSH connection")
			}
			config, err := sshdriver.DecodeConfig(connection.Spec.Config)
			if err != nil {
				return err
			}
			if identity != "" {
				config.IdentityFile = identity
			}
			if jump != "" {
				config.Jump, err = sshdriver.ParseJump(jump)
				if err != nil {
					return usageError(err.Error())
				}
			}
			connection.Spec.Config, _ = json.Marshal(config)
		}
	} else {
		host, port, err := sshdriver.ParseTarget(target)
		if err != nil {
			return notFoundError(fmt.Sprintf("no such connection and invalid SSH target: %s", target))
		}
		jumps, err := sshdriver.ParseJump(jump)
		if err != nil {
			return usageError(err.Error())
		}
		config := sshdriver.Config{Host: host, Port: port, IdentityFile: identity, Jump: jumps, Forwards: []sshdriver.Forward{}}
		raw, _ := json.Marshal(config)
		connection = core.Connection{
			APIVersion: core.APIVersion, Kind: core.Kind, Metadata: core.Metadata{Name: sanitizeName("check-" + host)},
			Spec: core.ConnectionSpec{Driver: sshdriver.ID, Lifecycle: "foreground", ConfigVersion: 1, Config: raw},
		}
	}
	registry, err := core.NewRegistry(sshdriver.New())
	if err != nil {
		return err
	}
	report, err := core.NewPlanner(registry).Check(ctx, connection, core.CheckOptions{Timeout: timeout})
	if err != nil {
		return err
	}
	if opts.JSON {
		_ = writeJSON(streams.Out, map[string]any{
			"contractVersion": ContractVersion,
			"outcome":         map[bool]string{true: "succeeded", false: "failed"}[report.Healthy],
			"report":          report,
		})
	} else {
		for _, item := range report.Conditions {
			fmt.Fprintf(streams.Out, "%s: %s\n", item.Code, item.Message)
		}
	}
	if !report.Healthy {
		for _, item := range report.Conditions {
			if item.Code == "ssh_authentication_failed" {
				return renderedExit(4)
			}
		}
		return renderedExit(1)
	}
	return nil
}

func doctor(ctx context.Context, args []string, streams IO, opts options) error {
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var offline bool
	var timeoutValue string
	flags.BoolVar(&offline, "offline", false, "skip network authentication checks")
	flags.StringVar(&timeoutValue, "timeout", "12", "per-connection timeout in seconds")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	timeout, err := parseTimeout(timeoutValue)
	if err != nil {
		return err
	}
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	targets := document.Items
	if name != "" {
		_, connection := configstore.Find(&document, name)
		if connection == nil {
			return notFoundError(fmt.Sprintf("no such connection: %s", name))
		}
		targets = []core.Connection{*connection}
	}
	registry, err := core.NewRegistry(sshdriver.New())
	if err != nil {
		return err
	}
	planner := core.NewPlanner(registry)
	conditions := make([]core.Condition, 0)
	for _, connection := range targets {
		if err := validateKnownDriver(connection); err != nil {
			conditions = append(conditions, diagnosticCondition(connection, "invalid_driver_config", "error", err.Error(), nil))
			continue
		}
		if connection.Spec.Driver != sshdriver.ID {
			conditions = append(conditions, diagnosticCondition(connection, "driver_not_installed", "warning",
				fmt.Sprintf("driver %q is not installed", connection.Spec.Driver),
				[]core.Action{{Type: "inspect_driver", Parameters: map[string]any{"driver": connection.Spec.Driver}}}))
			continue
		}
		foreground := cloneConnection(connection)
		foreground.Spec.Lifecycle = "foreground"
		if _, err := planner.Plan(ctx, foreground, core.PlanOptions{NonInteractive: true}); err != nil {
			conditions = append(conditions, diagnosticCondition(connection, "execution_plan_failed", "error", err.Error(), nil))
			continue
		}
		conditions = append(conditions, endpointAvailability(connection)...)
		conditions = append(conditions, endpointConflicts(&document, connection)...)
		if !offline {
			report, err := planner.Check(ctx, foreground, core.CheckOptions{Timeout: timeout})
			if err != nil {
				conditions = append(conditions, diagnosticCondition(connection, "driver_check_failed", "error", err.Error(), nil))
			} else {
				conditions = append(conditions, report.Conditions...)
			}
		}
	}
	sort.SliceStable(conditions, func(i, j int) bool {
		if conditions[i].Connection == conditions[j].Connection {
			return conditions[i].Code < conditions[j].Code
		}
		return conditions[i].Connection < conditions[j].Connection
	})
	healthy := true
	for _, item := range conditions {
		if item.Severity == "error" {
			healthy = false
			break
		}
	}
	outcome := "succeeded"
	if !healthy {
		outcome = "failed"
	}
	result := map[string]any{
		"contractVersion": ContractVersion, "outcome": outcome, "healthy": healthy,
		"observedAt": time.Now().UTC(), "conditions": conditions,
	}
	if opts.JSON {
		_ = writeJSON(streams.Out, result)
	} else if len(conditions) == 0 {
		fmt.Fprintln(streams.Out, "no connections defined; configuration is valid")
	} else {
		for _, item := range conditions {
			fmt.Fprintf(streams.Out, "%s\t%s\t%s\n", item.Severity, item.Connection, item.Message)
		}
	}
	if !healthy {
		return renderedExit(1)
	}
	return nil
}

func endpointAvailability(connection core.Connection) []core.Condition {
	config, err := sshdriver.DecodeConfig(connection.Spec.Config)
	if err != nil {
		return nil
	}
	conditions := make([]core.Condition, 0, len(config.Forwards))
	for _, forward := range config.Forwards {
		if forward.Type == "remote" {
			conditions = append(conditions, diagnosticCondition(connection, "remote_endpoint_not_probed", "info",
				fmt.Sprintf("remote endpoint %s:%d is managed on the SSH server", defaultBind(forward.Bind), forward.SrcPort), nil))
			continue
		}
		address := net.JoinHostPort(defaultBind(forward.Bind), strconv.Itoa(forward.SrcPort))
		listener, err := net.Listen("tcp", address)
		if err != nil {
			conditions = append(conditions, diagnosticCondition(connection, "endpoint_in_use", "warning",
				fmt.Sprintf("local endpoint %s is already in use", address),
				[]core.Action{{Type: "inspect_local_endpoint", Parameters: map[string]any{"address": address}}}))
			continue
		}
		_ = listener.Close()
		conditions = append(conditions, diagnosticCondition(connection, "endpoint_available", "info",
			fmt.Sprintf("local endpoint %s is available", address), nil))
	}
	return conditions
}

func diagnosticCondition(connection core.Connection, code, severity, message string, actions []core.Action) core.Condition {
	if actions == nil {
		actions = []core.Action{}
	}
	return core.Condition{
		Code: code, Severity: severity, Message: message, Driver: connection.Spec.Driver,
		Connection: connection.Metadata.Name, Actions: actions,
	}
}

func parseTimeout(value string) (time.Duration, error) {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0.25 || seconds > 120 {
		return 0, usageError("timeout must be between 0.25 and 120 seconds")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
