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
		return usageError("usage: tunlite schema [connection|connection-list|ssh|cli]")
	}
	if len(args) == 1 {
		name = args[0]
	}
	value, ok := schemas.Read(name)
	if !ok {
		return usageError(fmt.Sprintf("unknown schema %q (connection, connection-list, ssh, cli)", name))
	}
	_, err := streams.Out.Write(value)
	return err
}

func capabilities(args []string, streams IO, opts options) error {
	if len(args) != 0 {
		return usageError("usage: tunlite capabilities")
	}
	commands := []string{
		"add", "capabilities", "check", "doctor", "export", "help", "import", "list", "rename", "rm", "run", "schema", "set", "validate", "version",
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
			"available": false,
			"reason":    "native service adapters are delivered in stage 3",
		},
		"drivers": []map[string]any{{
			"id": "io.tunlite.ssh", "builtIn": true, "configVersion": 1,
			"capabilities": []string{"access", "publish", "socks", "foreground", "multiForward", "check", "diagnose"},
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
	fmt.Fprintln(streams.Out, "persistent services: unavailable until stage 3")
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
	fmt.Fprintln(streams.Out, "declarations: import, export, validate")
	fmt.Fprintln(streams.Out, "machine contract: schema, capabilities, version")
	fmt.Fprintln(streams.Out, "")
	fmt.Fprintln(streams.Out, "persistent service commands arrive in stage 3; no daemon is used")
	return nil
}
