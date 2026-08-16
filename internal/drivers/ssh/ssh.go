package ssh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yuanyuanzijin/tunlite/internal/core"
	"github.com/yuanyuanzijin/tunlite/internal/plan"
)

const ID = "io.tunlite.ssh"

type Config struct {
	Host         string    `json:"host"`
	Port         int       `json:"port,omitempty"`
	IdentityFile string    `json:"identityFile,omitempty"`
	Jump         []string  `json:"jump,omitempty"`
	Forwards     []Forward `json:"forwards"`
	SSHOptions   []string  `json:"sshOptions,omitempty"`
}

type Forward struct {
	Type     string `json:"type"`
	Bind     string `json:"bind"`
	SrcPort  int    `json:"srcPort"`
	DestHost string `json:"destHost,omitempty"`
	DestPort int    `json:"destPort,omitempty"`
}

type Driver struct {
	lookup func(string) (string, error)
}

func New() *Driver {
	return &Driver{lookup: exec.LookPath}
}

func NewWithLookup(lookup func(string) (string, error)) *Driver {
	return &Driver{lookup: lookup}
}

func (d *Driver) ID() string { return ID }

func (d *Driver) Plan(_ context.Context, connection core.Connection, options core.PlanOptions) (plan.ExecutionPlan, error) {
	if connection.Spec.ConfigVersion != 1 {
		return plan.ExecutionPlan{}, fmt.Errorf("unsupported SSH configVersion %d", connection.Spec.ConfigVersion)
	}
	config, err := DecodeConfig(connection.Spec.Config)
	if err != nil {
		return plan.ExecutionPlan{}, err
	}
	if err := Validate(config); err != nil {
		return plan.ExecutionPlan{}, err
	}
	if options.NonInteractive || connection.Spec.Lifecycle == "persistent" {
		for _, option := range config.SSHOptions {
			key, value, _ := strings.Cut(option, "=")
			if strings.EqualFold(key, "BatchMode") && !strings.EqualFold(strings.TrimSpace(value), "yes") {
				return plan.ExecutionPlan{}, errors.New("BatchMode cannot be disabled for non-interactive or persistent connections")
			}
		}
	}

	binary := os.Getenv("TUNLITE_SSH")
	if binary == "" {
		binary = "ssh"
	}
	program, err := d.lookup(binary)
	if err != nil {
		return plan.ExecutionPlan{}, fmt.Errorf("ssh not found (%s)", binary)
	}

	args := []string{
		"-N", "-T",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if options.NonInteractive || connection.Spec.Lifecycle == "persistent" {
		args = append(args, "-o", "BatchMode=yes")
	}
	if config.IdentityFile != "" {
		args = append(args, "-i", expandHome(config.IdentityFile), "-o", "IdentitiesOnly=yes")
	}
	if config.Port != 0 && config.Port != 22 {
		args = append(args, "-p", strconv.Itoa(config.Port))
	}
	if len(config.Jump) > 0 {
		args = append(args, "-J", strings.Join(config.Jump, ","))
	}

	endpoints := make([]plan.Endpoint, 0, len(config.Forwards))
	healthChecks := make([]plan.HealthCheck, 0, len(config.Forwards))
	for i, forward := range config.Forwards {
		flag, spec := forwardArg(forward)
		args = append(args, flag, spec)
		direction := "access"
		if forward.Type == "remote" {
			direction = "publish"
		}
		address := net.JoinHostPort(defaultBind(forward.Bind), strconv.Itoa(forward.SrcPort))
		endpoints = append(endpoints, plan.Endpoint{
			Name: fmt.Sprintf("forward-%d", i+1), Direction: direction, Protocol: "tcp", Address: address,
		})
		if forward.Type != "remote" {
			healthChecks = append(healthChecks, plan.HealthCheck{Type: "tcp", Address: address, TimeoutMS: 1500})
		}
	}
	for _, option := range config.SSHOptions {
		args = append(args, "-o", option)
	}
	args = append(args, "--", config.Host)

	restart := "on-failure"
	if connection.Spec.Lifecycle == "foreground" {
		restart = "never"
	}
	return plan.ExecutionPlan{
		Mode:      "direct",
		Exec:      plan.Exec{Program: program, Args: args, Environment: map[string]string{}},
		Lifecycle: plan.Lifecycle{Restart: restart, RestartDelaySeconds: 10, StopTimeoutSeconds: 5},
		Endpoints: endpoints, HealthChecks: healthChecks, Redactions: []plan.Redaction{},
	}, nil
}

func DecodeConfig(raw json.RawMessage) (Config, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode SSH config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("more than one JSON value")
		}
		return Config{}, fmt.Errorf("decode SSH config: %w", err)
	}
	return config, nil
}

