package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tunlite/tunlite/internal/plan"
)

const (
	launchctlProgram = "/bin/launchctl"
	mktempProgram    = "/usr/bin/mktemp"
	ddProgram        = "/bin/dd"
	installProgram   = "/usr/bin/install"
	removeProgram    = "/bin/rm"
	syncProgram      = "/bin/sync"
	psProgram        = "/bin/ps"
	killProgram      = "/bin/kill"
	trueProgram      = "/usr/bin/true"
)

type Launchd struct {
	agentDir              string
	daemonDir             string
	logDir                string
	uid                   int
	userName              string
	userDomainAvailable   bool
	systemDomainAvailable bool
	transitionTimeout     time.Duration
	transitionStable      time.Duration
	transitionPoll        time.Duration
	runner                CommandRunner
}

func NewLaunchd(agentDir, logDir string, uid int, runner CommandRunner) *Launchd {
	return NewLaunchdWithSystemScope(agentDir, "", logDir, uid, "", runner)
}

// NewLaunchdWithSystemScope exists so the platform adapter can be exercised
// wholly inside a temporary filesystem. Production construction always uses
// /Library/LaunchDaemons; tests must pass a temp directory and a fake runner.
func NewLaunchdWithSystemScope(agentDir, daemonDir, logDir string, uid int, userName string, runner CommandRunner) *Launchd {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Launchd{
		agentDir: agentDir, daemonDir: daemonDir, logDir: logDir, uid: uid, userName: userName,
		userDomainAvailable: true, systemDomainAvailable: daemonDir != "",
		// SSH uses ConnectTimeout=10. Readiness must leave enough time after
		// that connection budget for the full continuous stability window.
		transitionTimeout: 30 * time.Second, transitionStable: 11 * time.Second, transitionPoll: 100 * time.Millisecond,
		runner: runner,
	}
}

func (l *Launchd) Name() string { return "launchd" }

func (l *Launchd) Capabilities() Capabilities {
	return Capabilities{
		Registration: true, Background: true, Persistence: true, Logs: true, Discovery: true,
		UserScope:   l.userDomainAvailable,
		SystemScope: l.systemDomainAvailable && l.daemonDir != "" && l.userName != "" && l.userName != "root" && l.uid > 0,
	}
}

func (l *Launchd) setDomainAvailability(userAvailable, systemAvailable bool) {
	l.userDomainAvailable = userAvailable
	l.systemDomainAvailable = systemAvailable
}

func (l *Launchd) ensureScopeAvailable(scope Scope) error {
	scope = NormalizeScope(scope)
	if !SupportsScope(l, scope) {
		return &UnsupportedScopeError{Adapter: l.Name(), Scope: scope}
	}
	return nil
}

func (l *Launchd) Target(connection string) Artifact {
	return l.rawTarget(ScopeUser, connection)
}

func (l *Launchd) TargetScoped(scope Scope, connection string) (Artifact, error) {
	scope = NormalizeScope(scope)
	if !SupportsScope(l, scope) {
		return Artifact{}, &UnsupportedScopeError{Adapter: l.Name(), Scope: scope}
	}
	return l.rawTarget(scope, connection), nil
}

func (l *Launchd) rawTarget(scope Scope, connection string) Artifact {
	scope = NormalizeScope(scope)
	label := "dev.tunlite.connection." + connection
	directory := l.agentDir
	if scope == ScopeSystem {
		directory = l.daemonDir
	}
	return Artifact{
		Connection: connection,
		Adapter:    l.Name(),
		Scope:      scope,
		UnitName:   label,
		Path:       filepath.Join(directory, label+".plist"),
		LogPaths: []string{
			filepath.Join(l.logDir, connection+".stdout.log"),
			filepath.Join(l.logDir, connection+".stderr.log"),
		},
	}
}

func (l *Launchd) Build(connection string, executionPlan plan.ExecutionPlan) (Artifact, error) {
	return l.BuildScoped(ScopeUser, connection, executionPlan)
}

func (l *Launchd) BuildScoped(scope Scope, connection string, executionPlan plan.ExecutionPlan) (Artifact, error) {
	artifact, err := l.TargetScoped(scope, connection)
	if err != nil {
		return Artifact{}, err
	}
	return l.build(artifact, executionPlan)
}

func (l *Launchd) BuildBound(reference Reference, executionPlan plan.ExecutionPlan) (Artifact, error) {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return Artifact{}, err
	}
	return l.build(l.resolve(reference), executionPlan)
}

func (l *Launchd) build(artifact Artifact, executionPlan plan.ExecutionPlan) (Artifact, error) {
	if err := plan.Validate(executionPlan); err != nil {
		return Artifact{}, err
	}
	if executionPlan.Lifecycle.Restart == "on-failure" {
		return Artifact{}, errors.New("launchd cannot guarantee on-failure recovery for nondemand jobs; use always keep-up semantics")
	}
	if err := validateLaunchdDemandIdentity(artifact.Connection, artifact.Scope); err != nil {
		return Artifact{}, err
	}
	digest, err := PlanDigest(executionPlan)
	if err != nil {
		return Artifact{}, err
	}
	artifact.Plan = executionPlan
	artifact.PlanDigest = digest
	if artifact.Scope == ScopeSystem {
		if err := l.validateSystemArtifact(artifact); err != nil {
			return Artifact{}, err
		}
		if l.userName == "" || l.userName == "root" || l.uid <= 0 {
			return Artifact{}, &UnsupportedScopeError{Adapter: l.Name(), Scope: ScopeSystem}
		}
		if _, exists := executionPlan.Exec.Environment["SSH_AUTH_SOCK"]; exists {
			return Artifact{}, errors.New("system launchd services cannot inherit SSH_AUTH_SOCK; configure a non-interactive identity file")
		}
	}
	if len(artifact.LogPaths) != 2 || strings.TrimSpace(artifact.LogPaths[0]) == "" || strings.TrimSpace(artifact.LogPaths[1]) == "" {
		return Artifact{}, errors.New("launchd service requires exactly one non-empty stdout path and one non-empty stderr path")
	}
	if err := validateLaunchdText(artifact, executionPlan); err != nil {
		return Artifact{}, err
	}
	embedded, err := encodeEmbeddedPlan(executionPlan)
	if err != nil {
		return Artifact{}, err
	}
	artifact.Content, err = renderLaunchd(artifact, embedded, executionPlan, l.userName, l.logDir)
	return artifact, err
}

func validateLaunchdText(artifact Artifact, executionPlan plan.ExecutionPlan) error {
	values := []string{artifact.UnitName, artifact.Path, executionPlan.Exec.Program}
	values = append(values, artifact.LogPaths...)
	values = append(values, executionPlan.Exec.Args...)
	if executionPlan.Exec.WorkingDirectory != nil {
		values = append(values, *executionPlan.Exec.WorkingDirectory)
	}
	for key, value := range executionPlan.Exec.Environment {
		values = append(values, key, value)
	}
	for _, value := range values {
		if !utf8.ValidString(value) {
			return errors.New("launchd plan contains invalid UTF-8")
		}
		for _, char := range value {
			if char != '\t' && char != '\n' && char != '\r' && (char < 0x20 || char == 0xfffe || char == 0xffff) {
				return fmt.Errorf("launchd plan contains an XML-incompatible control character U+%04X", char)
			}
		}
	}
	return nil
}

func renderLaunchd(artifact Artifact, embedded string, executionPlan plan.ExecutionPlan, userName, logDir string) ([]byte, error) {
	var output strings.Builder
	fmt.Fprintln(&output, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprintln(&output, `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`)
	fmt.Fprintf(&output, "<!-- Generated by Tunlite. DO NOT EDIT. Plan-SHA256: %s -->\n", artifact.PlanDigest)
	fmt.Fprintf(&output, "<!-- %s%s -->\n", embeddedPlanPrefix, embedded)
	fmt.Fprintln(&output, `<plist version="1.0">`)
	fmt.Fprintln(&output, `<dict>`)
	plistKeyString(&output, "Label", artifact.UnitName)
	if artifact.Scope == ScopeSystem {
		plistKeyString(&output, "UserName", userName)
	}
	// Keep the executable explicit. On current macOS, a TLE-managed job whose
	// executable is only inferred from ProgramArguments can remain in xpcproxy
	// with execs=0 indefinitely.
	plistKeyString(&output, "Program", executionPlan.Exec.Program)
	fmt.Fprintln(&output, "  <key>ProgramArguments</key>")
	fmt.Fprintln(&output, "  <array>")
	for _, argument := range append([]string{executionPlan.Exec.Program}, executionPlan.Exec.Args...) {
		fmt.Fprintf(&output, "    <string>%s</string>\n", xmlText(argument))
	}
	fmt.Fprintln(&output, "  </array>")
	if executionPlan.Exec.WorkingDirectory != nil {
		plistKeyString(&output, "WorkingDirectory", *executionPlan.Exec.WorkingDirectory)
	}
	if len(executionPlan.Exec.Environment) > 0 {
		fmt.Fprintln(&output, "  <key>EnvironmentVariables</key>")
		fmt.Fprintln(&output, "  <dict>")
		keys := make([]string, 0, len(executionPlan.Exec.Environment))
		for key := range executionPlan.Exec.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&output, "    <key>%s</key>\n    <string>%s</string>\n", xmlText(key), xmlText(executionPlan.Exec.Environment[key]))
		}
		fmt.Fprintln(&output, "  </dict>")
	}
	fmt.Fprintln(&output, "  <key>RunAtLoad</key>")
	fmt.Fprintln(&output, "  <true/>")
	fmt.Fprintln(&output, "  <key>KeepAlive</key>")
	switch executionPlan.Lifecycle.Restart {
	case "always":
		fmt.Fprintln(&output, "  <dict>")
		fmt.Fprintln(&output, "    <key>PathState</key>")
		fmt.Fprintln(&output, "    <dict>")
		fmt.Fprintf(&output, "      <key>%s</key>\n", xmlText(launchdDemandPath(logDir, artifact)))
		fmt.Fprintln(&output, "      <true/>")
		fmt.Fprintln(&output, "    </dict>")
		fmt.Fprintln(&output, "  </dict>")
	case "on-failure":
		fmt.Fprintln(&output, "  <dict>")
		fmt.Fprintln(&output, "    <key>SuccessfulExit</key>")
		fmt.Fprintln(&output, "    <false/>")
		fmt.Fprintln(&output, "  </dict>")
	default:
		fmt.Fprintln(&output, "  <false/>")
	}
	plistKeyInteger(&output, "ThrottleInterval", executionPlan.Lifecycle.RestartDelaySeconds)
	plistKeyInteger(&output, "ExitTimeOut", executionPlan.Lifecycle.StopTimeoutSeconds)
	plistKeyString(&output, "ProcessType", "Background")
	plistKeyString(&output, "StandardOutPath", artifact.LogPaths[0])
	plistKeyString(&output, "StandardErrorPath", artifact.LogPaths[1])
	fmt.Fprintln(&output, `</dict>`)
	fmt.Fprintln(&output, `</plist>`)
	return []byte(output.String()), nil
}

