// Package xdgpaths resolves the shared config and data directories
// that gatewayd and gateway must agree on byte-for-byte, since gateway
// locates gatewayd's Unix domain sockets and profile files by
// re-deriving these same paths independently.
package xdgpaths

import (
	"os"
	"path/filepath"
)

const appName = "ai-debug-gateway"

// Dirs is the resolved set of filesystem locations.
type Dirs struct {
	Config string
	Data   string
}

// Resolve resolves the config and data directories, creating them
// with owner-only (0700) permissions if missing.
func Resolve() (Dirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, err
	}

	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}
	dataBase := os.Getenv("XDG_DATA_HOME")
	if dataBase == "" {
		dataBase = filepath.Join(home, ".local", "share")
	}

	d := Dirs{
		Config: filepath.Join(configBase, appName),
		Data:   filepath.Join(dataBase, appName),
	}
	for _, dir := range []string{d.Config, d.Data} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Dirs{}, err
		}
	}
	return d, nil
}
