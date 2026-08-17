package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		} else if !filepath.IsAbs(base) {
			return nil, errors.New("XDG_CONFIG_HOME must be an absolute path")
		}
		return NewSystemd(filepath.Join(base, "systemd", "user"), runner), nil
	case "darwin":
		return NewLaunchd(
			filepath.Join(home, "Library", "LaunchAgents"),
			filepath.Join(home, "Library", "Logs", "Tunlite"),
			currentUID(), runner,
		), nil
	default:
		return nil, errors.New("persistent services are supported on Linux and macOS; use `tunlite run` on this platform")
	}
}