// launchdDemandPath is both the active-mode marker and a real launchd demand
// source. Modern macOS can leave nondemand KeepAlive jobs pended indefinitely;
// an existing PathState marker makes the user's "keep this tunnel up" intent
// explicit. Scope is part of the name so a migration can stage and roll back
// user and system jobs independently. Plist paths use POSIX semantics on every
// host, including when parsing launchd definitions on Windows.
func launchdDemandPath(logDir string, artifact Artifact) string {
	return path.Join(logDir, "."+artifact.Connection+"."+string(NormalizeScope(artifact.Scope))+".active")
}

func validateLaunchdDemandIdentity(connection string, scope Scope) error {
	if len(connection) < 1 || len(connection) > 63 || !isASCIIAlphaNumeric(connection[0]) {
		return fmt.Errorf("invalid launchd connection identity %q", connection)
	}
	for index := 1; index < len(connection); index++ {
		char := connection[index]
		if !isASCIIAlphaNumeric(char) && char != '.' && char != '_' && char != '-' {
			return fmt.Errorf("invalid launchd connection identity %q", connection)
		}
	}
	if normalized := NormalizeScope(scope); normalized != ScopeUser && normalized != ScopeSystem {
		return fmt.Errorf("invalid launchd scope %q", scope)
	}
	return nil
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func setLaunchdDemand(logDir string, artifact Artifact, active bool) error {
	if err := validateLaunchdDemandIdentity(artifact.Connection, artifact.Scope); err != nil {
		return err
	}
	path := launchdDemandPath(logDir, artifact)
	if active {
		return replaceFile(path, []byte("active\n"), 0o600)
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func launchdDemandActive(logDir string, artifact Artifact) (bool, error) {
	if err := validateLaunchdDemandIdentity(artifact.Connection, artifact.Scope); err != nil {
		return false, err
	}
	_, err := os.Stat(launchdDemandPath(logDir, artifact))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func restoreLaunchdDemand(logDir string, artifact Artifact, active bool) error {
	return setLaunchdDemand(logDir, artifact, active)
}

func plistKeyString(output *strings.Builder, key, value string) {
	fmt.Fprintf(output, "  <key>%s</key>\n  <string>%s</string>\n", xmlText(key), xmlText(value))
}

func plistKeyInteger(output *strings.Builder, key string, value int) {
	fmt.Fprintf(output, "  <key>%s</key>\n  <integer>%d</integer>\n", xmlText(key), value)
}

func xmlText(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

func (l *Launchd) ensureTargetAvailable(ctx context.Context, target Artifact) error {
	opposite := l.rawTarget(oppositeScope(target.Scope), target.Connection)
	opposite.UnitName = target.UnitName
	observation, err := l.inspectIdentityOne(ctx, opposite)
	if err != nil {
		return err
	}
	if nativeIdentityPresent(observation) {
		return &ScopeConflictError{UnitName: target.UnitName}
	}
	return nil
}

func (l *Launchd) install(ctx context.Context, artifact Artifact) ([]byte, bool, error) {
	info, statErr := os.Lstat(artifact.Path)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, false, statErr
	}
	var previous []byte
	var err error
	if existed {
		if NormalizeScope(artifact.Scope) == ScopeSystem {
			if err := l.validateTrustedSystemFile(artifact.Path, info); err != nil {
				return nil, false, fmt.Errorf("refuse to read untrusted system launchd definition: %w", err)
			}
		}
		previous, err = os.ReadFile(artifact.Path)
		if err != nil {
			return nil, false, err
		}
	}
	if NormalizeScope(artifact.Scope) == ScopeUser {
		if err := replaceFile(artifact.Path, artifact.Content, 0o600); err != nil {
			return nil, false, err
		}
		return previous, existed, nil
	}
	if err := l.validateSystemArtifact(artifact); err != nil {
		return nil, false, err
	}
	if err := l.validateManagedSystemDefinition(artifact, artifact.Content); err != nil {
		return nil, false, err
	}
	if existed {
		if err := l.validateManagedSystemDefinition(artifact, previous); err != nil {
			return nil, false, fmt.Errorf("refuse to replace untrusted system launchd definition: %w", err)
		}
	}
	if err := l.installSystemArtifact(ctx, artifact.Path, artifact.Content); err != nil {
		recoveryCtx, cancel := scopeCompensationContext(ctx)
		defer cancel()
		if recoveryErr := l.recoverFailedSystemInstall(recoveryCtx, artifact, previous, existed); recoveryErr != nil {
			return previous, existed, &RecoveryBlockedError{
				Operation: "install system launchd definition", Cause: err, Recovery: recoveryErr,
			}
		}
		return previous, existed, err
	}
	return previous, existed, nil
}

func (l *Launchd) recoverFailedSystemInstall(ctx context.Context, artifact Artifact, previous []byte, existed bool) error {
	info, err := os.Lstat(artifact.Path)
	if errors.Is(err, os.ErrNotExist) {
		if existed {
			return l.installSystemArtifact(ctx, artifact.Path, previous)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := l.validateTrustedSystemFile(artifact.Path, info); err != nil {
		return err
	}
	current, err := os.ReadFile(artifact.Path)
	if err != nil {
		return err
	}
	if existed && bytes.Equal(current, previous) {
		return nil
	}
	if !bytes.Equal(current, artifact.Content) {
		return errors.New("system launchd definition is neither the exact previous nor intended transaction content")
	}
	if existed {
		return l.installSystemArtifact(ctx, artifact.Path, previous)
	}
	return l.removeArtifact(ctx, artifact)
}

func (l *Launchd) restore(ctx context.Context, artifact Artifact, previous []byte, existed bool) error {
	if NormalizeScope(artifact.Scope) == ScopeUser {
		return restoreArtifact(artifact.Path, previous, existed)
	}
	if existed {
		return l.installSystemArtifact(ctx, artifact.Path, previous)
	}
	return l.removeArtifact(ctx, artifact)
}

func (l *Launchd) installSystemArtifact(ctx context.Context, destination string, content []byte) (result error) {
	directoryMetadata, err := l.validateTrustedSystemDirectory()
	if err != nil {
		return err
	}
	template := filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".tunlite.XXXXXXXX")
	stageCtx, cancelStage := scopeCompensationContext(ctx)
	var stageStdout bytes.Buffer
	_, stageErr := l.runner.Run(stageCtx, MacOSSudoProgram, []string{"-n", "--", mktempProgram, template}, Streams{Out: &stageStdout})
	cancelStage()
	staged, err := validateLaunchdStagingPath(destination, stageStdout.Bytes())
	if err != nil {
		if stageErr != nil {
			return errors.Join(&PrivilegeError{Operation: "stage system launchd service", Cause: stageErr},
				fmt.Errorf("privileged mktemp did not return a safely cleanable path: %w", err))
		}
		return fmt.Errorf("unsafe privileged launchd staging path: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := scopeCompensationContext(ctx)
		defer cancel()
		_, cleanupErr := l.runner.Run(cleanupCtx, MacOSSudoProgram, []string{"-n", "--", removeProgram, "-f", staged}, Streams{})
		if cleanupErr != nil {
			cleanupErr = &PrivilegeError{Operation: "remove staged system launchd service " + staged, Cause: cleanupErr}
			result = errors.Join(result, cleanupErr)
		}
	}()
	stageMetadata, err := l.validateTrustedSystemStage(staged, directoryMetadata, nil, 0)
	if err != nil {
		return fmt.Errorf("unsafe privileged launchd staging file: %w", err)
	}
	if stageErr != nil {
		return &PrivilegeError{Operation: "stage system launchd service", Cause: stageErr}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// macOS dd has no O_NOFOLLOW/O_EXCL output flag. Reopening this pathname is
	// safe against the supported non-root threat model because the real daemon
	// directory is root-owned and not group/world writable, mktemp created a
	// root-owned 0600 file with one link, and we verify the exact directory and
	// stage dev+inode again after the fsynced write. A concurrent root adversary
	// is intentionally outside Tunlite's privilege-boundary guarantees.
	if _, err := l.runner.Run(context.WithoutCancel(ctx), MacOSSudoProgram, []string{
		"-n", "--", ddProgram, "of=" + staged, "bs=65536", "conv=fsync",
	}, Streams{In: bytes.NewReader(content)}); err != nil {
		return &PrivilegeError{Operation: "write staged system launchd service", Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := l.validateTrustedSystemStage(staged, directoryMetadata, &stageMetadata, int64(len(content))); err != nil {
		return fmt.Errorf("staged system launchd definition changed during write: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := l.runner.Run(context.WithoutCancel(ctx), MacOSSudoProgram, []string{
		"-n", "--", installProgram, "-S", "-o", "root", "-g", "wheel", "-m", "0644", staged, destination,
	}, Streams{}); err != nil {
		return &PrivilegeError{Operation: "install system launchd service", Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// install -S fsyncs its target file and replaces it atomically through an
	// INS@XXXXXX file in the destination directory. sync(8) additionally
	// forces the rename and directory metadata to stable storage; macOS has no
	// command-line utility for fsyncing one already-open directory descriptor.
	if _, err := l.runner.Run(ctx, syncProgram, nil, Streams{}); err != nil {
		return fmt.Errorf("flush system launchd directory metadata: %w", err)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return fmt.Errorf("inspect installed system launchd definition: %w", err)
	}
	if err := l.validateTrustedSystemFile(destination, info); err != nil {
		return fmt.Errorf("installed system launchd definition is untrusted: %w", err)
	}
	installed, err := os.ReadFile(destination)
	if err != nil {
		return fmt.Errorf("read installed system launchd definition: %w", err)
	}
	if !bytes.Equal(installed, content) {
		return errors.New("installed system launchd definition bytes do not match the staged definition")
	}
	return nil
}

func validateLaunchdStagingPath(destination string, output []byte) (string, error) {
	raw := strings.TrimSuffix(string(output), "\n")
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00") {
		return "", fmt.Errorf("mktemp returned malformed path %q", string(output))
	}
	if !filepath.IsAbs(raw) || filepath.Clean(raw) != raw || filepath.Dir(raw) != filepath.Dir(destination) {
		return "", fmt.Errorf("mktemp returned path outside the canonical daemon directory: %q", raw)
	}
	prefix := "." + filepath.Base(destination) + ".tunlite."
	suffix := strings.TrimPrefix(filepath.Base(raw), prefix)
	if suffix == filepath.Base(raw) || len(suffix) != 8 {
		return "", fmt.Errorf("mktemp returned path with unexpected name %q", raw)
	}
	for _, character := range suffix {
		if !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') {
			return "", fmt.Errorf("mktemp returned path with unsafe suffix %q", suffix)
		}
	}
	return raw, nil
}

func (l *Launchd) validateTrustedSystemFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("system launchd path %q is not a regular non-symlink file", path)
	}
	if info.Mode().Perm() != 0o644 && filepath.Clean(l.daemonDir) == "/Library/LaunchDaemons" {
		return fmt.Errorf("system launchd path %q has mode %04o, expected 0644", path, info.Mode().Perm())
	}
	if filepath.Clean(l.daemonDir) == "/Library/LaunchDaemons" {
		metadata, ok := securityMetadata(info)
		if !ok || metadata.UID != 0 || metadata.GID != 0 {
			return fmt.Errorf("system launchd path %q is not owned by root:wheel", path)
		}
	}
	return nil
}

func (l *Launchd) validateTrustedSystemDirectory() (fileSecurityMetadata, error) {
	info, err := os.Lstat(l.daemonDir)
	if err != nil {
		return fileSecurityMetadata{}, fmt.Errorf("inspect system launchd directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fileSecurityMetadata{}, fmt.Errorf("system launchd directory %q is not a real directory", l.daemonDir)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fileSecurityMetadata{}, fmt.Errorf("system launchd directory %q is group/world writable (%04o)", l.daemonDir, info.Mode().Perm())
	}
	metadata, ok := securityMetadata(info)
	if !ok {
		return fileSecurityMetadata{}, fmt.Errorf("cannot verify system launchd directory identity %q", l.daemonDir)
	}
	if filepath.Clean(l.daemonDir) == "/Library/LaunchDaemons" && (metadata.UID != 0 || metadata.GID != 0) {
		return fileSecurityMetadata{}, fmt.Errorf("system launchd directory %q is not owned by root:wheel", l.daemonDir)
	}
	return metadata, nil
}

func (l *Launchd) validateTrustedSystemStage(
	path string,
	directory fileSecurityMetadata,
	expected *fileSecurityMetadata,
	expectedSize int64,
) (fileSecurityMetadata, error) {
	currentDirectory, err := l.validateTrustedSystemDirectory()
	if err != nil {
		return fileSecurityMetadata{}, err
	}
	if currentDirectory.Dev != directory.Dev || currentDirectory.Ino != directory.Ino {
		return fileSecurityMetadata{}, fmt.Errorf("system launchd directory identity changed during staging")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fileSecurityMetadata{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fileSecurityMetadata{}, fmt.Errorf("stage %q is not a regular non-symlink 0600 file", path)
	}
	metadata, ok := securityMetadata(info)
	if !ok || metadata.Nlink != 1 || metadata.Dev != directory.Dev {
		return fileSecurityMetadata{}, fmt.Errorf("stage %q identity, link count, or filesystem is unsafe", path)
	}
	if filepath.Clean(l.daemonDir) == "/Library/LaunchDaemons" && (metadata.UID != 0 || metadata.GID != 0) {
		return fileSecurityMetadata{}, fmt.Errorf("stage %q is not owned by root:wheel", path)
	}
	if expected != nil && (metadata.Dev != expected.Dev || metadata.Ino != expected.Ino) {
		return fileSecurityMetadata{}, fmt.Errorf("stage %q inode changed during the privileged write", path)
	}
	if info.Size() != expectedSize {
		return fileSecurityMetadata{}, fmt.Errorf("stage %q has size %d, expected %d", path, info.Size(), expectedSize)
	}
	return metadata, nil
}

func (l *Launchd) removeArtifact(ctx context.Context, artifact Artifact) error {
	if NormalizeScope(artifact.Scope) == ScopeUser {
		return os.Remove(artifact.Path)
	}
	if err := l.validateSystemArtifact(artifact); err != nil {
		return err
	}
	info, err := os.Lstat(artifact.Path)
	if err != nil {
		return err
	}
	if err := l.validateTrustedSystemFile(artifact.Path, info); err != nil {
		return fmt.Errorf("refuse to remove untrusted system launchd definition: %w", err)
	}
	content, err := os.ReadFile(artifact.Path)
	if err != nil {
		return err
	}
	if err := l.validateManagedSystemDefinition(artifact, content); err != nil {
		return fmt.Errorf("refuse to remove untrusted system launchd definition: %w", err)
	}
	_, err = l.runner.Run(ctx, MacOSSudoProgram, []string{"-n", "--", removeProgram, "-f", artifact.Path}, Streams{})
	if err != nil {
		return &PrivilegeError{Operation: "remove system launchd service", Cause: err}
	}
	return nil
}

func (l *Launchd) validateSystemArtifact(artifact Artifact) error {
	if NormalizeScope(artifact.Scope) != ScopeSystem {
		return errors.New("system launchd artifact validation requires system scope")
	}
	if err := validateLaunchdDemandIdentity(artifact.Connection, ScopeSystem); err != nil {
		return err
	}
	canonical := l.rawTarget(ScopeSystem, artifact.Connection)
	if artifact.UnitName != canonical.UnitName {
		return fmt.Errorf("system launchd unit %q is not canonical for connection %q", artifact.UnitName, artifact.Connection)
	}
	if filepath.Clean(artifact.Path) != filepath.Clean(canonical.Path) || artifact.Path != canonical.Path {
		return fmt.Errorf("system launchd artifact path %q is not canonical path %q", artifact.Path, canonical.Path)
	}
	return nil
}

func (l *Launchd) validateManagedSystemDefinition(artifact Artifact, content []byte) error {
	if err := l.validateSystemArtifact(artifact); err != nil {
		return err
	}
	embedded := embeddedPlanFromText(string(content), "<!-- "+embeddedPlanPrefix)
	if strings.TrimSpace(strings.TrimSuffix(embedded, "-->")) == "" {
		return errors.New("system launchd definition has no controlled embedded execution plan")
	}
	_, label, _, err := parseLaunchdManagedPlan(content, l.logDir, ScopeSystem, l.userName)
	if err != nil {
		return err
	}
	if label != artifact.UnitName {
		return fmt.Errorf("system launchd definition label %q does not match canonical identity %q", label, artifact.UnitName)
	}
	return nil
}

func (l *Launchd) Materialize(ctx context.Context, artifact Artifact) error {
	if err := l.ensureScopeAvailable(artifact.Scope); err != nil {
		return err
	}
	if err := os.MkdirAll(l.logDir, 0o700); err != nil {
		return err
	}
	if err := l.ensureTargetAvailable(ctx, artifact); err != nil {
		return err
	}
	demandWasActive, err := launchdDemandActive(l.logDir, artifact)
	if err != nil {
		return err
	}
	previous, existed, err := l.install(ctx, artifact)
	if err != nil {
		return fmt.Errorf("install launchd plist: %w", err)
	}
	// A plist in LaunchAgents would otherwise be loaded on the next login.
	// Keep it disabled until the user explicitly chooses persistent mode.
	if !existed {
		if err := setLaunchdDemand(l.logDir, artifact, false); err != nil {
			cause := fmt.Errorf("clear stale launchd demand marker: %w", err)
			return errors.Join(cause, l.restore(ctx, artifact, previous, existed), restoreLaunchdDemand(l.logDir, artifact, demandWasActive))
		}
		if _, err := l.runMutation(ctx, artifact.Scope, "disable", l.domainTarget(artifact.Scope, artifact.UnitName)); err != nil {
			return errors.Join(err, l.restore(ctx, artifact, previous, existed), restoreLaunchdDemand(l.logDir, artifact, demandWasActive))
		}
	}
	return nil
}

func (l *Launchd) Remove(ctx context.Context, reference Reference) error {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return err
	}
	target := l.resolve(reference)
	if NormalizeScope(target.Scope) == ScopeSystem {
		if err := l.validateSystemArtifact(target); err != nil {
			return err
		}
	}
	// Scope transitions intentionally have both native definitions staged for
	// a short window. Snapshot only the definition being removed; the ordinary
	// public Inspect path continues to reject cross-scope twins.
	before, err := l.inspectOne(ctx, target)
	if err != nil {
		return err
	}
	if before.Installed && NormalizeScope(target.Scope) == ScopeSystem {
		if err := l.validateManagedSystemDefinition(target, before.Artifact); err != nil {
			return err
		}
	}
	demandWasActive, err := launchdDemandActive(l.logDir, target)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		var installErr error
		if before.Installed && len(before.Artifact) > 0 {
			restoreTarget := target
			restoreTarget.Content = append([]byte(nil), before.Artifact...)
			_, _, installErr = l.install(ctx, restoreTarget)
		}
		demandErr := restoreLaunchdDemand(l.logDir, target, demandWasActive)
		restoreErr := l.restoreObservation(ctx, target, before)
		// restoreObservation derives the normal marker state from the native
		// mode; restore the exact pre-operation marker last so rollback also
		// preserves a detectable drift state.
		exactDemandErr := restoreLaunchdDemand(l.logDir, target, demandWasActive)
		return errors.Join(cause, installErr, demandErr, restoreErr, exactDemandErr)
	}
	if err := setLaunchdDemand(l.logDir, target, false); err != nil {
		return err
	}
	if _, err := l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return rollback(err)
	}
	output, err := l.runMutation(ctx, target.Scope, "bootout", l.domainTarget(target.Scope, target.UnitName))
	if err != nil && !missingService(output) {
		return rollback(err)
	}
	if err := l.removeArtifact(ctx, target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return rollback(err)
	}
	// Leave the label explicitly enabled after the definition is gone so a
	// later job reusing it is not silently disabled. launchctl has no safe
	// per-label command that removes this override; reset-disabled would alter
	// every service in the user's domain.
	_, err = l.runMutation(ctx, target.Scope, "enable", l.domainTarget(target.Scope, target.UnitName))
	if err != nil {
		return rollback(err)
	}
	return nil
}

type launchdScopeTransition struct {
	launchd       *Launchd
	source        Artifact
	target        Artifact
	sourceState   Observation
	sourceDemand  bool
	sourceContent []byte
	committed     bool
}

type launchdScopeInstall struct {
	launchd   *Launchd
	target    Artifact
	committed bool
}

type launchdDefinitionReplace struct {
	launchd      *Launchd
	target       Artifact
	before       Observation
	beforeDemand bool
	committed    bool
}

func (l *Launchd) CheckDefinitionReplace(ctx context.Context, source Reference, target Artifact) error {
	if NormalizeScope(source.Scope) != ScopeSystem || NormalizeScope(target.Scope) != ScopeSystem {
		return errors.New("privileged launchd definition replacement is only valid in system scope")
	}
	resolved := l.resolve(source)
	if resolved.UnitName != target.UnitName || resolved.Connection != target.Connection || filepath.Clean(resolved.Path) != filepath.Clean(target.Path) {
		return errors.New("launchd definition replacement must preserve native identity")
	}
	if err := l.validateSystemArtifact(target); err != nil {
		return err
	}
	if err := l.validateManagedSystemDefinition(target, target.Content); err != nil {
		return err
	}
	before, err := l.inspectOne(ctx, resolved)
	if err != nil {
		return err
	}
	if !before.Installed {
		return errors.New("system launchd definition is missing")
	}
	if err := l.validateManagedSystemDefinition(resolved, before.Artifact); err != nil {
		return err
	}
	return nil
}

func (l *Launchd) PreflightDefinitionReplace(ctx context.Context, source Reference, target Artifact) error {
	if err := l.CheckDefinitionReplace(ctx, source, target); err != nil {
		return err
	}
	if _, err := l.runner.Run(ctx, MacOSSudoProgram, []string{"-n", "--", trueProgram}, Streams{}); err != nil {
		return &PrivilegeError{Operation: "preflight system launchd definition replacement", Cause: err}
	}
	return nil
}

func (l *Launchd) StageDefinitionReplace(
	ctx context.Context,
	source Reference,
	target Artifact,
	mode string,
	verify func(context.Context, Reference) error,
) (ScopeTransition, error) {
	if mode != ModePersistent {
		return nil, errors.New("system launchd definition replacement requires persistent mode")
	}
	if err := l.PreflightDefinitionReplace(ctx, source, target); err != nil {
		return nil, err
	}
	resolved := l.resolve(source)
	before, err := l.inspectOne(ctx, resolved)
	if err != nil {
		return nil, err
	}
	beforeDemand, err := launchdDemandActive(l.logDir, resolved)
	if err != nil {
		return nil, err
	}
	transaction := &launchdDefinitionReplace{launchd: l, target: target, before: before, beforeDemand: beforeDemand}
	rollback := func(cause error) (ScopeTransition, error) {
		recoveryCtx, cancel := scopeCompensationContext(ctx)
		defer cancel()
		if recoveryErr := transaction.Rollback(recoveryCtx); recoveryErr != nil {
			return nil, &RecoveryBlockedError{Operation: "replace system launchd definition", Cause: cause, Recovery: recoveryErr}
		}
		return nil, cause
	}
	if _, _, err := l.install(ctx, target); err != nil {
		return rollback(err)
	}
	output, err := l.runMutation(ctx, ScopeSystem, "bootout", l.domainTarget(ScopeSystem, target.UnitName))
	if err != nil && !missingService(output) {
		return rollback(err)
	}
	if err := setLaunchdDemand(l.logDir, target, true); err != nil {
		return rollback(err)
	}
	if _, err := l.runMutation(ctx, ScopeSystem, "enable", l.domainTarget(ScopeSystem, target.UnitName)); err != nil {
		return rollback(err)
	}
	if err := l.bootstrapOrKickstart(ctx, target, false); err != nil {
		return rollback(err)
	}
	if err := l.observeTransitionTarget(ctx, target, verify); err != nil {
		return rollback(err)
	}
	return transaction, nil
}

func (t *launchdDefinitionReplace) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *launchdDefinitionReplace) Rollback(ctx context.Context) error {
	if t.committed {
		return errors.New("launchd definition replacement is already committed")
	}
	previous := t.target
	previous.Content = append([]byte(nil), t.before.Artifact...)
	_, _, installErr := t.launchd.install(ctx, previous)
	restoreErr := t.launchd.restoreObservation(ctx, previous, t.before)
	demandErr := restoreLaunchdDemand(t.launchd.logDir, previous, t.beforeDemand)
	return errors.Join(installErr, restoreErr, demandErr)
}

func (l *Launchd) CheckScopeInstall(ctx context.Context, target Artifact) error {
	if !SupportsScope(l, target.Scope) {
		return &UnsupportedScopeError{Adapter: l.Name(), Scope: target.Scope}
	}
	if target.Scope == ScopeSystem {
		if err := l.validateSystemArtifact(target); err != nil {
			return err
		}
	}
	current, err := l.inspectIdentityOne(ctx, target)
	if err != nil {
		return err
	}
	if nativeIdentityPresent(current) {
		return &ScopeConflictError{UnitName: target.UnitName}
	}
	return l.ensureTargetAvailable(ctx, target)
}

func (l *Launchd) PreflightScopeInstall(ctx context.Context, target Artifact) error {
	if err := l.CheckScopeInstall(ctx, target); err != nil {
		return err
	}
	if target.Scope == ScopeSystem {
		if _, err := l.runner.Run(ctx, MacOSSudoProgram, []string{"-n", "--", trueProgram}, Streams{}); err != nil {
			return &PrivilegeError{Operation: "preflight system launchd installation", Cause: err}
		}
	}
	return nil
}

func (l *Launchd) StageScopeInstall(
	ctx context.Context,
	target Artifact,
	mode string,
	verify func(context.Context, Reference) error,
) (ScopeTransition, error) {
	if mode != ModeStopped && mode != ModeBackground && mode != ModePersistent {
		return nil, fmt.Errorf("cannot install launchd service in mode %s", mode)
	}
	if err := l.PreflightScopeInstall(ctx, target); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(l.logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create launchd log directory: %w", err)
	}
	transition := &launchdScopeInstall{launchd: l, target: target}
	rollback := func(cause error) (ScopeTransition, error) {
		recoveryCtx, cancel := scopeCompensationContext(ctx)
		defer cancel()
		recoveryErr := transition.Rollback(recoveryCtx)
		if recoveryErr != nil {
			return nil, &RecoveryBlockedError{Operation: "install launchd service", Cause: cause, Recovery: recoveryErr}
		}
		return nil, cause
	}
	if _, _, err := l.install(ctx, target); err != nil {
		return rollback(err)
	}
	if _, err := l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return rollback(err)
	}
	if err := l.applyModeOne(ctx, target, mode); err != nil {
		return rollback(err)
	}
	if mode != ModeStopped {
		if err := l.observeTransitionTarget(ctx, target, verify); err != nil {
			return rollback(err)
		}
	}
	return transition, nil
}

func (t *launchdScopeInstall) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *launchdScopeInstall) Rollback(ctx context.Context) error {
	if t.committed {
		return errors.New("launchd scope installation is already committed")
	}
	return t.launchd.Remove(ctx, t.target.Reference())
}

func (l *Launchd) PreflightScopeTransition(ctx context.Context, sourceReference Reference, target Artifact) error {
	if err := l.CheckScopeTransition(ctx, sourceReference, target); err != nil {
		return err
	}
	if NormalizeScope(sourceReference.Scope) == ScopeSystem || target.Scope == ScopeSystem {
		if _, err := l.runner.Run(ctx, MacOSSudoProgram, []string{"-n", "--", trueProgram}, Streams{}); err != nil {
			return &PrivilegeError{Operation: "preflight system launchd migration", Cause: err}
		}
	}
	return nil
}

func (l *Launchd) CheckScopeTransition(ctx context.Context, sourceReference Reference, target Artifact) error {
	if err := l.ensureScopeAvailable(sourceReference.Scope); err != nil {
		return err
	}
	if err := l.ensureScopeAvailable(target.Scope); err != nil {
		return err
	}
	source := l.resolve(sourceReference)
	if source.Scope == target.Scope {
		return nil
	}
	if err := l.validateTransitionIdentity(source, target); err != nil {
		return err
	}
	sourceState, err := l.inspectOne(ctx, source)
	if err != nil {
		return err
	}
	if !sourceState.Installed {
		return errors.New("source launchd service is not installed")
	}
	targetState, err := l.inspectIdentityOne(ctx, target)
	if err != nil {
		return err
	}
	if nativeIdentityPresent(targetState) {
		return &ScopeConflictError{UnitName: target.UnitName}
	}
	return nil
}

func (l *Launchd) StageScopeTransition(
	ctx context.Context,
	sourceReference Reference,
	target Artifact,
	mode string,
	verify func(context.Context, Reference) error,
) (ScopeTransition, error) {
	source := l.resolve(sourceReference)
	if source.Scope == target.Scope {
		return nil, fmt.Errorf("source and target service scopes are both %s", source.Scope)
	}
	if mode != ModeStopped && mode != ModeBackground && mode != ModePersistent {
		return nil, fmt.Errorf("cannot migrate launchd service in mode %s", mode)
	}
	if err := l.PreflightScopeTransition(ctx, sourceReference, target); err != nil {
		return nil, err
	}
	sourceState, err := l.inspectOne(ctx, source)
	if err != nil {
		return nil, err
	}
	if !sourceState.Installed {
		return nil, errors.New("source launchd service is not installed")
	}
	sourceDemand, err := launchdDemandActive(l.logDir, source)
	if err != nil {
		return nil, err
	}
	targetState, err := l.inspectOne(ctx, target)
	if err != nil {
		return nil, err
	}
	if nativeIdentityPresent(targetState) {
		return nil, &ScopeConflictError{UnitName: target.UnitName}
	}
	transition := &launchdScopeTransition{
		launchd: l, source: source, target: target, sourceState: sourceState,
		sourceDemand:  sourceDemand,
		sourceContent: append([]byte(nil), sourceState.Artifact...),
	}
	rollback := func(cause error) (ScopeTransition, error) {
		recoveryCtx, cancel := scopeCompensationContext(ctx)
		defer cancel()
		recoveryErr := transition.Rollback(recoveryCtx)
		if recoveryErr != nil {
			return nil, &RecoveryBlockedError{Operation: "migrate launchd service scope", Cause: cause, Recovery: recoveryErr}
		}
		return nil, cause
	}
	if err := l.stopOne(ctx, source); err != nil {
		return rollback(err)
	}
	if _, _, err := l.install(ctx, target); err != nil {
		return rollback(err)
	}
	if _, err := l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return rollback(err)
	}
	if err := l.applyModeOne(ctx, target, mode); err != nil {
		return rollback(err)
	}
	if mode != ModeStopped {
		if err := l.observeTransitionTarget(ctx, target, verify); err != nil {
			return rollback(err)
		}
	}
	return transition, nil
}

func (l *Launchd) validateTransitionIdentity(source, target Artifact) error {
	if source.UnitName == "" || source.UnitName != target.UnitName || source.Connection != target.Connection {
		return errors.New("scope migration must preserve the native service identity")
	}
	if target.Scope == ScopeSystem {
		return l.validateSystemArtifact(target)
	}
	return nil
}

func (l *Launchd) observeTransitionTarget(ctx context.Context, target Artifact, verify func(context.Context, Reference) error) error {
	timeout := l.transitionTimeout
	stableFor := l.transitionStable
	poll := l.transitionPoll
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if stableFor < 0 {
		stableFor = 0
	}
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	var healthySince time.Time
	for {
		observation, err := l.inspectOne(ctx, target)
		if err != nil {
			return err
		}
		if observation.ProcessState == ProcessFailed {
			return errors.New("target launchd service entered failed state")
		}
		healthy := observation.ProcessState == ProcessRunning
		if healthy && verify != nil {
			healthy = verify(ctx, target.Reference()) == nil
		}
		if healthy {
			if healthySince.IsZero() {
				healthySince = time.Now()
			}
			if time.Since(healthySince) >= stableFor {
				return nil
			}
		} else {
			healthySince = time.Time{}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("target launchd service did not remain healthy for %s within %s", stableFor, timeout)
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *Launchd) stopOne(ctx context.Context, target Artifact) error {
	if err := setLaunchdDemand(l.logDir, target, false); err != nil {
		return err
	}
	if _, err := l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return err
	}
	output, err := l.runMutation(ctx, target.Scope, "bootout", l.domainTarget(target.Scope, target.UnitName))
	if err != nil && !missingService(output) {
		return err
	}
	return nil
}

func (l *Launchd) applyModeOne(ctx context.Context, target Artifact, mode string) error {
	if mode == ModeStopped {
		return setLaunchdDemand(l.logDir, target, false)
	}
	if err := setLaunchdDemand(l.logDir, target, true); err != nil {
		return err
	}
	if _, err := l.runMutation(ctx, target.Scope, "enable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return err
	}
	if err := l.bootstrapOrKickstart(ctx, target, false); err != nil {
		return err
	}
	var err error
	if mode == ModeBackground {
		_, err = l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName))
	}
	return err
}

func (t *launchdScopeTransition) Commit(ctx context.Context) error {
	if t.committed {
		return nil
	}
	if err := t.launchd.Remove(ctx, t.source.Reference()); err != nil {
		return err
	}
	t.committed = true
	return nil
}

func (t *launchdScopeTransition) Rollback(ctx context.Context) error {
	if t.committed {
		return errors.New("launchd scope transition is already committed")
	}
	removeTargetErr := t.launchd.Remove(ctx, t.target.Reference())
	if removeTargetErr != nil {
		// The target may still be runnable or persistent. Starting the source in
		// that state could create two SSH processes competing for one remote
		// forward, so leave the source stopped and require explicit recovery.
		return fmt.Errorf("remove transition target before restoring source: %w", removeTargetErr)
	}
	currentSource, inspectErr := t.launchd.inspectOne(ctx, t.source)
	var installErr error
	if inspectErr == nil && !currentSource.Installed {
		_, _, installErr = t.launchd.install(ctx, Artifact{
			Connection: t.source.Connection, Adapter: t.source.Adapter, Scope: t.source.Scope,
			UnitName: t.source.UnitName, Path: t.source.Path, Content: t.sourceContent,
			LogPaths: append([]string(nil), t.source.LogPaths...),
		})
	}
	restoreErr := t.launchd.restoreObservation(ctx, t.source, t.sourceState)
	exactDemandErr := restoreLaunchdDemand(t.launchd.logDir, t.source, t.sourceDemand)
	return errors.Join(inspectErr, installErr, restoreErr, exactDemandErr)
}

func scopeCompensationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
}

