// Package profile persists board profiles: UART line settings and a
// stable USB identity. Profiles never store passwords or private-key
// passphrases; SSH configuration is added in a later phase.
package profile

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport/serial"
)

// UARTConfig is a profile's persistent UART configuration.
type UARTConfig struct {
	Identity transport.Identity
	Line     serial.LineSettings
}

// Profile is one board's saved configuration.
type Profile struct {
	Name string
	UART *UARTConfig
}

// Save validates p and atomically writes it as 0600 JSON to
// dir/<name>.json, replacing any existing file of the same name.
func Save(dir string, p Profile) error {
	if p.UART != nil {
		if err := p.UART.Line.Validate(); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".profile-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, filepath.Join(dir, p.Name+".json"))
}

// Load reads and decodes the profile named name from dir.
func Load(dir, name string) (Profile, error) {
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return Profile{}, err
	}
	return p, nil
}