func Validate(config Config) error {
	if config.Host == "" {
		return errors.New("SSH host is required")
	}
	if strings.HasPrefix(config.Host, "-") || strings.ContainsAny(config.Host, "\r\n\x00") {
		return fmt.Errorf("invalid SSH host %q", config.Host)
	}
	if config.Port == 0 {
		config.Port = 22
	}
	if !validPort(config.Port) {
		return fmt.Errorf("invalid SSH port %d", config.Port)
	}
	if len(config.Forwards) == 0 {
		return errors.New("at least one SSH forward is required")
	}
	if strings.ContainsAny(config.IdentityFile, "\r\n\x00") {
		return errors.New("identity file contains an invalid control character")
	}
	claimed := make(map[string]struct{}, len(config.Forwards))
	for _, forward := range config.Forwards {
		if err := validateForward(forward); err != nil {
			return err
		}
		scope := "L"
		if forward.Type == "remote" {
			scope = "R"
		}
		key := fmt.Sprintf("%s:%s:%d", scope, defaultBind(forward.Bind), forward.SrcPort)
		if _, exists := claimed[key]; exists {
			return fmt.Errorf("duplicate SSH forward endpoint %s", key)
		}
		claimed[key] = struct{}{}
	}
	for _, option := range config.SSHOptions {
		if err := validateSSHOption(option); err != nil {
			return err
		}
	}
	for _, jump := range config.Jump {
		if jump == "" || strings.HasPrefix(jump, "-") || strings.ContainsAny(jump, "\r\n\x00") {
			return fmt.Errorf("invalid SSH jump host %q", jump)
		}
		if _, _, err := ParseTarget(jump); err != nil {
			return fmt.Errorf("invalid SSH jump host %q: %w", jump, err)
		}
	}
	return nil
}

func validateSSHOption(option string) error {
	if option == "" || strings.HasPrefix(option, "-") || strings.ContainsAny(option, "\r\n\x00") {
		return fmt.Errorf("invalid SSH option %q", option)
	}
	key, _, found := strings.Cut(option, "=")
	if !found || key == "" || strings.ContainsAny(key, " \t") {
		return fmt.Errorf("SSH option must use Key=Value form, got %q", option)
	}
	return nil
}

func ParseTarget(value string) (host string, port int, err error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\r\n\x00") {
		return "", 0, fmt.Errorf("invalid SSH target %q", value)
	}
	port = 22
	userPrefix := ""
	endpoint := value
	if at := strings.LastIndex(value, "@"); at >= 0 {
		userPrefix, endpoint = value[:at+1], value[at+1:]
	}
	if strings.HasPrefix(endpoint, "[") {
		if close := strings.LastIndex(endpoint, "]:"); close >= 0 {
			parsedHost, parsedPort, splitErr := net.SplitHostPort(endpoint)
			if splitErr != nil {
				return "", 0, fmt.Errorf("invalid SSH target %q", value)
			}
			p, parseErr := parsePort(parsedPort, value)
			if parseErr != nil {
				return "", 0, parseErr
			}
			return userPrefix + parsedHost, p, nil
		}
	}
	if strings.Count(endpoint, ":") == 1 {
		parts := strings.SplitN(endpoint, ":", 2)
		p, parseErr := parsePort(parts[1], value)
		if parseErr != nil {
			return "", 0, parseErr
		}
		return userPrefix + parts[0], p, nil
	}
	return value, port, nil
}

func ParseJump(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return []string{}, nil
	}
	hops := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, _, err := ParseTarget(item); err != nil {
			return nil, fmt.Errorf("invalid jump host: %w", err)
		}
		hops = append(hops, item)
	}
	return hops, nil
}