func (l *Launchd) Start(ctx context.Context, reference Reference) error {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return err
	}
	if NormalizeScope(reference.Scope) == ScopeSystem {
		return errors.New("system launchd services cannot run in temporary background mode; use enable")
	}
	target := l.resolve(reference)
	before, err := l.Inspect(ctx, reference)
	if err != nil {
		return err
	}
	demandWasActive, err := launchdDemandActive(l.logDir, target)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		return errors.Join(cause, l.restoreObservation(ctx, target, before), restoreLaunchdDemand(l.logDir, target, demandWasActive))
	}
	if err := setLaunchdDemand(l.logDir, target, true); err != nil {
		return err
	}
	// A disabled override prevents the next login from loading the job. The
	// active PathState marker remains a valid demand for this loaded session.
	if _, err := l.runMutation(ctx, target.Scope, "enable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return rollback(err)
	}
	if err := l.bootstrapOrKickstart(ctx, target, false); err != nil {
		return rollback(err)
	}
	if _, err = l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return rollback(err)
	}
	return nil
}

func (l *Launchd) Stop(ctx context.Context, reference Reference) error {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return err
	}
	if NormalizeScope(reference.Scope) == ScopeSystem {
		return errors.New("system launchd services are persistent; use disable to stop and unregister them")
	}
	target := l.resolve(reference)
	before, err := l.Inspect(ctx, reference)
	if err != nil {
		return err
	}
	demandWasActive, err := launchdDemandActive(l.logDir, target)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		return errors.Join(cause, l.restoreObservation(ctx, target, before), restoreLaunchdDemand(l.logDir, target, demandWasActive))
	}
	if err := setLaunchdDemand(l.logDir, target, false); err != nil {
		return err
	}
	if _, err := l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return rollback(err)
	}
	output, err := l.runMutation(ctx, target.Scope, "bootout", l.domainTarget(target.Scope, target.UnitName))
	if err != nil && !missingService(output) {
		return rollback(err)
	}
	return nil
}

