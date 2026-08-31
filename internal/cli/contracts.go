package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/tunlite/tunlite/internal/service"
	"github.com/tunlite/tunlite/schemas"
)

func schema(args []string, streams IO, opts options) error {
	name := "connection"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var output string
	var force bool
	flags.StringVar(&output, "output", "", "write the raw schema to a file or - for stdout")
	flags.BoolVar(&force, "force", false, "replace an existing output file")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError("usage: tunlite schema [connection|connection-list|ssh|cli|service-state] [--output <path|->] [--force]")
	}
	value, ok := schemas.Read(name)
	if !ok {
		return usageError(fmt.Sprintf("unknown schema %q (connection, connection-list, ssh, cli, service-state)", name))
	}
	if output == "-" {
		if opts.DryRun {
			return usageError("schema --dry-run requires --output <file>")
		}
		if opts.JSON {
			return usageError("--json cannot be combined with --output -")
		}
		_, err := streams.Out.Write(value)
		return err
	}
	if output != "" {
		absolute, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		if opts.DryRun {
			return renderSchemaWrite(streams, opts, "planned", name, absolute)
		}
		if err := writeRawDocument(absolute, value, force); err != nil {
			if errors.Is(err, os.ErrExist) {
				return conflictError(fmt.Sprintf("output file already exists: %s (use --force to replace it)", absolute))
			}
			return err
		}
		return renderSchemaWrite(streams, opts, "applied", name, absolute)
	}
	if opts.DryRun {
		return usageError("schema --dry-run requires --output <file>")
	}
	if opts.JSON {
		return writeResponse(streams.Out, map[string]any{
			"outcome": "succeeded", "name": name, "schema": json.RawMessage(value),
		})
	}
	_, err := streams.Out.Write(value)
	return err
}

func renderSchemaWrite(streams IO, opts options, outcome, name, path string) error {
	if opts.JSON {
		return writeResponse(streams.Out, map[string]any{
			"outcome": outcome, "name": name, "path": path, "dryRun": opts.DryRun,
		})
	}
	verb := "wrote"
	if opts.DryRun {
		verb = "would write"
	}
	fmt.Fprintf(streams.Out, "%s %s schema to %s\n", verb, name, path)
	return nil
}

