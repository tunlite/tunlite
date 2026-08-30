package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuanyuanzijin/tunlite/internal/configstore"
	"github.com/yuanyuanzijin/tunlite/internal/core"
	sshdriver "github.com/yuanyuanzijin/tunlite/internal/drivers/ssh"
	"github.com/yuanyuanzijin/tunlite/internal/service"
)

func checkSSH(ctx context.Context, args []string, streams IO, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError("usage: tunlite check <connection-or-user@host> [-p port] [-i key] [-J host] [--timeout seconds]")
	}
	target := args[0]
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var identity, jump, timeoutValue string
	var port int
	flags.IntVar(&port, "p", 0, "SSH port")
	flags.StringVar(&identity, "i", "", "identity file")
	flags.StringVar(&jump, "J", "", "comma-separated jump hosts")
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
	if port != 0 && (port < 1 || port > 65535) {
		return usageError(fmt.Sprintf("invalid SSH port %d", port))
	}
	renderCommandProgress(streams, opts, append([]string{"check", target}, args...))
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, err := store.Load()
	if err != nil {
		return err
	}
	connection, err := resolveSSHConnection(document, target, port, identity, jump)
	if err != nil {
		return err
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
		_ = writeResponse(streams.Out, map[string]any{
			"outcome": map[bool]string{true: "succeeded", false: "failed"}[report.Healthy],
			"report":  report,
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

func resolveSSHConnection(document configstore.Document, target string, port int, identity, jump string) (core.Connection, error) {
	if _, stored := configstore.Find(&document, target); stored != nil {
		connection := cloneConnection(*stored)
		if connection.Spec.Driver != sshdriver.ID {
			return core.Connection{}, usageError("command requires an SSH connection")
		}
		if port == 0 && identity == "" && jump == "" {
			return connection, nil
		}
		config, err := sshdriver.DecodeConfig(connection.Spec.Config)
		if err != nil {
			return core.Connection{}, err
		}
		if identity != "" {
			config.IdentityFile = identity
		}
		if port != 0 {
			config.Port = port
		}
		if jump != "" {
			config.Jump, err = sshdriver.ParseJump(jump)
			if err != nil {
				return core.Connection{}, usageError(err.Error())
			}
		}
		connection.Spec.Config, _ = json.Marshal(config)
		return connection, nil
	}
	targetSpec, err := sshdriver.ParseTargetSpec(target)
	if err != nil {
		return core.Connection{}, notFoundError(fmt.Sprintf("no such connection and invalid SSH target: %s", target))
	}
	if targetSpec.PortSpecified && !targetSpec.IPv6 {
		return core.Connection{}, usageError(fmt.Sprintf("SSH target must not contain a port; use -p %d %s", targetSpec.Port, targetSpec.Host))
	}
	if targetSpec.PortSpecified {
		if port != 0 {
			return core.Connection{}, usageError("SSH port may not be set by both -p and the IPv6 target")
		}
		port = targetSpec.Port
	}
	if port == 0 {
		port = 22
	}
	jumps, err := sshdriver.ParseJump(jump)
	if err != nil {
		return core.Connection{}, usageError(err.Error())
	}
	host := targetSpec.Host
	config := sshdriver.Config{Host: host, Port: port, IdentityFile: identity, Jump: jumps, Forwards: []sshdriver.Forward{}}
	raw, _ := json.Marshal(config)
	return core.Connection{
		ConfigVersion: core.ConfigVersion, Kind: core.Kind, Metadata: core.Metadata{Name: sanitizeName("check-" + host)},
		Spec: core.ConnectionSpec{Driver: sshdriver.ID, Config: raw},
	}, nil
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
	progressArgs := []string{"doctor"}
	if name != "" {
		progressArgs = append(progressArgs, name)
	}
	renderCommandProgress(streams, opts, progressArgs)
	store, err := connectionStore(opts)
	if err != nil {
		return err
	}
	document, loadErr := store.Load()
	conditions := doctorEnvironmentConditions(store.Path(), loadErr, opts)
	targets := document.Items
	if name != "" {
		if loadErr != nil {
			targets = nil
		} else {
			_, connection := configstore.Find(&document, name)
			if connection == nil {
				return notFoundError(fmt.Sprintf("no such connection: %s", name))
			}
			targets = []core.Connection{*connection}
		}
	}
	registry, err := core.NewRegistry(sshdriver.New())
	if err != nil {
		return err
	}
	planner := core.NewPlanner(registry)
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
		if _, err := planner.Plan(ctx, foreground, core.PlanOptions{NonInteractive: true}); err != nil {
			conditions = append(conditions, diagnosticCondition(connection, "execution_plan_failed", "error", err.Error(), nil))
			continue
		}
		running := doctorConnectionRunning(ctx, connection, opts)
		conditions = append(conditions, endpointAvailability(connection, running)...)
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
		"outcome": outcome, "healthy": healthy,
		"observedAt": time.Now().UTC(), "conditions": conditions,
	}
	if opts.JSON {
		_ = writeResponse(streams.Out, result)
	} else if len(conditions) == 0 {
		glyph, shade := doctorStyle("info")
		fmt.Fprintf(streams.Out, "%s %s\n", colorize(glyph, shade, outputUsesColor(streams, opts)), localized(opts.Language,
			"No connections yet; the configuration is valid.", "还没有连接；当前配置有效。"))
	} else {
		color := outputUsesColor(streams, opts)
		counts := map[string]int{}
		for _, item := range conditions {
			glyph, shade := doctorStyle(item.Severity)
			counts[item.Severity]++
			scope := item.Connection
			if scope == "" {
				scope = localized(opts.Language, "system", "系统")
			}
			fmt.Fprintf(streams.Out, "%s %-16s %s\n", colorize(glyph, shade, color), scope, doctorConditionMessage(item, opts.Language))
			if fix := doctorFix(item, opts.Language); fix != "" {
				fmt.Fprintln(streams.Out, colorize("    ↳ "+localized(opts.Language, "fix: ", "处理：")+fix, ansiDim, color))
			}
		}
		if isChinese(opts.Language) {
			fmt.Fprintf(streams.Out, "\n共 %d 项检查：%d 项通过，%d 项警告，%d 项失败\n",
				len(conditions), counts["info"], counts["warning"], counts["error"])
		} else {
			fmt.Fprintf(streams.Out, "\n%d checks: %d passed, %d warnings, %d failed\n",
				len(conditions), counts["info"], counts["warning"], counts["error"])
		}
	}
	if !healthy {
		return renderedExit(1)
	}
	return nil
}

func doctorFix(condition core.Condition, lang language) string {
	for _, action := range condition.Actions {
		switch action.Type {
		case "setup_ssh_key":
			target := fmt.Sprint(action.Parameters["host"])
			if target != "" {
				return localized(lang, "configure OpenSSH key authentication for ", "为以下主机配置 OpenSSH 密钥认证：") + target
			}
		case "repair_configuration":
			return localized(lang, "inspect ", "检查 ") + fmt.Sprint(action.Parameters["path"])
		case "inspect_local_endpoint":
			return localized(lang, "free or change local endpoint ", "释放或更改本地端点 ") + fmt.Sprint(action.Parameters["address"])
		}
	}
	return ""
}

func doctorConditionMessage(condition core.Condition, lang language) string {
	if !isChinese(lang) {
		return condition.Message
	}
	detail := func(prefix string) string { return strings.TrimPrefix(condition.Message, prefix) }
	switch condition.Code {
	case "config_valid":
		return "配置有效：" + strings.TrimSuffix(strings.TrimPrefix(condition.Message, "configuration is valid ("), ")")
	case "config_invalid":
		return "配置无效。详细信息：" + condition.Message
	case "ssh_client_found":
		return "已找到 OpenSSH 客户端：" + detail("OpenSSH client found at ")
	case "ssh_client_missing", "provider_missing":
		return "未找到 OpenSSH 客户端。详细信息：" + condition.Message
	case "ssh_keygen_found":
		return "已找到 ssh-keygen：" + detail("ssh-keygen found at ")
	case "ssh_keygen_missing":
		return "未找到 ssh-keygen；仍可使用现有密钥和 SSH Agent。"
	case "ssh_identity_found":
		return "已找到默认 SSH 密钥：" + detail("default SSH identity found at ")
	case "ssh_agent_found":
		return "SSH Agent 可用。"
	case "ssh_identity_unknown":
		return "未检测到默认 SSH 私钥或 SSH Agent；仍可显式指定密钥文件。"
	case "native_service_available":
		return "原生后台服务可用。详细信息：" + condition.Message
	case "native_service_unavailable":
		return "原生后台服务不可用。详细信息：" + condition.Message
	case "endpoint_available":
		address := strings.TrimSuffix(strings.TrimPrefix(condition.Message, "local endpoint "), " is available")
		return "本地端点 " + address + " 可用。"
	case "endpoint_accepting":
		return "Connection 运行中，本地端点已被占用（符合预期）。详细信息：" + condition.Message
	case "endpoint_in_use":
		return "本地端点已被其他进程占用。详细信息：" + condition.Message
	case "remote_endpoint_not_probed":
		return "远程端点由 SSH 服务器管理，本机未探测。详细信息：" + condition.Message
	case "ssh_authentication_succeeded":
		return "SSH 认证成功。详细信息：" + condition.Message
	case "ssh_authentication_failed":
		return "SSH 认证失败。详细信息：" + condition.Message
	case "ssh_host_key_untrusted":
		return "SSH 主机密钥尚未受信任。详细信息：" + condition.Message
	case "ssh_dns_failed":
		return "SSH 主机名解析失败。详细信息：" + condition.Message
	case "ssh_unreachable":
		return "无法连接 SSH 主机。详细信息：" + condition.Message
	case "ssh_check_timeout":
		return "SSH 检查超时。详细信息：" + condition.Message
	default:
		return "检查结果（" + condition.Code + "）：" + condition.Message
	}
}

func doctorEnvironmentConditions(configPath string, configErr error, opts options) []core.Condition {
	conditions := make([]core.Condition, 0, 6)
	add := func(code, severity, message string, actions []core.Action) {
		if actions == nil {
			actions = []core.Action{}
		}
		conditions = append(conditions, core.Condition{Code: code, Severity: severity, Message: message, Actions: actions})
	}
	if configErr != nil {
		add("config_invalid", "error", fmt.Sprintf("configuration %s is invalid: %v", configPath, configErr),
			[]core.Action{{Type: "repair_configuration", Parameters: map[string]any{"path": configPath}}})
	} else {
		add("config_valid", "info", "configuration is valid ("+configPath+")", nil)
	}
	sshBinary := os.Getenv("TUNLITE_SSH")
	if sshBinary == "" {
		sshBinary = "ssh"
	}
	if resolved, err := exec.LookPath(sshBinary); err != nil {
		add("ssh_client_missing", "error", fmt.Sprintf("OpenSSH client not found (%s)", sshBinary),
			[]core.Action{{Type: "install_provider", Parameters: map[string]any{"provider": "openssh"}}})
	} else {
		add("ssh_client_found", "info", "OpenSSH client found at "+resolved, nil)
	}
	if resolved, err := exec.LookPath("ssh-keygen"); err != nil {
		add("ssh_keygen_missing", "warning", "ssh-keygen is not available; existing keys and SSH agents still work", nil)
	} else {
		add("ssh_keygen_found", "info", "ssh-keygen found at "+resolved, nil)
	}
	if key := defaultSSHIdentity(); key != "" {
		add("ssh_identity_found", "info", "default SSH identity found at "+key, nil)
	} else if os.Getenv("SSH_AUTH_SOCK") != "" {
		add("ssh_agent_found", "info", "SSH agent is available", nil)
	} else {
		add("ssh_identity_unknown", "warning", "no default SSH private key or SSH agent was detected; explicit identity files still work", nil)
	}
	if adapter, err := opts.newServiceAdapter(); err != nil {
		add("native_service_unavailable", "warning", "native background services are unavailable: "+err.Error(), nil)
	} else {
		caps := adapter.Capabilities()
		add("native_service_available", "info", fmt.Sprintf("native service adapter %s is available (background=%t, persistence=%t)", adapter.Name(), caps.Background, caps.Persistence), nil)
	}
	return conditions
}

func defaultSSHIdentity() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		path := filepath.Join(home, ".ssh", name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func doctorConnectionRunning(ctx context.Context, connection core.Connection, opts options) bool {
	adapter, records, config, _, err := serviceDependencies(opts)
	if err != nil {
		return false
	}
	result := statusService(ctx, adapter, records, connection, config.Path())
	return result.State != nil && result.State.ProcessState == service.ProcessRunning
}

func endpointAvailability(connection core.Connection, running bool) []core.Condition {
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
			if running {
				conditions = append(conditions, diagnosticCondition(connection, "endpoint_accepting", "info",
					fmt.Sprintf("local endpoint %s is occupied while the Connection is running", address), nil))
				continue
			}
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