func (l *Launchd) Restart(ctx context.Context, reference Reference) error {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return err
	}
	if NormalizeScope(reference.Scope) == ScopeSystem {
		return l.restartSystemPersistent(ctx, reference)
	}
	target := l.resolve(reference)
	before, err := l.Inspect(ctx, reference)
	if err != nil {
		return err
	}
	demandWasActive, err := launchdDemandActive(l.logDir, target)
	if err != nil {
		return err
	}
	output, err := l.runMutation(ctx, target.Scope, "bootout", l.domainTarget(target.Scope, target.UnitName))
	if err != nil && !missingService(output) {
		return err
	}
	rollback := func(cause error) error {
		return errors.Join(cause, l.restoreObservation(ctx, target, before), restoreLaunchdDemand(l.logDir, target, demandWasActive))
	}
	if err := setLaunchdDemand(l.logDir, target, true); err != nil {
		return rollback(err)
	}
	if _, err := l.runMutation(ctx, target.Scope, "enable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return rollback(err)
	}
	if err := l.bootstrapOrKickstart(ctx, target, true); err != nil {
		return rollback(err)
	}
	if !before.AutoStart {
		if _, err = l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
			return rollback(err)
		}
	}
	return nil
}

// restartSystemPersistent deliberately does not mutate the system launchd
// domain. A system service is restarted by terminating the verified non-root
// provider and allowing its already-registered KeepAlive policy to replace it.
// Every check happens before the signal, and there is no privileged fallback.
func (l *Launchd) restartSystemPersistent(ctx context.Context, reference Reference) error {
	target := l.resolve(reference)
	if err := l.validateSystemArtifact(target); err != nil {
		return err
	}
	before, expectedPlan, oldPID, err := l.verifySystemRestartTarget(ctx, reference, target, 0)
	if err != nil {
		return err
	}
	if before.ProcessState != ProcessRunning || !before.AutoStart {
		return errors.New("system launchd service is not a running persistent job")
	}
	// Close the easy PID-reuse/job-replacement window by checking launchd and
	// the process identity a second time immediately before sending SIGTERM.
	if _, _, confirmedPID, err := l.verifySystemRestartTarget(ctx, reference, target, oldPID); err != nil {
		return err
	} else if confirmedPID != oldPID {
		return errors.New("system launchd provider PID changed during restart verification")
	}
	if _, err := l.runner.Run(ctx, killProgram, []string{"-TERM", strconv.Itoa(oldPID)}, Streams{}); err != nil {
		return fmt.Errorf("terminate verified system launchd provider pid %d: %w", oldPID, err)
	}
	return l.waitForSystemRestart(ctx, reference, target, expectedPlan, oldPID)
}