func writeRawDocument(path string, value []byte, force bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if !force {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.Write(value); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return err
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return err
		}
		return nil
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tunlite-schema-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	installed := false
	defer func() {
		_ = temporary.Close()
		if !installed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	installed = true
	return nil
}

// publicCommands is shared by help, suggestions, capabilities and the Agent
// contract so the public surface cannot drift between renderers.
var publicCommands = []string{
	"add", "capabilities", "check", "disable", "doctor", "enable", "export", "help", "import", "list", "logs", "manage", "monitor", "remove", "rename", "resolve", "restart", "run", "schema", "set", "start", "status", "stop", "unmanage", "validate", "version",
}

func capabilities(args []string, streams IO, opts options) error {
	if len(args) != 0 {
		return usageError("usage: tunlite capabilities")
	}
	commands := append([]string(nil), publicCommands...)
	sort.Strings(commands)
	adapter, adapterErr := opts.newServiceAdapter()
	nativeAvailable := adapterErr == nil && adapter.Capabilities().Persistence
	nativeAdapter := ""
	nativeCapabilities := map[string]bool{
		"registration": false, "background": false, "persistence": false, "logs": false, "discovery": false,
		"userScope": false, "systemScope": false,
	}
	supportedScopes := []string{}
	var defaultScope any
	interactiveElevation := "unsupported"
	nonInteractiveElevation := "unsupported"
	if adapterErr == nil {
		capabilities := adapter.Capabilities()
		userScope := service.SupportsScope(adapter, service.ScopeUser)
		systemScope := service.SupportsScope(adapter, service.ScopeSystem)
		nativeAdapter = adapter.Name()
		nativeCapabilities = map[string]bool{
			"registration": capabilities.Registration, "background": capabilities.Background,
			"persistence": capabilities.Persistence, "logs": capabilities.Logs, "discovery": capabilities.Discovery,
			"userScope": userScope, "systemScope": systemScope,
		}
		if userScope {
			supportedScopes = append(supportedScopes, string(service.ScopeUser))
			defaultScope = string(service.ScopeUser)
		}
		if systemScope {
			supportedScopes = append(supportedScopes, string(service.ScopeSystem))
			if defaultScope == nil {
				// Native service creation still requires an explicit --scope when
				// the login domain is unavailable; advertise no implicit default.
				defaultScope = nil
			}
			nonInteractiveElevation = "external"
			interactiveElevation = "sudo-v"
		}
	}
	result := map[string]any{
		"outcome":        "succeeded",
		"version":        Version,
		"platform":       map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
		"commands":       commands,
		"executionModes": []string{"direct"},
		"nativeServices": map[string]any{
			"available": nativeAvailable, "adapter": nativeAdapter, "capabilities": nativeCapabilities,
			"scope": map[string]any{
				"default": defaultScope, "supported": supportedScopes,
				"migration":               nativeCapabilities["userScope"] && nativeCapabilities["systemScope"],
				"systemRequiresElevation": nativeCapabilities["systemScope"],
				"interactiveElevation":    interactiveElevation,
				"nonInteractiveElevation": nonInteractiveElevation,
			},
			"adapters": map[string]string{"linux": "systemd", "darwin": "launchd"},
			"stateDimensions": map[string][]string{
				"scope":            {"user", "system"},
				"serviceState":     {"registered", "absent", "unsupported", "unknown"},
				"mode":             {"stopped", "foreground", "background", "persistent", "unknown"},
				"processState":     {"inactive", "starting", "running", "failed", "unknown"},
				"health":           {"unknown", "healthy", "unhealthy"},
				"consistencyState": {"consistent", "drifted", "missing", "blocked"},
			},
		},
		"driverSelection": map[string]any{"option": "--driver", "default": "ssh", "changeViaSet": false},
		"drivers": []map[string]any{{
			"id": "io.tunlite.ssh", "name": "ssh", "builtIn": true, "default": true,
			"argumentSyntax": []string{"-N", "-T", "-p", "-i", "-J", "-L", "-R", "-D", "-o", "user@host"},
			"setClearFields": []string{"identity", "jump", "options"},
			"capabilities":   []string{"access", "publish", "socks", "foreground", "persistent", "multiForward", "inspect", "check", "diagnose"},
		}},
		"exitCodes": map[string]int{"success": 0, "error": 1, "invalidArguments": 2, "notFound": 3, "interactionRequired": 4, "partial": 5},
		"schemas":   schemas.Names,
	}
	if opts.JSON {
		return writeResponse(streams.Out, result)
	}
	fmt.Fprintf(streams.Out, "Tunlite %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(streams.Out, "commands: %s\n", strings.Join(commands, ", "))
	fmt.Fprintln(streams.Out, "driver: io.tunlite.ssh (access, publish, socks, multi-forward)")
	fmt.Fprintf(streams.Out, "native services: %t (systemd on Linux, launchd on macOS)\n", nativeAvailable)
	return nil
}

func help(args []string, streams IO, opts options) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		args = nil
	}
	if len(args) > 1 {
		return usageError("usage: tunlite help [command]")
	}
	if len(args) == 1 {
		return commandHelp(args[0], streams, opts)
	}
	if opts.JSON {
		return capabilities(nil, streams, opts)
	}
	if isChinese(opts.Language) {
		fmt.Fprintln(streams.Out, "Tunlite — 面向开发者和 AI Agent 的 SSH 连接工具")
		fmt.Fprintln(streams.Out)
		fmt.Fprintln(streams.Out, "用法：tunlite <命令> [选项]")
		fmt.Fprintln(streams.Out)
		fmt.Fprintln(streams.Out, "连接管理：add, list, set, rename, remove")
		fmt.Fprintln(streams.Out, "运行检查：run, check, doctor, monitor")
		fmt.Fprintln(streams.Out, "后台服务：start, stop, enable, disable, restart, status, logs")
		fmt.Fprintln(streams.Out, "服务接管：manage, unmanage, resolve")
		fmt.Fprintln(streams.Out, "声明文件：import, export, validate")
		fmt.Fprintln(streams.Out, "机器接口：schema, capabilities, version")
		fmt.Fprintln(streams.Out, "\n运行方式：")
		fmt.Fprintln(streams.Out, "  run      在当前终端运行，退出终端即停止")
		fmt.Fprintln(streams.Out, "  start    在系统后台运行，但不设置自动启动")
		fmt.Fprintln(streams.Out, "  stop     停止临时后台连接，不修改自动启动设置")
		fmt.Fprintln(streams.Out, "  enable   在系统后台运行，并开启自动启动")
		fmt.Fprintln(streams.Out, "  disable  停止连接，并关闭自动启动")
		fmt.Fprintln(streams.Out, "  restart  重启连接，不修改自动启动设置")
		fmt.Fprintln(streams.Out, "\n语言：--lang en|zh-CN；也可设置 TUNLITE_LANG，默认跟随系统语言。")
		return nil
	}
	fmt.Fprintln(streams.Out, "Tunlite — SSH connections for developers and AI agents")
	fmt.Fprintln(streams.Out)
	fmt.Fprintln(streams.Out, "usage: tunlite <command> [options]")
	fmt.Fprintln(streams.Out)
	fmt.Fprintln(streams.Out, "connections: add, list, set, rename, remove")
	fmt.Fprintln(streams.Out, "foreground:  run, check, doctor, monitor")
	fmt.Fprintln(streams.Out, "services:    start, stop, enable, disable, restart, status, logs")
	fmt.Fprintln(streams.Out, "ownership:   manage, unmanage, resolve")
	fmt.Fprintln(streams.Out, "declarations: import, export, validate")
	fmt.Fprintln(streams.Out, "machine contract: schema, capabilities, version")
	fmt.Fprintln(streams.Out, "\nruntime model:")
	fmt.Fprintln(streams.Out, "  run      current terminal; stops when the terminal exits")
	fmt.Fprintln(streams.Out, "  start    system background; does not enable autostart")
	fmt.Fprintln(streams.Out, "  stop     stops a temporary background connection")
	fmt.Fprintln(streams.Out, "  enable   system background with autostart enabled")
	fmt.Fprintln(streams.Out, "  disable  stops the connection and turns off autostart")
	fmt.Fprintln(streams.Out, "  restart  restarts without changing autostart")
	fmt.Fprintln(streams.Out, "\nlanguage: --lang en|zh-CN, or TUNLITE_LANG; defaults to the system locale")
	return nil
}

