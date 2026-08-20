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

// LegacyConfigFile is the read-only discovery location used by the 0.x to v1
// installer. v1 never uses this path as its live declaration store.
func LegacyConfigFile() (string, error) {
	if root := os.Getenv("TUNLITE_HOME"); root != "" {
		return filepath.Join(root, "config", "config.json"), nil
	}
	base, err := configBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName, "config.json"), nil
}

// LegacyStateDir identifies 0.x installation metadata and logs. Upgrade code
// may inspect exact owned files here, but v1 runtime state always lives below
// StateDir's versioned directory.
func LegacyStateDir() (string, error) {
	if root := os.Getenv("TUNLITE_HOME"); root != "" {
		return filepath.Join(root, "data"), nil
	}
	base, err := stateBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}

func configBase() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("APPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return base, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config"), nil
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
	}
	return base, nil
}

func stateBase() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		return base, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state"), nil
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("XDG_STATE_HOME must be an absolute path")
	}
	return base, nil
}