func ParseForward(flag, spec string) (Forward, error) {
	if spec == "" {
		return Forward{}, fmt.Errorf("%s needs a forward specification", flag)
	}
	if strings.Contains(spec, "/") {
		return Forward{}, fmt.Errorf("unix-domain-socket forwards are not supported: %q", spec)
	}
	fields, err := splitForwardSpec(spec)
	if err != nil {
		return Forward{}, err
	}
	if flag == "-D" {
		if len(fields) == 1 {
			port, err := parsePort(fields[0], spec)
			return Forward{Type: "dynamic", Bind: "127.0.0.1", SrcPort: port}, err
		}
		if len(fields) == 2 {
			bind, err := normalizeBind(fields[0], spec)
			if err != nil {
				return Forward{}, err
			}
			port, err := parsePort(fields[1], spec)
			return Forward{Type: "dynamic", Bind: bind, SrcPort: port}, err
		}
		return Forward{}, fmt.Errorf("-D wants [bind:]port, got %q", spec)
	}
	if flag != "-L" && flag != "-R" {
		return Forward{}, fmt.Errorf("unknown forward flag %q", flag)
	}
	if len(fields) != 3 && len(fields) != 4 {
		return Forward{}, fmt.Errorf("%s wants [bind:]port:host:hostport, got %q", flag, spec)
	}
	bind := "127.0.0.1"
	offset := 0
	if len(fields) == 4 {
		bind, err = normalizeBind(fields[0], spec)
		if err != nil {
			return Forward{}, err
		}
		offset = 1
	}
	srcPort, err := parsePort(fields[offset], spec)
	if err != nil {
		return Forward{}, err
	}
	destHost := unbracket(fields[offset+1])
	if destHost == "" {
		return Forward{}, fmt.Errorf("empty target host in %q", spec)
	}
	destPort, err := parsePort(fields[offset+2], spec)
	if err != nil {
		return Forward{}, err
	}
	forwardType := "local"
	if flag == "-R" {
		forwardType = "remote"
	}
	return Forward{Type: forwardType, Bind: bind, SrcPort: srcPort, DestHost: destHost, DestPort: destPort}, nil
}

func validateForward(forward Forward) error {
	if forward.Type != "local" && forward.Type != "remote" && forward.Type != "dynamic" {
		return fmt.Errorf("invalid forward type %q", forward.Type)
	}
	if !validPort(forward.SrcPort) {
		return fmt.Errorf("invalid source port %d", forward.SrcPort)
	}
	if strings.ContainsAny(forward.Bind, "\r\n\x00") || strings.ContainsAny(forward.DestHost, "\r\n\x00") {
		return errors.New("SSH forward contains an invalid control character")
	}
	if forward.Type != "dynamic" && (forward.DestHost == "" || !validPort(forward.DestPort)) {
		return fmt.Errorf("forward %s requires a target host and port", forward.Type)
	}
	return nil
}

func forwardArg(forward Forward) (string, string) {
	bind := bracket(defaultBind(forward.Bind))
	if forward.Type == "dynamic" {
		return "-D", fmt.Sprintf("%s:%d", bind, forward.SrcPort)
	}
	flag := "-L"
	if forward.Type == "remote" {
		flag = "-R"
	}
	return flag, fmt.Sprintf("%s:%d:%s:%d", bind, forward.SrcPort, bracket(forward.DestHost), forward.DestPort)
}

func splitForwardSpec(spec string) ([]string, error) {
	var fields []string
	var current strings.Builder
	inBracket := false
	for _, char := range spec {
		switch char {
		case '[':
			if inBracket {
				return nil, fmt.Errorf("invalid bracketed address in %q", spec)
			}
			inBracket = true
			current.WriteRune(char)
		case ']':
			if !inBracket {
				return nil, fmt.Errorf("invalid bracketed address in %q", spec)
			}
			inBracket = false
			current.WriteRune(char)
		case ':':
			if inBracket {
				current.WriteRune(char)
			} else {
				fields = append(fields, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}
	if inBracket {
		return nil, fmt.Errorf("unclosed bracket in %q", spec)
	}
	return append(fields, current.String()), nil
}

func normalizeBind(value, spec string) (string, error) {
	if value == "*" {
		return "0.0.0.0", nil
	}
	value = unbracket(value)
	if value == "" {
		return "", fmt.Errorf("empty bind in %q", spec)
	}
	return value, nil
}

func parsePort(value, source string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || !validPort(port) {
		return 0, fmt.Errorf("invalid port %q in %q", value, source)
	}
	return port, nil
}

func validPort(port int) bool { return port >= 1 && port <= 65535 }

func defaultBind(bind string) string {
	if bind == "" {
		return "127.0.0.1"
	}
	return bind
}

func bracket(host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}

func unbracket(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

func expandHome(value string) string {
	if value == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, value[2:])
		}
	}
	return value
}