func commandHelp(command string, streams IO, opts options) error {
	if !containsString(publicCommands, command) {
		message := fmt.Sprintf("unknown command %q", command)
		if suggestion := suggestCommand(command); suggestion != "" {
			message += fmt.Sprintf("; did you mean %q?", suggestion)
		}
		return usageError(message)
	}
	if opts.JSON {
		return writeResponse(streams.Out, map[string]any{"outcome": "succeeded", "command": command, "usage": commandUsage(command)})
	}
	usage := commandUsage(command)
	if isChinese(opts.Language) {
		usage = "用法：" + strings.TrimPrefix(usage, "usage: ")
	}
	fmt.Fprintln(streams.Out, usage)
	if command == "enable" {
		if isChinese(opts.Language) {
			fmt.Fprintln(streams.Out, "范围：新服务默认 user；已有服务不传 --scope 时保留原范围。system 是否可用以 capabilities 为准。交互终端会在写入前通过 sudo 请求一次管理员授权；--non-interactive 只使用已预置授权。")
		} else {
			fmt.Fprintln(streams.Out, "Scope: new services default to user; existing services keep their scope unless --scope is set. Check capabilities for system support. Before a change enters or leaves system scope, an interactive terminal requests administrator authorization once through sudo; --non-interactive only uses pre-authorized access.")
		}
	}
	if command == "set" {
		if isChinese(opts.Language) {
			fmt.Fprintln(streams.Out, "行为：")
			fmt.Fprintln(streams.Out, "  - 传入任意 -L/-R/-D 时，会用本次参数整体替换现有转发列表；不传则保持不变。")
			fmt.Fprintln(streams.Out, "  - 传入任意 -o 时，会用本次参数整体替换现有 SSH 选项；--clear options 会清空它们。")
			fmt.Fprintln(streams.Out, "  - 如果连接正在临时或长期后台运行，执行配置变更可能导致短暂重启。")
		} else {
			fmt.Fprintln(streams.Out, "Behavior:")
			fmt.Fprintln(streams.Out, "  - Supplying any -L/-R/-D replaces the entire forwarding list with the values in this command; omitting them leaves it unchanged.")
			fmt.Fprintln(streams.Out, "  - Supplying any -o replaces the entire SSH options list; --clear options empties it.")
			fmt.Fprintln(streams.Out, "  - Execution changes may briefly restart a connection that is running in temporary or persistent background mode.")
		}
	}
	if command == "unmanage" {
		if isChinese(opts.Language) {
			fmt.Fprintln(streams.Out, "影响：只移除 Tunlite 的声明和管理记录。原生后台服务保持安装，当前运行状态和自动启动设置不变。")
		} else {
			fmt.Fprintln(streams.Out, "Effect: removes only Tunlite's declaration and management record. The native service remains installed; its current running state and autostart setting stay unchanged.")
		}
	}
	if command == "rename" {
		if isChinese(opts.Language) {
			fmt.Fprintln(streams.Out, "影响：只更改用户看到的 Connection 名称。底层 serviceKey、unitName 和原生服务标识保持不变，因此正在运行的连接不会中断。")
		} else {
			fmt.Fprintln(streams.Out, "Effect: changes only the user-facing Connection name. The underlying serviceKey, unitName, and native identity stay unchanged, so a running connection is not interrupted.")
		}
	}
	return nil
}