func (l *Launchd) verifySystemRestartTarget(
	ctx context.Context,
	reference Reference,
	target Artifact,
	requirePID int,
) (Observation, plan.ExecutionPlan, int, error) {
	if l.userName == "" || l.userName == "root" || l.uid <= 0 {
		return Observation{}, plan.ExecutionPlan{}, 0, errors.New("system launchd provider is not configured to run as the current non-root user")
	}
	if reference.BaselineDigest == "" || reference.PlanDigest == "" {
		return Observation{}, plan.ExecutionPlan{}, 0, errors.New("system launchd restart requires a recorded definition and execution-plan baseline")
	}
	if target.UnitName == "" || target.UnitName != "dev.tunlite.connection."+target.Connection {
		return Observation{}, plan.ExecutionPlan{}, 0, errors.New("system launchd native identity does not match the managed connection")
	}
	observation, err := l.inspectOne(ctx, target)
	if err != nil {
		return Observation{}, plan.ExecutionPlan{}, 0, err
	}
	if !observation.Installed || len(observation.Artifact) == 0 {
		return Observation{}, plan.ExecutionPlan{}, 0, errors.New("system launchd definition is missing")
	}
	if ContentDigest(observation.Artifact) != reference.BaselineDigest {
		return Observation{}, plan.ExecutionPlan{}, 0, errors.New("system launchd definition drifted from the recorded baseline")
	}
	expectedPlan, label, _, err := parseLaunchdManagedPlan(observation.Artifact, l.logDir, ScopeSystem, l.userName)
	if err != nil {
		return Observation{}, plan.ExecutionPlan{}, 0, fmt.Errorf("validate system launchd definition: %w", err)
	}
	if label != target.UnitName {
		return Observation{}, plan.ExecutionPlan{}, 0, errors.New("system launchd definition label does not match its native identity")
	}
	if digest, digestErr := PlanDigest(expectedPlan); digestErr != nil {
		return Observation{}, plan.ExecutionPlan{}, 0, digestErr
	} else if digest != reference.PlanDigest {
		return Observation{}, plan.ExecutionPlan{}, 0, fmt.Errorf("system launchd execution plan drifted from the recorded baseline (%s != %s)", digest, reference.PlanDigest)
	}
	demand, err := launchdDemandActive(l.logDir, target)
	if err != nil {
		return Observation{}, plan.ExecutionPlan{}, 0, err
	}
	if !demand || !observation.AutoStart {
		return Observation{}, plan.ExecutionPlan{}, 0, errors.New("system launchd KeepAlive demand is not active")
	}
	pid, err := launchdObservationPID(observation)
	if err != nil {
		return Observation{}, plan.ExecutionPlan{}, 0, err
	}
	if requirePID > 0 && pid != requirePID {
		return Observation{}, plan.ExecutionPlan{}, 0, errors.New("system launchd provider PID changed during restart verification")
	}
	if path := observation.Platform["path"]; path != target.Path {
		return Observation{}, plan.ExecutionPlan{}, 0, fmt.Errorf("system launchd job path %q does not match managed definition %q", path, target.Path)
	}
	if program := observation.Platform["program"]; program != expectedPlan.Exec.Program {
		return Observation{}, plan.ExecutionPlan{}, 0, fmt.Errorf("system launchd job program %q does not match expected provider %q", program, expectedPlan.Exec.Program)
	}
	if err := l.verifyProviderProcess(ctx, pid, expectedPlan.Exec.Program); err != nil {
		return Observation{}, plan.ExecutionPlan{}, 0, err
	}
	return observation, expectedPlan, pid, nil
}

