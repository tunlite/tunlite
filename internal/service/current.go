package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

func NewCurrent(runner CommandRunner) (Adapter, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return newCurrent(runtime.GOOS, home, runner, exec.LookPath, user.Current, currentUID())
}

func newCurrent(
	goos, home string,
	runner CommandRunner,
	lookPath func(string) (string, error),
	currentUser func() (*user.User, error),
	uid int,
) (Adapter, error) {
	switch goos {
	case "linux":
		if _, err := lookPath("systemctl"); err != nil {
			return nil, errors.New("systemd user services are unavailable; use `tunlite run` on this platform")
		}
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		} else if !filepath.IsAbs(base) {
			return nil, errors.New("XDG_CONFIG_HOME must be an absolute path")
		}
		if err := probeServiceManager(runner, "systemctl", []string{"--user", "show-environment"}); err != nil {
			return nil, fmt.Errorf("systemd user services are unavailable: %w; use `tunlite run` on this platform", err)
		}
		return NewSystemd(filepath.Join(base, "systemd", "user"), runner), nil
	case "darwin":
		if _, err := lookPath(launchctlProgram); err != nil {
			return nil, errors.New("launchd services are unavailable; use `tunlite run` on this platform")
		}
		systemErr := probeServiceManager(runner, launchctlProgram, []string{"print", "system"})
		userErr := probeServiceManager(runner, launchctlProgram, []string{"print", "gui/" + strconv.Itoa(uid)})
		if systemErr != nil && userErr != nil {
			return nil, fmt.Errorf("launchd services are unavailable (system: %v; user: %v); use `tunlite run` on this platform", systemErr, userErr)
		}
		userName := ""
		resolvedUser, err := currentUser()
		if err == nil && resolvedUser != nil && resolvedUser.Username != "root" && uid > 0 {
			userName = resolvedUser.Username
		}
		adapter := NewLaunchdWithSystemScope(
			filepath.Join(home, "Library", "LaunchAgents"),
			"/Library/LaunchDaemons",
			filepath.Join(home, "Library", "Logs", "Tunlite"),
			uid, userName, runner,
		)
		adapter.setDomainAvailability(userErr == nil, systemErr == nil)
		return adapter, nil
	default:
		return nil, errors.New("native background services are supported on Linux and macOS; use `tunlite run` on this platform")
	}
}

func probeServiceManager(runner CommandRunner, command string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := runner.Run(ctx, command, args, Streams{})
	return err
}