func commandUsage(command string) string {
	usage := map[string]string{
		"add":          "usage: tunlite add <name> [--driver ssh] [--group group] [-p port] [-i key] [-J jump] -L/-R/-D ... user@host",
		"set":          "usage: tunlite set <name> [-p port] [-i key] [-J jump] [-L/-R/-D ...] [-o Key=Value ...] [--clear identity|jump|options ...] [user@host] [--group group|--no-group]",
		"remove":       "usage: tunlite remove <name>",
		"rename":       "usage: tunlite rename <name> <new-name>",
		"run":          "usage: tunlite run <name>",
		"start":        "usage: tunlite start <name> | --group <group> | --all",
		"stop":         "usage: tunlite stop <name> | --group <group> | --all",
		"enable":       "usage: tunlite enable (<name> | --group <group> | --all) [--scope user|system]",
		"disable":      "usage: tunlite disable <name> | --group <group> | --all",
		"restart":      "usage: tunlite restart <name> | --group <group> | --all",
		"status":       "usage: tunlite status [<name> | --group <group> | --all]",
		"monitor":      "usage: tunlite monitor [--group group] [--interval seconds]",
		"logs":         "usage: tunlite logs <name> [--lines N] [--follow]",
		"doctor":       "usage: tunlite doctor [name] [--offline] [--timeout seconds]",
		"check":        "usage: tunlite check <connection-or-user@host> [-p port] [-i key] [-J host] [--timeout seconds]",
		"manage":       "usage: tunlite manage [native-service] [--name name]",
		"unmanage":     "usage: tunlite unmanage <name>",
		"resolve":      "usage: tunlite resolve <name> --use tunlite|system",
		"import":       "usage: tunlite import <path> [--force]",
		"export":       "usage: tunlite export [--output path|-] [--force]",
		"validate":     "usage: tunlite validate [path]",
		"schema":       "usage: tunlite schema [connection|connection-list|ssh|cli|service-state] [--output path|-] [--force]",
		"capabilities": "usage: tunlite capabilities",
		"version":      "usage: tunlite version",
		"list":         "usage: tunlite list [--group group]",
		"help":         "usage: tunlite help [command]",
	}
	if value := usage[command]; value != "" {
		return value
	}
	return "usage: tunlite " + command + " [options]"
}

func containsHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func suggestCommand(value string) string {
	best, distance := "", 4
	for _, candidate := range publicCommands {
		if current := editDistance(value, candidate); current < distance {
			best, distance = candidate, current
		}
	}
	return best
}

func editDistance(left, right string) int {
	a, b := []rune(left), []rune(right)
	if len(a) == len(b) {
		for index := 0; index+1 < len(a); index++ {
			swapped := append([]rune(nil), a...)
			swapped[index], swapped[index+1] = swapped[index+1], swapped[index]
			if string(swapped) == string(b) {
				return 1
			}
		}
	}
	row := make([]int, len(b)+1)
	for index := range row {
		row[index] = index
	}
	for i, ac := range a {
		previous := row[0]
		row[0] = i + 1
		for j, bc := range b {
			old := row[j+1]
			cost := 0
			if ac != bc {
				cost = 1
			}
			row[j+1] = minInt(row[j+1]+1, row[j]+1, previous+cost)
			previous = old
		}
	}
	return row[len(b)]
}

func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}
