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
	"unicode"

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
	config, err := DecodeConfig(connection.Spec.Config)
	if err != nil {
		return plan.ExecutionPlan{}, err
	}
	if err := Validate(config); err != nil {
		return plan.ExecutionPlan{}, err
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
		// Managed Connections must not inherit commands, forwards, multiplexing,
		// or other hidden behavior from user/system SSH config. Explicit safe
		// fields below are the complete runtime contract.
		"-F", "none",
		"-N", "-T",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if options.NonInteractive {
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

	return plan.ExecutionPlan{
		Mode:      "direct",
		Exec:      plan.Exec{Program: program, Args: args, Environment: map[string]string{}},
		Lifecycle: plan.Lifecycle{Restart: "always", RestartDelaySeconds: 10, StopTimeoutSeconds: 5},
		Endpoints: endpoints, HealthChecks: healthChecks, Redactions: []plan.Redaction{},
	}, nil
}

// ConfigFromPlan reverses a direct OpenSSH plan that uses Tunlite's supported
// argv subset. It is used when a native service is explicitly brought under
// management; unknown flags are rejected instead of guessed.
func ConfigFromPlan(value plan.ExecutionPlan) (Config, error) {
	if value.Mode != plan.ModeDirect || filepath.Base(value.Exec.Program) != "ssh" {
		return Config{}, errors.New("execution plan is not a direct OpenSSH command")
	}
	config := Config{}
	isolatedConfig := false
	args := value.Exec.Args
	for index := 0; index < len(args); index++ {
		argument := args[index]
		next := func() (string, error) {
			if index+1 >= len(args) {
				return "", fmt.Errorf("SSH option %s has no value", argument)
			}
			index++
			return args[index], nil
		}
		switch argument {
		case "-N", "-T":
			continue
		case "-F":
			if isolatedConfig {
				return Config{}, errors.New("OpenSSH plan specifies -F more than once")
			}
			configFile, err := next()
			if err != nil {
				return Config{}, err
			}
			if configFile != "none" {
				return Config{}, fmt.Errorf("unsupported OpenSSH config file %q", configFile)
			}
			isolatedConfig = true
		case "-o":
			option, err := next()
			if err != nil {
				return Config{}, err
			}
			if isManagedSSHOption(option) {
				continue
			}
			config.SSHOptions = append(config.SSHOptions, option)
		case "-i":
			identity, err := next()
			if err != nil {
				return Config{}, err
			}
			config.IdentityFile = identity
		case "-p":
			portText, err := next()
			if err != nil {
				return Config{}, err
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				return Config{}, fmt.Errorf("invalid SSH port %q", portText)
			}
			config.Port = port
		case "-J":
			jump, err := next()
			if err != nil {
				return Config{}, err
			}
			if jump == "" {
				return Config{}, errors.New("SSH option -J has an empty jump host")
			}
			config.Jump, err = ParseJump(jump)
			if err != nil {
				return Config{}, err
			}
		case "-L", "-R", "-D":
			specification, err := next()
			if err != nil {
				return Config{}, err
			}
			forward, err := ParseForward(argument, specification)
			if err != nil {
				return Config{}, err
			}
			config.Forwards = append(config.Forwards, forward)
		case "--":
			if index+1 != len(args)-1 {
				return Config{}, errors.New("OpenSSH plan must have exactly one target after --")
			}
			config.Host = args[index+1]
			index = len(args)
		default:
			return Config{}, fmt.Errorf("unsupported OpenSSH argument %q", argument)
		}
	}
	if !isolatedConfig {
		return Config{}, errors.New("OpenSSH plan must disable ambient config with -F none")
	}
	if err := Validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func isManagedSSHOption(option string) bool {
	key, value, _ := strings.Cut(option, "=")
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.ToLower(strings.TrimSpace(value))
	switch key {
	case "serveraliveinterval":
		return value == "15"
	case "serveralivecountmax":
		return value == "3"
	case "exitonforwardfailure":
		return value == "yes"
	case "connecttimeout":
		return value == "10"
	case "stricthostkeychecking":
		return value == "accept-new"
	case "batchmode", "identitiesonly":
		return value == "yes"
	default:
		return false
	}
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
	if err := validateCanonicalTarget(config.Host); err != nil {
		return err
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
	seenOptions := make(map[string]struct{}, len(config.SSHOptions))
	for _, option := range config.SSHOptions {
		if err := validateSSHOption(option); err != nil {
			return err
		}
		key, _, _ := strings.Cut(option, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if _, exists := seenOptions[key]; exists {
			return fmt.Errorf("duplicate SSH option %q", key)
		}
		seenOptions[key] = struct{}{}
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
	key, value, found := strings.Cut(option, "=")
	key = strings.TrimSpace(key)
	if !found || key == "" || strings.ContainsAny(key, " \t") {
		return fmt.Errorf("SSH option must use Key=Value form, got %q", option)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("SSH option %q requires a non-empty value", key)
	}
	if _, allowed := allowedSSHOptions[strings.ToLower(key)]; !allowed {
		return fmt.Errorf("SSH option %q is not allowed; use a structured Tunlite field or a supported safe option", key)
	}
	return nil
}

// allowedSSHOptions is deliberately conservative. Every option added here
// must be safe to serialize as one argv value and must not execute a helper,
// create a forward, load another config, or override Tunlite's lifecycle and
// reliability baseline. OpenSSH options not listed here are rejected instead
// of being guessed safe.
var allowedSSHOptions = map[string]struct{}{
	"addressfamily":                {},
	"bindaddress":                  {},
	"bindinterface":                {},
	"casignaturealgorithms":        {},
	"certificatefile":              {},
	"ciphers":                      {},
	"compression":                  {},
	"fingerprinthash":              {},
	"gssapiauthentication":         {},
	"gssapidelegatecredentials":    {},
	"hashknownhosts":               {},
	"hostbasedauthentication":      {},
	"hostkeyalgorithms":            {},
	"ipqos":                        {},
	"kbdinteractiveauthentication": {},
	"kexalgorithms":                {},
	"loglevel":                     {},
	"logverbose":                   {},
	"macs":                         {},
	"numberofpasswordprompts":      {},
	"passwordauthentication":       {},
	"preferredauthentications":     {},
	"pubkeyacceptedalgorithms":     {},
	"pubkeyauthentication":         {},
	"rekeylimit":                   {},
	"requiredrsasize":              {},
	"sendenv":                      {},
	"setenv":                       {},
	"updatehostkeys":               {},
	"verifyhostkeydns":             {},
}

func ParseTarget(value string) (host string, port int, err error) {
	target, err := ParseTargetSpec(value)
	if err != nil {
		return "", 0, err
	}
	return target.Host, target.Port, nil
}

type TargetSpec struct {
	Host          string
	Port          int
	PortSpecified bool
	IPv6          bool
	user          string
	endpoint      string
}

func ParseTargetSpec(value string) (TargetSpec, error) {
	if value == "" || value != strings.TrimSpace(value) || containsTargetControlOrSpace(value) {
		return TargetSpec{}, fmt.Errorf("invalid SSH target %q", value)
	}
	result := TargetSpec{Port: 22}
	userPrefix := ""
	endpoint := value
	if strings.Count(value, "@") > 1 {
		return TargetSpec{}, fmt.Errorf("invalid SSH target %q", value)
	}
	if at := strings.IndexByte(value, '@'); at >= 0 {
		user := value[:at]
		if !validSSHUser(user) {
			return TargetSpec{}, fmt.Errorf("invalid SSH user in target %q", value)
		}
		result.user = user
		userPrefix, endpoint = user+"@", value[at+1:]
	}
	if endpoint == "" || strings.HasPrefix(endpoint, "-") {
		return TargetSpec{}, fmt.Errorf("invalid SSH target %q", value)
	}
	if strings.HasPrefix(endpoint, "[") {
		close := strings.IndexByte(endpoint, ']')
		if close < 0 {
			return TargetSpec{}, fmt.Errorf("invalid SSH target %q", value)
		}
		literal := endpoint[1:close]
		if ip := net.ParseIP(literal); ip == nil || !strings.Contains(literal, ":") {
			return TargetSpec{}, fmt.Errorf("invalid bracketed IPv6 target %q", value)
		}
		result.Host, result.endpoint, result.IPv6 = userPrefix+literal, literal, true
		suffix := endpoint[close+1:]
		if suffix == "" {
			return result, nil
		}
		if !strings.HasPrefix(suffix, ":") || len(suffix) == 1 {
			return TargetSpec{}, fmt.Errorf("invalid SSH target %q", value)
		}
		p, parseErr := parsePort(suffix[1:], value)
		if parseErr != nil {
			return TargetSpec{}, parseErr
		}
		result.Port, result.PortSpecified = p, true
		return result, nil
	}
	if strings.ContainsAny(endpoint, "[]") || strings.Count(endpoint, ":") > 1 {
		return TargetSpec{}, fmt.Errorf("IPv6 SSH targets must use brackets: %q", value)
	}
	if strings.Count(endpoint, ":") == 1 {
		parts := strings.SplitN(endpoint, ":", 2)
		if !validSSHHostname(parts[0]) {
			return TargetSpec{}, fmt.Errorf("invalid SSH host in target %q", value)
		}
		p, parseErr := parsePort(parts[1], value)
		if parseErr != nil {
			return TargetSpec{}, parseErr
		}
		result.Host, result.endpoint = userPrefix+parts[0], parts[0]
		result.Port, result.PortSpecified = p, true
		return result, nil
	}
	if !validSSHHostname(endpoint) {
		return TargetSpec{}, fmt.Errorf("invalid SSH host in target %q", value)
	}
	result.Host, result.endpoint = value, endpoint
	return result, nil
}

func validateCanonicalTarget(value string) error {
	if value == "" || value != strings.TrimSpace(value) || containsTargetControlOrSpace(value) {
		return fmt.Errorf("invalid SSH target %q", value)
	}
	endpoint := value
	if strings.Count(value, "@") > 1 {
		return fmt.Errorf("invalid SSH target %q", value)
	}
	if at := strings.IndexByte(value, '@'); at >= 0 {
		if !validSSHUser(value[:at]) {
			return fmt.Errorf("invalid SSH user in target %q", value)
		}
		endpoint = value[at+1:]
	}
	if endpoint == "" || strings.HasPrefix(endpoint, "-") || strings.ContainsAny(endpoint, "[]") {
		return fmt.Errorf("invalid SSH target %q", value)
	}
	if strings.Contains(endpoint, ":") {
		if ip := net.ParseIP(endpoint); ip == nil {
			return fmt.Errorf("invalid canonical IPv6 target %q", value)
		}
		return nil
	}
	if !validSSHHostname(endpoint) {
		return fmt.Errorf("invalid SSH host in target %q", value)
	}
	return nil
}

func containsTargetControlOrSpace(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			return true
		}
	}
	return false
}

func validSSHUser(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, char := range value {
		if !isASCIIAlphaNumeric(char) && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validSSHHostname(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, "-") {
		return false
	}
	if ip := net.ParseIP(value); ip != nil {
		return !strings.Contains(value, ":")
	}
	if onlyDigitsAndDots := strings.Trim(value, "0123456789.") == ""; onlyDigitsAndDots {
		return false
	}
	trimmed := strings.TrimSuffix(value, ".")
	if trimmed == "" {
		return false
	}
	for _, label := range strings.Split(trimmed, ".") {
		if label == "" || len(label) > 63 || !isASCIIAlphaNumeric(rune(label[0])) || !isASCIIAlphaNumeric(rune(label[len(label)-1])) {
			return false
		}
		for _, char := range label {
			if !isASCIIAlphaNumeric(char) && char != '-' {
				return false
			}
		}
	}
	return true
}

func isASCIIAlphaNumeric(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}

func ParseJump(value string) ([]string, error) {
	if value == "" {
		return []string{}, nil
	}
	if value != strings.TrimSpace(value) || containsTargetControlOrSpace(value) {
		return nil, fmt.Errorf("invalid jump host %q", value)
	}
	hops := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item == "" {
			return nil, fmt.Errorf("invalid empty jump host in %q", value)
		}
		target, err := ParseTargetSpec(item)
		if err != nil {
			return nil, fmt.Errorf("invalid jump host: %w", err)
		}
		hops = append(hops, formatJumpTarget(target))
	}
	return hops, nil
}

func formatJumpTarget(target TargetSpec) string {
	endpoint := target.endpoint
	if target.IPv6 {
		endpoint = "[" + endpoint + "]"
	}
	if target.user != "" {
		endpoint = target.user + "@" + endpoint
	}
	if target.PortSpecified {
		endpoint += ":" + strconv.Itoa(target.Port)
	}
	return endpoint
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
