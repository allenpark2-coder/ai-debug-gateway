package policy

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// ArgvRule identifies one command invocation. Executable and Args are matched
// literally; no globbing, option interpretation, or prefix matching occurs.
type ArgvRule struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

// File is the on-disk board policy format.
type File struct {
	Allow []ArgvRule `json:"allow"`
	Deny  []ArgvRule `json:"deny"`
}

type argvRuleWire struct {
	Executable string    `json:"executable"`
	Args       *[]string `json:"args"`
}

type fileWire struct {
	Allow []argvRuleWire `json:"allow"`
	Deny  []argvRuleWire `json:"deny"`
}

// LoadFile reads an owner-only board policy and returns a copy of base with
// its exact deny and allow rules applied. The supplied base is never mutated.
func LoadFile(path string, base *Policy) (*Policy, error) {
	if base == nil {
		return nil, fmt.Errorf("policy file: base policy is nil")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("policy file: stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("policy file: must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("policy file: permissions must be exactly 0600, got %04o", info.Mode().Perm())
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("policy file: open: %w", err)
	}
	defer f.Close()
	var wire fileWire
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("policy file: invalid JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	rules, err := materializeRules(wire)
	if err != nil {
		return nil, err
	}

	denied := make(map[string]map[string]struct{})
	allowed := make(map[string]map[string]struct{})
	for _, rule := range rules.Deny {
		if err := validateRule(rule, false); err != nil {
			return nil, err
		}
		addExactRule(denied, rule)
	}
	for _, rule := range rules.Allow {
		if err := validateRule(rule, true); err != nil {
			return nil, err
		}
		key := argvKey(rule.Args)
		if _, exists := denied[rule.Executable][key]; exists {
			return nil, fmt.Errorf("policy file: contradictory allow and deny rule for %q", rule.Executable)
		}
		addExactRule(allowed, rule)
	}

	result := &Policy{commands: make(map[string]validator, len(base.commands)+len(allowed))}
	for executable, validate := range base.commands {
		result.commands[executable] = validate
	}
	for executable := range denied {
		wrapExactRules(result, executable, allowed[executable], denied[executable])
	}
	for executable := range allowed {
		if _, alreadyWrapped := denied[executable]; !alreadyWrapped {
			wrapExactRules(result, executable, allowed[executable], nil)
		}
	}
	return result, nil
}

func materializeRules(wire fileWire) (File, error) {
	convert := func(entries []argvRuleWire) ([]ArgvRule, error) {
		result := make([]ArgvRule, 0, len(entries))
		for _, entry := range entries {
			if entry.Args == nil {
				return nil, fmt.Errorf("policy file: args must be an explicit JSON array")
			}
			result = append(result, ArgvRule{Executable: entry.Executable, Args: *entry.Args})
		}
		return result, nil
	}
	allow, err := convert(wire.Allow)
	if err != nil {
		return File{}, err
	}
	deny, err := convert(wire.Deny)
	if err != nil {
		return File{}, err
	}
	return File{Allow: allow, Deny: deny}, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("policy file: invalid JSON: multiple values")
		}
		return fmt.Errorf("policy file: invalid JSON: %w", err)
	}
	return nil
}

func addExactRule(set map[string]map[string]struct{}, rule ArgvRule) {
	if set[rule.Executable] == nil {
		set[rule.Executable] = make(map[string]struct{})
	}
	set[rule.Executable][argvKey(rule.Args)] = struct{}{}
}

func wrapExactRules(result *Policy, executable string, allowed, denied map[string]struct{}) {
	baseValidate, existed := result.commands[executable]
	result.commands[executable] = func(argv []string) Decision {
		key := argvKey(argv[1:])
		if _, found := denied[key]; found {
			return deny("file.deny", "command is explicitly denied by the board policy")
		}
		if existed {
			return baseValidate(argv)
		}
		if _, found := allowed[key]; found {
			return allow("file.allow")
		}
		return deny("command.unknown", fmt.Sprintf("executable %q is not allowlisted", executable))
	}
}

func argvKey(args []string) string {
	data, _ := json.Marshal(args)
	return string(data)
}

func validateRule(rule ArgvRule, allowing bool) error {
	if rule.Executable == "" {
		return fmt.Errorf("policy file: executable is required (empty argv)")
	}
	if strings.ContainsAny(rule.Executable, "*?[]{};&|<>$`\\/\"'") || strings.IndexFunc(rule.Executable, unicode.IsSpace) >= 0 {
		return fmt.Errorf("policy file: executable %q must be a literal basename", rule.Executable)
	}
	for _, arg := range rule.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("policy file: arguments must not contain NUL")
		}
	}
	if allowing && forbiddenExecutable(rule.Executable) {
		return fmt.Errorf("policy file: forbidden interpreter or mutating executable %q cannot be allowed", rule.Executable)
	}
	return nil
}

func forbiddenExecutable(name string) bool {
	switch strings.ToLower(name) {
	case "sh", "bash", "dash", "ash", "zsh", "ksh", "fish", "python", "python3", "perl", "ruby", "lua", "node",
		"rm", "mv", "cp", "install", "dd", "tee", "touch", "mkdir", "rmdir", "chmod", "chown", "chgrp", "ln",
		"kill", "killall", "pkill", "reboot", "poweroff", "halt", "shutdown", "mount", "umount",
		"systemctl", "service", "modprobe", "insmod", "rmmod", "sysctl", "ip", "ifconfig", "route", "ethtool",
		"apt", "apt-get", "dpkg", "rpm", "yum", "dnf", "opkg", "apk", "sed", "awk", "find", "xargs":
		return true
	default:
		return false
	}
}