func (l *Launchd) verifyProviderProcess(ctx context.Context, pid int, expectedProgram string) error {
	output, err := l.runner.Run(ctx, psProgram, []string{"-p", strconv.Itoa(pid), "-o", "uid=", "-o", "comm="}, Streams{})
	if err != nil {
		return fmt.Errorf("inspect system launchd provider pid %d: %w", pid, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return fmt.Errorf("cannot prove identity of system launchd provider pid %d", pid)
	}
	uid, err := strconv.Atoi(fields[0])
	if err != nil || uid != l.uid || uid == 0 {
		return fmt.Errorf("system launchd provider pid %d is not owned by the configured non-root user", pid)
	}
	if filepath.Clean(fields[1]) != filepath.Clean(expectedProgram) {
		return fmt.Errorf("system launchd provider pid %d executable %q does not match %q", pid, fields[1], expectedProgram)
	}
	return nil
}

func (l *Launchd) waitForSystemRestart(ctx context.Context, reference Reference, target Artifact, expected plan.ExecutionPlan, oldPID int) error {
	timeout, poll := l.transitionTimeout, l.transitionPoll
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		observation, _, pid, err := l.verifySystemRestartTarget(ctx, reference, target, 0)
		if err == nil && observation.ProcessState == ProcessRunning && pid != oldPID {
			if healthErr := verifyExecutionPlanHealth(ctx, expected); healthErr == nil {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("system launchd KeepAlive did not converge on a healthy replacement for pid %d within %s", oldPID, timeout)
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func verifyExecutionPlanHealth(ctx context.Context, value plan.ExecutionPlan) error {
	for _, check := range value.HealthChecks {
		probeCtx, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutMS)*time.Millisecond)
		connection, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", check.Address)
		cancel()
		if err != nil {
			return err
		}
		_ = connection.Close()
	}
	return nil
}

func launchdObservationPID(observation Observation) (int, error) {
	raw, _ := observation.Platform["pid"].(string)
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 || observation.ProcessState != ProcessRunning {
		return 0, errors.New("system launchd job has no stable running provider PID")
	}
	return pid, nil
}

func (l *Launchd) Enable(ctx context.Context, reference Reference) error {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return err
	}
	target := l.resolve(reference)
	before, err := l.Inspect(ctx, reference)
	if err != nil {
		return err
	}
	demandWasActive, err := launchdDemandActive(l.logDir, target)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		return errors.Join(cause, l.restoreObservation(ctx, target, before), restoreLaunchdDemand(l.logDir, target, demandWasActive))
	}
	if err := setLaunchdDemand(l.logDir, target, true); err != nil {
		return rollback(err)
	}
	if _, err := l.runMutation(ctx, target.Scope, "enable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return rollback(err)
	}
	// A background job that is already running must become persistent without
	// a kickstart: -k would kill the SSH process and briefly drop the tunnel.
	if before.ProcessState == ProcessRunning || before.ProcessState == ProcessStarting {
		return nil
	}
	if err := l.bootstrapOrKickstart(ctx, target, false); err != nil {
		return rollback(err)
	}
	return nil
}

func (l *Launchd) Disable(ctx context.Context, reference Reference) error {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return err
	}
	if NormalizeScope(reference.Scope) == ScopeSystem {
		// Remove performs disable + bootout + root-owned artifact removal as one
		// rollback-capable adapter transaction. The user-level Record is retained
		// by the application so the system scope intent can be enabled again.
		return l.Remove(ctx, reference)
	}
	target := l.resolve(reference)
	before, err := l.Inspect(ctx, reference)
	if err != nil {
		return err
	}
	if err := setLaunchdDemand(l.logDir, target, false); err != nil {
		return err
	}
	if _, err := l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return errors.Join(err, l.restoreObservation(ctx, target, before))
	}
	output, err := l.runMutation(ctx, target.Scope, "bootout", l.domainTarget(target.Scope, target.UnitName))
	if err != nil && !missingService(output) {
		return errors.Join(err, l.restoreObservation(ctx, target, before))
	}
	return nil
}

func (l *Launchd) PreflightDisable(ctx context.Context, reference Reference) error {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return err
	}
	if NormalizeScope(reference.Scope) != ScopeSystem {
		return nil
	}
	target := l.resolve(reference)
	if err := l.validateSystemArtifact(target); err != nil {
		return err
	}
	observation, err := l.inspectOne(ctx, target)
	if err != nil {
		return err
	}
	if observation.Installed {
		if err := l.validateManagedSystemDefinition(target, observation.Artifact); err != nil {
			return err
		}
	}
	if _, err := l.runner.Run(ctx, MacOSSudoProgram, []string{"-n", "--", trueProgram}, Streams{}); err != nil {
		return &PrivilegeError{Operation: "preflight system launchd disable", Cause: err}
	}
	return nil
}

func (l *Launchd) restoreObservation(ctx context.Context, target Artifact, observation Observation) error {
	if observation.AutoStart {
		if err := setLaunchdDemand(l.logDir, target, true); err != nil {
			return err
		}
		if _, err := l.runMutation(ctx, target.Scope, "enable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
			return err
		}
		return l.bootstrapOrKickstart(ctx, target, false)
	}
	if observation.ProcessState == ProcessRunning || observation.ProcessState == ProcessStarting || observation.ProcessState == ProcessFailed {
		if err := setLaunchdDemand(l.logDir, target, true); err != nil {
			return err
		}
		if _, err := l.runMutation(ctx, target.Scope, "enable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
			return err
		}
		if err := l.bootstrapOrKickstart(ctx, target, false); err != nil {
			return err
		}
		_, err := l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName))
		return err
	}
	if err := setLaunchdDemand(l.logDir, target, false); err != nil {
		return err
	}
	if _, err := l.runMutation(ctx, target.Scope, "disable", l.domainTarget(target.Scope, target.UnitName)); err != nil {
		return err
	}
	output, err := l.runMutation(ctx, target.Scope, "bootout", l.domainTarget(target.Scope, target.UnitName))
	if err != nil && !missingService(output) {
		return err
	}
	return nil
}

// bootstrapOrKickstart loads the job, then starts it without replacing a
// process created during bootstrap. Using kickstart -k unconditionally can
// kill that fresh process and poison launchd's crash-loop accounting. Only an
// explicitly requested replacement of an already-loaded job uses -k.
func (l *Launchd) bootstrapOrKickstart(ctx context.Context, target Artifact, replaceLoaded bool) error {
	output, err := l.runMutation(ctx, target.Scope, "bootstrap", l.domain(target.Scope), target.Path)
	if err != nil && !strings.Contains(strings.ToLower(string(output)), "already") {
		return err
	}
	args := []string{"kickstart"}
	if err != nil && replaceLoaded {
		args = append(args, "-k")
	}
	args = append(args, l.domainTarget(target.Scope, target.UnitName))
	_, err = l.runMutation(ctx, target.Scope, args...)
	return err
}

func (l *Launchd) Inspect(ctx context.Context, reference Reference) (Observation, error) {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return Observation{Scope: NormalizeScope(reference.Scope), ProcessState: ProcessUnknown}, err
	}
	target := l.resolve(reference)
	observation, err := l.inspectIdentityOne(ctx, target)
	if err != nil {
		return observation, err
	}
	opposite := l.rawTarget(oppositeScope(target.Scope), target.Connection)
	opposite.UnitName = target.UnitName
	oppositeObservation, oppositeErr := l.inspectIdentityOne(ctx, opposite)
	if oppositeErr != nil {
		return observation, oppositeErr
	}
	if nativeIdentityPresent(oppositeObservation) {
		if nativeIdentityPresent(observation) {
			return observation, &ScopeConflictError{UnitName: target.UnitName}
		}
		return observation, &ScopeMismatchError{UnitName: target.UnitName, Expected: target.Scope, Actual: opposite.Scope}
	}
	return observation, nil
}

