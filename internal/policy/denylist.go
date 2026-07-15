package policy

import (
	"fmt"
	"path"
	"strings"
)

// hardDenyInterpreters lists executables that can run arbitrary,
// dynamically-supplied code. No board denylist configuration can remove or
// weaken these: a denylist that blocks "rm" but allows "sh -c 'rm -rf /'"
// blocks nothing.
var hardDenyInterpreters = map[string]struct{}{
	"sh": {}, "bash": {}, "dash": {}, "ash": {}, "zsh": {}, "ksh": {}, "fish": {}, "csh": {}, "tcsh": {},
	"python": {}, "python2": {}, "python3": {}, "perl": {}, "perl5": {}, "ruby": {}, "lua": {}, "node": {},
	"nodejs": {}, "php": {}, "tclsh": {}, "wish": {}, "expect": {}, "busybox": {},
}

// hardDenyReason reports why argv is unconditionally denied in denylist
// mode, independent of any board configuration, or "" if no hard rule
// applies. This covers interpreters, eval, and the indirect-execution
// forms (exec, env, xargs, find -exec/-execdir/-ok/-okdir) that would
// otherwise let a denied executable run under a name the board denylist
// does not mention.
func hardDenyReason(argv []string) string {
	base := strings.ToLower(path.Base(argv[0]))
	if _, found := hardDenyInterpreters[base]; found {
		return "interpreter and shell invocation is never allowed in denylist mode"
	}
	switch base {
	case "eval":
		return "eval is never allowed in denylist mode"
	case "exec":
		return "exec is never allowed in denylist mode"
	case "env":
		return "env is never allowed in denylist mode"
	case "xargs":
		return "xargs is never allowed in denylist mode"
	case "find":
		for _, arg := range argv[1:] {
			if arg == "-exec" || arg == "-execdir" || arg == "-ok" || arg == "-okdir" {
				return "find -exec, -execdir, -ok, and -okdir are never allowed in denylist mode"
			}
		}
	}
	return ""
}

// DenylistRules is the in-memory, already-validated board configuration for
// DenylistPolicy. Loading and validating it from a file is a separate
// concern (LoadUnsafeShellFile).
type DenylistRules struct {
	DenyExecutables []string
	DenyExact       []ArgvRule
}

// DenylistPolicy allows every syntactically valid simple command unless it
// is hard-denied, matches a board DenyExecutables entry, matches a board
// DenyExact entry, or touches a sensitive path. It is a distinct type from
// Policy on purpose: it must never be constructible from, or convertible
// into, the allowlist Common() uses, so a bug in one cannot silently grant
// the other's behavior.
type DenylistPolicy struct {
	denyExecutables map[string]struct{}
	denyExact       map[string]map[string]struct{}
}

// Denylist builds a DenylistPolicy from already-validated rules.
func Denylist(rules DenylistRules) *DenylistPolicy {
	denyExecutables := make(map[string]struct{}, len(rules.DenyExecutables))
	for _, name := range rules.DenyExecutables {
		denyExecutables[name] = struct{}{}
	}
	denyExact := make(map[string]map[string]struct{})
	for _, rule := range rules.DenyExact {
		if denyExact[rule.Executable] == nil {
			denyExact[rule.Executable] = make(map[string]struct{})
		}
		denyExact[rule.Executable][argvKey(rule.Args)] = struct{}{}
	}
	return &DenylistPolicy{denyExecutables: denyExecutables, denyExact: denyExact}
}

// Evaluate implements the same contract as (*Policy).Evaluate.
func (p *DenylistPolicy) Evaluate(text string) Decision {
	return evaluateShellLine(text, walkOptions{
		allowRedirection: true,
		classify:         p.classifyArgv,
	})
}

func (p *DenylistPolicy) classifyArgv(argv []string) Decision {
	if len(argv) == 0 {
		return deny("command.empty", "empty commands are not allowed")
	}
	if reason := hardDenyReason(argv); reason != "" {
		return deny("denylist.hard", reason)
	}
	for _, arg := range argv[1:] {
		if reason := unsafePath(arg); reason != "" {
			return deny("path.sensitive", reason)
		}
	}
	exe := argv[0]
	if _, found := p.denyExecutables[exe]; found {
		return deny("denylist.executable", fmt.Sprintf("executable %q is denied by the board denylist", exe))
	}
	if set, found := p.denyExact[exe]; found {
		if _, found := set[argvKey(argv[1:])]; found {
			return deny("denylist.exact", fmt.Sprintf("this exact invocation of %q is denied by the board denylist", exe))
		}
	}
	return allow("denylist.allow")
}
