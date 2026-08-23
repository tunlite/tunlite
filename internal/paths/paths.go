package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const appName = "tunlite"

func ConfigFile() (string, error) {
	if root := os.Getenv("TUNLITE_HOME"); root != "" {
		return filepath.Join(root, "config", "connections.json"), nil
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("APPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, appName, "connections.json"), nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	} else if !filepath.IsAbs(base) {
		return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
	}
	return filepath.Join(base, appName, "connections.json"), nil
}

func StateDir() (string, error) {
	if root := os.Getenv("TUNLITE_HOME"); root != "" {
		return filepath.Join(root, "state", "core"), nil
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, appName, "state", "core"), nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	} else if !filepath.IsAbs(base) {
		return "", errors.New("XDG_STATE_HOME must be an absolute path")
	}
	return filepath.Join(base, appName, "core"), nil
}