// inspectIdentityOne checks the plist even when a launchd domain is currently
// unavailable. Runtime-only jobs can only be queried in an available domain,
// but a static identity must still prevent a cross-scope twin.
func (l *Launchd) inspectIdentityOne(ctx context.Context, target Artifact) (Observation, error) {
	available := l.userDomainAvailable
	if NormalizeScope(target.Scope) == ScopeSystem {
		available = l.systemDomainAvailable
	}
	var observation Observation
	var err error
	if available {
		observation, err = l.inspectOne(ctx, target)
		if err != nil || nativeIdentityPresent(observation) {
			return observation, err
		}
	} else {
		observation = Observation{
			Scope: target.Scope, ProcessState: ProcessInactive,
			Platform: map[string]any{"label": target.UnitName, "domain": l.domain(target.Scope), "domainAvailable": false},
		}
		artifact, readErr := os.ReadFile(target.Path)
		if readErr == nil {
			observation.Installed = true
			observation.Artifact = artifact
			return observation, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return observation, readErr
		}
	}
	staticObservation, found, err := l.findStaticIdentity(target.Scope, target.UnitName)
	if err != nil {
		return observation, err
	}
	if found {
		return staticObservation, nil
	}
	return observation, nil
}

func (l *Launchd) findStaticIdentity(scope Scope, label string) (Observation, bool, error) {
	directory := l.agentDir
	if NormalizeScope(scope) == ScopeSystem {
		directory = l.daemonDir
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return Observation{}, false, nil
	}
	if err != nil {
		return Observation{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".plist") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		dictionary, decodeErr := decodeLaunchdDictionary(raw)
		if decodeErr != nil {
			continue
		}
		candidate, _ := dictionary["Label"].(string)
		if candidate != label {
			continue
		}
		return Observation{
			Scope: scope, Installed: true, ProcessState: ProcessInactive, Artifact: raw,
			Platform: map[string]any{"label": label, "domain": l.domain(scope), "artifactPath": path},
		}, true, nil
	}
	return Observation{}, false, nil
}

func (l *Launchd) inspectOne(ctx context.Context, target Artifact) (Observation, error) {
	observation := Observation{
		Scope: target.Scope, ProcessState: ProcessUnknown,
		Platform: map[string]any{"label": target.UnitName, "domain": l.domain(target.Scope)},
	}
	artifact, err := os.ReadFile(target.Path)
	if err == nil {
		observation.Installed = true
		observation.Artifact = artifact
	} else if !errors.Is(err, os.ErrNotExist) {
		return observation, err
	}
	disabledOutput, disabledErr := l.run(ctx, "print-disabled", l.domain(target.Scope))
	if disabledErr != nil {
		return observation, fmt.Errorf("inspect launchd persistence state: %w", disabledErr)
	}
	autoStart := !launchdLabelDisabled(disabledOutput, target.UnitName)
	output, err := l.run(ctx, "print", l.domainTarget(target.Scope, target.UnitName))
	if err != nil {
		if missingService(output) {
			// launchd treats an unknown label as enabled by default, but a label
			// with neither a loaded job nor a plist has nothing to auto-start.
			observation.AutoStart = observation.Installed && autoStart
			observation.ProcessState = ProcessInactive
			return observation, nil
		}
		return observation, err
	}
	observation.AutoStart = autoStart
	properties := parseLaunchdProperties(output)
	for key, value := range properties {
		observation.Platform[key] = value
	}
	state := properties["state"]
	switch state {
	case "running":
		observation.ProcessState = ProcessRunning
	case "active":
		// launchd also reports an enabled KeepAlive job as active while it is
		// waiting for the rapid-restart throttle and has no child process.
		// Only treat active as running when launchctl exposes a real PID.
		if pid, parseErr := strconv.Atoi(properties["pid"]); parseErr == nil && pid > 0 {
			observation.ProcessState = ProcessRunning
		} else {
			observation.ProcessState = ProcessStarting
		}
	case "spawn scheduled", "waiting", "xpcproxy":
		observation.ProcessState = ProcessStarting
	default:
		if properties["lastExitCode"] != "" && properties["lastExitCode"] != "0" {
			observation.ProcessState = ProcessFailed
		} else {
			observation.ProcessState = ProcessInactive
		}
	}
	if observation.ProcessState == ProcessRunning {
		if pid, parseErr := strconv.Atoi(properties["pid"]); parseErr == nil && pid > 0 {
			queryCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			startedOutput, queryErr := l.runner.Run(queryCtx, psProgram,
				[]string{"-p", strconv.Itoa(pid), "-o", "lstart="}, Streams{})
			cancel()
			if queryErr == nil {
				raw := strings.TrimSpace(string(startedOutput))
				if started, parseErr := time.ParseInLocation("Mon Jan _2 15:04:05 2006", raw, time.Local); parseErr == nil {
					observation.Platform["activeEnterTimestamp"] = started.Format(time.RFC3339)
				}
			}
		}
	}
	return observation, nil
}

func nativeIdentityPresent(observation Observation) bool {
	// An enabled override without a loaded job or plist is only launchd's
	// default label state; it is not a service identity and must not block use.
	return observation.Installed || observation.ProcessState != ProcessInactive
}

func oppositeScope(scope Scope) Scope {
	if NormalizeScope(scope) == ScopeSystem {
		return ScopeUser
	}
	return ScopeSystem
}

func launchdLabelDisabled(output []byte, label string) bool {
	for _, raw := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, `"`+label+`"`) {
			continue
		}
		_, value, found := strings.Cut(line, "=>")
		if !found {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "disabled":
			return true
		default:
			return false
		}
	}
	return false
}

func (l *Launchd) resolve(reference Reference) Artifact {
	target := l.rawTarget(NormalizeScope(reference.Scope), reference.Connection)
	if reference.UnitName != "" {
		target.UnitName = reference.UnitName
	}
	if reference.ArtifactPath != "" {
		target.Path = reference.ArtifactPath
	}
	if len(reference.LogPaths) > 0 {
		target.LogPaths = append([]string(nil), reference.LogPaths...)
	}
	return target
}

