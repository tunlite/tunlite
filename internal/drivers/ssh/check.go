package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tunlite/tunlite/internal/core"
)

const (
	maxProbeOutputBytes = 64 * 1024
	probeMatchTailBytes = 256
	probeWaitDelay      = 500 * time.Millisecond
)

var (
	authSuccessPattern  = regexp.MustCompile(`(?i)\bAuthenticated to |\bAuthentication succeeded\b`)
	authFailurePattern  = regexp.MustCompile(`(?i)permission denied|too many authentication failures`)
	dnsFailurePattern   = regexp.MustCompile(`(?i)could not resolve hostname|name or service not known`)
	reachFailurePattern = regexp.MustCompile(`(?i)connection refused|connection timed out|operation timed out|no route to host`)
	hostKeyPattern      = regexp.MustCompile(`(?i)host key verification failed|no .* host key is known`)
)

func (d *Driver) Check(ctx context.Context, connection core.Connection, options core.CheckOptions) (core.CheckReport, error) {
	observedAt := time.Now().UTC()
	config, err := DecodeConfig(connection.Spec.Config)
	if err != nil {
		return core.CheckReport{}, err
	}
	validation := config
	if len(validation.Forwards) == 0 {
		validation.Forwards = []Forward{{Type: "dynamic", Bind: "127.0.0.1", SrcPort: 1}}
	}
	if err := Validate(validation); err != nil {
		return core.CheckReport{}, err
	}
	binary := os.Getenv("TUNLITE_SSH")
	if binary == "" {
		binary = "ssh"
	}
	program, err := d.lookup(binary)
	if err != nil {
		return checkReport(observedAt, connection, false, "provider_missing", "error",
			fmt.Sprintf("OpenSSH client not found (%s)", binary),
			[]core.Action{{Type: "install_provider", Parameters: map[string]any{"provider": "openssh"}}}), nil
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	seconds := int(timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	args := buildCheckArgs(config, seconds)

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	authenticated := make(chan struct{}, 1)
	probeOutput := newProbeOutputObserver(authenticated)
	command := exec.CommandContext(probeCtx, program, args...)
	command.Stdout = io.Discard
	command.Stderr = probeOutput
	command.WaitDelay = probeWaitDelay
	if err := command.Start(); err != nil {
		return core.CheckReport{}, err
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	var runErr error
	confirmed := false
	select {
	case runErr = <-waited:
	case <-authenticated:
		confirmed = true
		select {
		case runErr = <-waited:
		case <-time.After(500 * time.Millisecond):
			cancel()
			runErr = <-waited
		}
	case <-probeCtx.Done():
		runErr = <-waited
	}
	output := probeOutput.String()
	if confirmed || authSuccessPattern.MatchString(output) || runErr == nil ||
		(errors.Is(runErr, exec.ErrWaitDelay) && probeCtx.Err() == nil) {
		return checkReport(observedAt, connection, true, "ssh_authentication_succeeded", "info",
			fmt.Sprintf("SSH authentication to %s succeeded", config.Host), nil), nil
	}
	code, message, actions := classifyCheckFailure(config.Host, output, probeCtx.Err())
	return checkReport(observedAt, connection, false, code, "error", message, actions), nil
}

func buildCheckArgs(config Config, seconds int) []string {
	args := []string{
		"-F", "none",
		"-T", "-v",
		"-o", "BatchMode=yes",
		"-o", "NumberOfPasswordPrompts=0",
		"-o", "ConnectionAttempts=1",
		"-o", "ConnectTimeout=" + strconv.Itoa(seconds),
		"-o", "StrictHostKeyChecking=yes",
		"-o", "PermitLocalCommand=no",
		"-o", "ClearAllForwardings=yes",
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
	for _, option := range config.SSHOptions {
		args = append(args, "-o", option)
	}
	args = append(args, "--", config.Host, "true")
	return args
}

type probeOutputObserver struct {
	mu            sync.Mutex
	output        []byte
	matchTail     []byte
	authenticated chan<- struct{}
	authSignaled  bool
}

func newProbeOutputObserver(authenticated chan<- struct{}) *probeOutputObserver {
	return &probeOutputObserver{authenticated: authenticated}
}

func (observer *probeOutputObserver) Write(value []byte) (int, error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()

	if remaining := maxProbeOutputBytes - len(observer.output); remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		observer.output = append(observer.output, value[:remaining]...)
	}
	for unread := value; len(unread) > 0; {
		chunkSize := probeMatchTailBytes
		if chunkSize > len(unread) {
			chunkSize = len(unread)
		}
		candidate := make([]byte, 0, len(observer.matchTail)+chunkSize)
		candidate = append(candidate, observer.matchTail...)
		candidate = append(candidate, unread[:chunkSize]...)
		if !observer.authSignaled && authSuccessPattern.Match(candidate) {
			observer.authSignaled = true
			select {
			case observer.authenticated <- struct{}{}:
			default:
			}
		}
		if len(candidate) > probeMatchTailBytes {
			candidate = candidate[len(candidate)-probeMatchTailBytes:]
		}
		observer.matchTail = append(observer.matchTail[:0], candidate...)
		unread = unread[chunkSize:]
	}
	return len(value), nil
}

func (observer *probeOutputObserver) String() string {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return string(observer.output)
}

func classifyCheckFailure(host, output string, contextErr error) (string, string, []core.Action) {
	switch {
	case authFailurePattern.MatchString(output):
		return "ssh_authentication_failed", fmt.Sprintf("SSH authentication to %s failed", host),
			[]core.Action{{Type: "setup_ssh_key", Parameters: map[string]any{"host": host, "interactive": true}}}
	case hostKeyPattern.MatchString(output):
		return "ssh_host_key_untrusted", fmt.Sprintf("SSH host key for %s is not trusted", host),
			[]core.Action{{Type: "review_ssh_host_key", Parameters: map[string]any{"host": host}}}
	case dnsFailurePattern.MatchString(output):
		return "ssh_dns_failed", fmt.Sprintf("SSH host %s could not be resolved", host), []core.Action{}
	case reachFailurePattern.MatchString(output):
		return "ssh_unreachable", fmt.Sprintf("SSH host %s is unreachable", host), []core.Action{}
	case contextErr != nil:
		return "ssh_check_timeout", fmt.Sprintf("SSH check for %s timed out", host), []core.Action{}
	default:
		return "ssh_check_failed", fmt.Sprintf("SSH check for %s failed", host), []core.Action{}
	}
}

func checkReport(observedAt time.Time, connection core.Connection, healthy bool, code, severity, message string, actions []core.Action) core.CheckReport {
	if actions == nil {
		actions = []core.Action{}
	}
	return core.CheckReport{
		Healthy: healthy, ObservedAt: observedAt,
		Conditions: []core.Condition{{
			Code: code, Severity: severity, Message: message, Driver: ID,
			Connection: connection.Metadata.Name, Actions: actions,
		}},
	}
}
