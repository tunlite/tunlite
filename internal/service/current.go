package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err != nil {
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
		if _, err := exec.LookPath("launchctl"); err != nil {
			return nil, errors.New("launchd user services are unavailable; use `tunlite run` on this platform")
		}
		uid := currentUID()
		if err := probeServiceManager(runner, "launchctl", []string{"print", "gui/" + strconv.Itoa(uid)}); err != nil {
			return nil, fmt.Errorf("launchd user services are unavailable: %w; use `tunlite run` on this platform", err)
		}
		return NewLaunchd(
			filepath.Join(home, "Library", "LaunchAgents"),
			filepath.Join(home, "Library", "Logs", "Tunlite"),
			uid, runner,
		), nil
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