func (l *Launchd) Logs(ctx context.Context, reference Reference, lines int, follow bool, streams Streams) error {
	if err := l.ensureScopeAvailable(reference.Scope); err != nil {
		return err
	}
	target := l.resolve(reference)
	paths := make([]string, 0, len(target.LogPaths))
	for _, path := range target.LogPaths {
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(paths) == 0 {
		return nil
	}
	args := []string{"-n", strconv.Itoa(lines)}
	if follow {
		args = append(args, "-F")
	}
	args = append(args, paths...)
	_, err := l.runner.Run(ctx, "tail", args, streams)
	return err
}

func (l *Launchd) Discover(ctx context.Context) ([]Candidate, error) {
	result := make([]Candidate, 0)
	seen := map[string]Scope{}
	sources := make([]struct {
		scope Scope
		dir   string
	}, 0, 2)
	if l.agentDir != "" {
		sources = append(sources, struct {
			scope Scope
			dir   string
		}{ScopeUser, l.agentDir})
	}
	if l.daemonDir != "" {
		sources = append(sources, struct {
			scope Scope
			dir   string
		}{ScopeSystem, l.daemonDir})
	}
	for _, source := range sources {
		entries, err := os.ReadDir(source.dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		candidates, err := l.discoverDirectory(ctx, source.scope, source.dir, entries, seen)
		if err != nil {
			return nil, err
		}
		result = append(result, candidates...)
	}
	return result, nil
}

func (l *Launchd) discoverDirectory(ctx context.Context, scope Scope, directory string, entries []os.DirEntry, seen map[string]Scope) ([]Candidate, error) {
	result := make([]Candidate, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".plist") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		embedded := embeddedPlanFromText(string(raw), "<!-- "+embeddedPlanPrefix)
		embedded = strings.TrimSuffix(embedded, "-->")
		embedded = strings.TrimSpace(embedded)
		value, discoveredLabel, discoveredLogs, decodeErr := parseLaunchdExecutionPlanControlled(raw, l.logDir, scope, l.userName)
		if decodeErr != nil {
			continue
		}
		if embedded != "" {
			embeddedValue, embeddedErr := decodeEmbeddedPlan(embedded)
			if embeddedErr != nil {
				continue
			}
			value, decodeErr = mergeEmbeddedPlan(value, embeddedValue)
		}
		if decodeErr != nil {
			continue
		}
		label := strings.TrimSuffix(entry.Name(), ".plist")
		if discoveredLabel != "" {
			label = discoveredLabel
		}
		if previous, exists := seen[label]; exists && previous != scope {
			return nil, &ScopeConflictError{UnitName: label}
		}
		seen[label] = scope
		connection := strings.TrimPrefix(label, "dev.tunlite.connection.")
		logPaths := discoveredLogs
		if len(logPaths) == 0 {
			logPaths = []string{filepath.Join(l.logDir, connection+".stdout.log"), filepath.Join(l.logDir, connection+".stderr.log")}
		}
		reference := Reference{Connection: connection, Adapter: l.Name(), Scope: scope, UnitName: label, ArtifactPath: path, LogPaths: logPaths}
		observation, inspectErr := l.inspectIdentityOne(ctx, l.resolve(reference))
		if inspectErr != nil {
			continue
		}
		result = append(result, Candidate{Reference: reference, Plan: value, Observation: observation})
	}
	return result, nil
}

func parseLaunchdExecutionPlan(raw []byte) (plan.ExecutionPlan, string, []string, error) {
	return parseLaunchdExecutionPlanControlled(raw, "", ScopeUser, "")
}

func parseLaunchdManagedPlan(raw []byte, controlledLogDir string, actualScope Scope, configuredUserName string) (plan.ExecutionPlan, string, []string, error) {
	value, label, logs, err := parseLaunchdExecutionPlanControlled(raw, controlledLogDir, actualScope, configuredUserName)
	if err != nil {
		return plan.ExecutionPlan{}, "", nil, err
	}
	embedded := embeddedPlanFromText(string(raw), "<!-- "+embeddedPlanPrefix)
	embedded = strings.TrimSpace(strings.TrimSuffix(embedded, "-->"))
	if embedded == "" {
		return value, label, logs, nil
	}
	embeddedValue, err := decodeEmbeddedPlan(embedded)
	if err != nil {
		return plan.ExecutionPlan{}, "", nil, err
	}
	if _, err = mergeEmbeddedPlan(value, embeddedValue); err != nil {
		return plan.ExecutionPlan{}, "", nil, err
	}
	// The embedded plan is the exact canonical value whose digest is recorded;
	// the native parse above has already proved it cannot override executable
	// or lifecycle fields. Returning it preserves nil/empty representation for
	// a stable digest while retaining the native definition as authority.
	return embeddedValue, label, logs, nil
}

func parseLaunchdExecutionPlanControlled(raw []byte, controlledLogDir string, actualScope Scope, configuredUserName string) (plan.ExecutionPlan, string, []string, error) {
	dictionary, err := decodeLaunchdDictionary(raw)
	if err != nil {
		return plan.ExecutionPlan{}, "", nil, err
	}
	allowed := map[string]bool{
		"Label": true, "UserName": true, "Program": true, "ProgramArguments": true, "RunAtLoad": true, "KeepAlive": true,
		"ThrottleInterval": true, "ExitTimeOut": true, "ProcessType": true,
		"StandardOutPath": true, "StandardErrorPath": true,
	}
	for key := range dictionary {
		if !allowed[key] {
			return plan.ExecutionPlan{}, "", nil, fmt.Errorf("launchd key %s cannot be managed losslessly", key)
		}
	}
	actualScope = NormalizeScope(actualScope)
	if actualScope != ScopeUser && actualScope != ScopeSystem {
		return plan.ExecutionPlan{}, "", nil, fmt.Errorf("invalid launchd discovery scope %q", actualScope)
	}
	userName, hasUserName := dictionary["UserName"]
	if actualScope == ScopeUser {
		if hasUserName {
			return plan.ExecutionPlan{}, "", nil, errors.New("user LaunchAgent must not declare UserName")
		}
	} else {
		value, valid := userName.(string)
		if !hasUserName || !valid || value == "" {
			return plan.ExecutionPlan{}, "", nil, errors.New("system LaunchDaemon must declare UserName")
		}
		if configuredUserName == "" || configuredUserName == "root" || value == "root" || value != configuredUserName {
			return plan.ExecutionPlan{}, "", nil, fmt.Errorf("system LaunchDaemon UserName %q does not match configured non-root user %q", value, configuredUserName)
		}
	}
	for _, forbidden := range []string{"StartCalendarInterval", "StartInterval", "WatchPaths", "QueueDirectories", "OtherJobEnabled"} {
		if _, exists := dictionary[forbidden]; exists {
			return plan.ExecutionPlan{}, "", nil, fmt.Errorf("launchd key %s cannot map to a standard Tunlite mode", forbidden)
		}
	}
	argumentsValue, ok := dictionary["ProgramArguments"].([]any)
	if !ok || len(argumentsValue) == 0 {
		return plan.ExecutionPlan{}, "", nil, errors.New("launchd service has no ProgramArguments")
	}
	arguments := make([]string, len(argumentsValue))
	for index, item := range argumentsValue {
		argument, ok := item.(string)
		if !ok {
			return plan.ExecutionPlan{}, "", nil, errors.New("launchd ProgramArguments must contain only strings")
		}
		arguments[index] = argument
	}
	if programValue, exists := dictionary["Program"]; exists {
		program, valid := programValue.(string)
		if !valid || program == "" {
			return plan.ExecutionPlan{}, "", nil, errors.New("launchd Program must be a non-empty string")
		}
		if program != arguments[0] {
			return plan.ExecutionPlan{}, "", nil, errors.New("launchd Program must match ProgramArguments[0]")
		}
	}
	if runAtLoad, ok := dictionary["RunAtLoad"].(bool); !ok || !runAtLoad {
		return plan.ExecutionPlan{}, "", nil, errors.New("launchd service must use RunAtLoad=true")
	}
	restart := ""
	demandPath := ""
	switch keepAlive := dictionary["KeepAlive"].(type) {
	case map[string]any:
		if len(keepAlive) == 1 {
			if pathState, ok := keepAlive["PathState"].(map[string]any); ok && len(pathState) == 1 {
				for path, raw := range pathState {
					active, valid := raw.(bool)
					if valid && active {
						restart = "always"
						demandPath = path
					}
				}
			}
		}
	case bool:
		if !keepAlive {
			return plan.ExecutionPlan{}, "", nil, errors.New("launchd service does not guarantee failure recovery")
		}
		return plan.ExecutionPlan{}, "", nil, errors.New("launchd boolean KeepAlive cannot map to Tunlite's controlled active demand")
	}
	if restart == "" {
		return plan.ExecutionPlan{}, "", nil, errors.New("launchd service does not guarantee controlled keep-up recovery")
	}
	restartDelay := 10
	if throttle, ok := dictionary["ThrottleInterval"]; ok {
		value, valid := throttle.(string)
		parsed, parseErr := strconv.Atoi(value)
		if !valid || parseErr != nil || parsed < 0 {
			return plan.ExecutionPlan{}, "", nil, errors.New("launchd throttle interval is invalid")
		}
		restartDelay = parsed
	}
	stopTimeout := 5
	if exitTimeout, ok := dictionary["ExitTimeOut"]; ok {
		value, valid := exitTimeout.(string)
		parsed, parseErr := strconv.Atoi(value)
		if !valid || parseErr != nil || parsed <= 0 {
			return plan.ExecutionPlan{}, "", nil, errors.New("launchd exit timeout is invalid")
		}
		stopTimeout = parsed
	}
	if processType, ok := dictionary["ProcessType"]; ok {
		if value, valid := processType.(string); !valid || value != "Background" {
			return plan.ExecutionPlan{}, "", nil, errors.New("launchd process type cannot map to the Tunlite standard")
		}
	}
	value := plan.ExecutionPlan{
		Mode:      plan.ModeDirect,
		Exec:      plan.Exec{Program: arguments[0], Args: arguments[1:], Environment: map[string]string{}},
		Lifecycle: plan.Lifecycle{Restart: restart, RestartDelaySeconds: restartDelay, StopTimeoutSeconds: stopTimeout},
		Endpoints: []plan.Endpoint{}, HealthChecks: []plan.HealthCheck{}, Redactions: []plan.Redaction{},
	}
	if err := plan.Validate(value); err != nil {
		return plan.ExecutionPlan{}, "", nil, err
	}
	label, _ := dictionary["Label"].(string)
	stdout, stdoutOK := dictionary["StandardOutPath"].(string)
	stderr, stderrOK := dictionary["StandardErrorPath"].(string)
	if label == "" {
		return plan.ExecutionPlan{}, "", nil, errors.New("launchd service has no stable label")
	}
	if !stdoutOK || !stderrOK || strings.TrimSpace(stdout) == "" || strings.TrimSpace(stderr) == "" {
		return plan.ExecutionPlan{}, "", nil, errors.New("launchd service requires exactly one non-empty stdout path and one non-empty stderr path")
	}
	logs := []string{stdout, stderr}
	connection := strings.TrimPrefix(label, "dev.tunlite.connection.")
	if err := validateLaunchdDemandIdentity(connection, actualScope); err != nil {
		return plan.ExecutionPlan{}, "", nil, err
	}
	if controlledLogDir == "" {
		controlledLogDir = path.Dir(stdout)
	}
	expectedDemandPath := launchdDemandPath(controlledLogDir, Artifact{Connection: connection, Scope: actualScope})
	if demandPath != expectedDemandPath {
		return plan.ExecutionPlan{}, "", nil, fmt.Errorf("launchd PathState demand %q is not Tunlite's controlled marker %q", demandPath, expectedDemandPath)
	}
	return value, label, logs, nil
}

func decodeLaunchdDictionary(raw []byte) (map[string]any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var root any
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "dict" {
			continue
		}
		root, err = decodePlistValue(decoder, start)
		if err != nil {
			return nil, err
		}
		break
	}
	dictionary, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("launchd plist has no root dictionary")
	}
	return dictionary, nil
}

func decodePlistValue(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		result := map[string]any{}
		key := ""
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			switch item := token.(type) {
			case xml.EndElement:
				if item.Name.Local == "dict" {
					return result, nil
				}
			case xml.StartElement:
				if item.Name.Local == "key" {
					if err := decoder.DecodeElement(&key, &item); err != nil {
						return nil, err
					}
					continue
				}
				if key == "" {
					return nil, errors.New("plist dictionary value has no key")
				}
				value, err := decodePlistValue(decoder, item)
				if err != nil {
					return nil, err
				}
				if _, exists := result[key]; exists {
					return nil, fmt.Errorf("launchd plist contains duplicate key %q", key)
				}
				result[key], key = value, ""
			}
		}
	case "array":
		result := []any{}
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			switch item := token.(type) {
			case xml.EndElement:
				if item.Name.Local == "array" {
					return result, nil
				}
			case xml.StartElement:
				value, err := decodePlistValue(decoder, item)
				if err != nil {
					return nil, err
				}
				result = append(result, value)
			}
		}
	case "string", "integer":
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return nil, err
		}
		return value, nil
	case "true":
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return true, nil
	case "false":
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return false, nil
	default:
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

func (l *Launchd) run(ctx context.Context, args ...string) ([]byte, error) {
	return l.runner.Run(ctx, launchctlProgram, args, Streams{})
}

func (l *Launchd) runMutation(ctx context.Context, scope Scope, args ...string) ([]byte, error) {
	if NormalizeScope(scope) == ScopeUser {
		return l.run(ctx, args...)
	}
	commandArgs := append([]string{"-n", "--", launchctlProgram}, args...)
	output, err := l.runner.Run(ctx, MacOSSudoProgram, commandArgs, Streams{})
	if err != nil {
		return output, &PrivilegeError{Operation: "manage system launchd service", Cause: err}
	}
	return output, nil
}

func (l *Launchd) domain(scope Scope) string {
	if NormalizeScope(scope) == ScopeSystem {
		return "system"
	}
	return "gui/" + strconv.Itoa(l.uid)
}

func (l *Launchd) domainTarget(scope Scope, label string) string {
	return l.domain(scope) + "/" + label
}

func parseLaunchdProperties(output []byte) map[string]string {
	result := make(map[string]string)
	for _, raw := range bytes.Split(output, []byte{'\n'}) {
		line := strings.TrimSpace(string(raw))
		key, value, found := strings.Cut(line, " = ")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "state":
			result["state"] = value
		case "pid":
			result["pid"] = value
		case "path":
			result["path"] = value
		case "program":
			result["program"] = value
		case "last exit code":
			result["lastExitCode"] = value
		case "runs":
			result["runs"] = value
		case "execs":
			result["execs"] = value
		}
	}
	return result
}
