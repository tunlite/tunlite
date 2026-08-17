package cli

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/yuanyuanzijin/tunlite/schemas"
)

func schema(args []string, streams IO) error {
	name := "connection"
	if len(args) > 1 {
		return usageError("usage: tunlite schema [connection|connection-list|ssh|cli|service-state]")
	}
	if len(args) == 1 {
		name = args[0]
	}
	value, ok := schemas.Read(name)
	if !ok {
		return usageError(fmt.Sprintf("unknown schema %q (connection, connection-list, ssh, cli, service-state)", name))
	}
	_, err := streams.Out.Write(value)
	return err
}

func capabilities(args []string, streams IO, opts options) error {
	if len(args) != 0 {
		return usageError("usage: tunlite capabilities")
	}
	commands := []string{
		"add", "capabilities", "check", "disable", "doctor", "enable", "export", "help", "import", "list", "logs", "migrate", "rename", "restart", "rm", "run", "schema", "set", "status", "sync", "validate", "version",
	}
	sort.Strings(commands)
	result := map[string]any{
		"contractVersion": ContractVersion,
		"outcome":         "succeeded",
		"version":         Version,
		"platform":        map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
		"commands":        commands,
		"executionModes":  []string{"direct"},
		"persistentServices": map[string]any{
			"available": runtime.GOOS == "linux" || runtime.GOOS == "darwin",
			"adapters":  map[string]string{"linux": "systemd", "darwin": "launchd"},
			"stateDimensions": map[string][]string{
				"desiredState": {"enabled", "disabled"},
				"processState": {"inactive", "starting", "running", "failed", "unknown"},
				"health":       {"unknown", "healthy", "unhealthy"},
				"syncState":    {"synced", "drifted", "missing", "blocked"},
			},
		},
		"drivers": []map[string]any{{
			"id": "io.tunlite.ssh", "builtIn": true, "configVersion": 1,
			"capabilities": []string{"access", "publish", "socks", "foreground", "persistent", "multiForward", "inspect", "check", "diagnose"},
		}},
		"exitCodes": map[string]int{"success": 0, "error": 1, "invalidArguments": 2, "notFound": 3, "authenticationFailed": 4, "partial": 6},
		"schemas":   schemas.Names,
	}
	if opts.JSON {
		return writeJSON(streams.Out, result)
	}
	fmt.Fprintf(streams.Out, "Tunlite %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(streams.Out, "commands: %s\n", strings.Join(commands, ", "))
	fmt.Fprintln(streams.Out, "driver: io.tunlite.ssh (access, publish, socks, multi-forward)")
	fmt.Fprintf(streams.Out, "persistent services: %t (systemd on Linux, launchd on macOS)\n", runtime.GOOS == "linux" || runtime.GOOS == "darwin")
	return nil
}

func help(streams IO, opts options) error {
	if opts.JSON {
		return capabilities(nil, streams, opts)
	}
	fmt.Fprintln(streams.Out, "Tunlite — SSH connections for developers and AI agents")
	fmt.Fprintln(streams.Out, "")
	fmt.Fprintln(streams.Out, "usage: tunlite <command> [options]")
	fmt.Fprintln(streams.Out, "")
	fmt.Fprintln(streams.Out, "connections: add, list, set, rename, rm")
	fmt.Fprintln(streams.Out, "foreground:  run, check, doctor")
	fmt.Fprintln(streams.Out, "services:    enable, disable, restart, status, logs, sync")
	fmt.Fprintln(streams.Out, "declarations: import, export, validate, migrate")
	fmt.Fprintln(streams.Out, "machine contract: schema, capabilities, version")
	fmt.Fprintln(streams.Out, "")
	fmt.Fprintln(streams.Out, "each enabled connection is owned directly by the native service manager; no daemon is used")
	return nil
}
