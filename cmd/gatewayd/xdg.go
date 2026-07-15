package main

import (
	"os"
	"path/filepath"
)

const appName = "ai-debug-gateway"

// dirs is the daemon's resolved filesystem locations.
type dirs struct {
	Config string
	Data   string
}

// resolveXDGDirs resolves the config and data directories, creating
// them with owner-only (0700) permissions if missing.
func resolveXDGDirs() (dirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return dirs{}, err
	}

	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}
	dataBase := os.Getenv("XDG_DATA_HOME")
	if dataBase == "" {
		dataBase = filepath.Join(home, ".local", "share")
	}

	d := dirs{
		Config: filepath.Join(configBase, appName),
		Data:   filepath.Join(dataBase, appName),
	}
	for _, dir := range []string{d.Config, d.Data} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return dirs{}, err
		}
	}
	return d, nil
}
