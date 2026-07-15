package policy

import (
	"encoding/json"
	"fmt"
	"os"
)

// unsafeShellFileWire is the on-disk board unsafe-shell denylist format. It
// has no allow concept: unlike the LoadFile board policy, this file can
// only narrow what DenylistPolicy already permits, never widen it.
type unsafeShellFileWire struct {
	RiskAccepted    bool           `json:"risk_accepted"`
	DenyExecutables []string       `json:"deny_executables"`
	DenyExact       []argvRuleWire `json:"deny_exact"`
}

// LoadUnsafeShellFile reads an owner-only board unsafe-shell denylist and
// returns a DenylistPolicy built from it. risk_accepted must be true --
// its absence or falsity is refused with a clear error, requiring the
// operator to state, per board, that they accept this mode's risk (see
// docs/superpowers/specs/2026-07-15-auto-shell-denylist-design.md).
func LoadUnsafeShellFile(path string) (*DenylistPolicy, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("unsafe-shell file: stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe-shell file: must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("unsafe-shell file: permissions must be exactly 0600, got %04o", info.Mode().Perm())
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("unsafe-shell file: open: %w", err)
	}
	defer f.Close()
	var wire unsafeShellFileWire
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("unsafe-shell file: invalid JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if !wire.RiskAccepted {
		return nil, fmt.Errorf("unsafe-shell file: risk_accepted must be true; the operator must explicitly accept this board's risk")
	}

	for _, name := range wire.DenyExecutables {
		if err := validateRule(ArgvRule{Executable: name}, false); err != nil {
			return nil, err
		}
	}

	denyExact := make([]ArgvRule, 0, len(wire.DenyExact))
	for _, entry := range wire.DenyExact {
		if entry.Args == nil {
			return nil, fmt.Errorf("unsafe-shell file: args must be an explicit JSON array")
		}
		rule := ArgvRule{Executable: entry.Executable, Args: *entry.Args}
		if err := validateRule(rule, false); err != nil {
			return nil, err
		}
		denyExact = append(denyExact, rule)
	}

	return Denylist(DenylistRules{
		DenyExecutables: wire.DenyExecutables,
		DenyExact:       denyExact,
	}), nil
}
