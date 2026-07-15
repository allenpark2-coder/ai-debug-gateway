# Task 1 report: Shell policy classifier

## Status

Implemented and verified the common deny-by-default shell policy classifier. No Ambarella or vendor-specific allow rules were added.

## Implementation

- Added `Decision`, `Policy`, `Common`, and `(*Policy).Evaluate`.
- Parses the complete input with `mvdan.cc/sh/v3/syntax` and rejects unsupported AST nodes, redirects, substitutions/expansions, assignments, background/coprocess execution, negation, functions, loops, conditionals, arithmetic, interpreters, and unknown executables.
- Allows only literal simple calls connected by `;`, `&&`, `||`, or ordinary pipelines, with every call independently classified.
- Added explicit command validators for the common diagnostic commands in the approved design.
- Added normalized sensitive-path denials for password databases, SSH keys, process environment/memory, unsafe device nodes, and gateway state.
- Added command-specific mutation denials, including `find` actions, setters in `hostname` and `date`, write-capable `ip` and `ethtool` forms, `mount -a`, clearing/console-changing `dmesg`, and write-capable `sed` programs.
- Added `mvdan.cc/sh/v3 v3.13.1`; its dependency graph upgrades `golang.org/x/term` to v0.41.0.

## TDD evidence

### RED

Initial required table:

```text
go test ./internal/policy -run TestCommonPolicy -v
internal/policy/policy_test.go:16:7: undefined: Common
FAIL
```

Subsequent adversarial RED runs caught and drove fixes for:

- BusyBox positional `date` setter;
- relative sensitive paths;
- `dmesg -c` and console-level mutation;
- `mount -a`;
- long and short `ethtool` setters;
- `hostname --file`;
- `ip route restore`;
- `sed -n` programs containing the `w` command.

### GREEN

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/policy ./internal/core/...
ok github.com/allenpark2-coder/ai-debug-gateway/internal/policy
ok github.com/allenpark2-coder/ai-debug-gateway/internal/core/...
```

Final full repository verification (run outside the restricted sandbox because existing tests bind Unix/TCP sockets):

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./...
all packages PASS, including internal/integration and internal/transport/ssh
```

## Files

- `internal/policy/policy.go`
- `internal/policy/common.go`
- `internal/policy/policy_test.go`
- `go.mod`
- `go.sum`

## Self-review

- `git diff --check` passes.
- The implementation remains parsing/classification only; it executes nothing.
- Unknown commands, options in security-sensitive validators, syntax, and mutating subcommands deny by default.
- No Ambarella, `opsis-*`, or vendor command rules exist.
- Only Task 1 source/module files are staged for commit; existing `.superpowers` working files remain outside the product commit.

## Concerns

- The allowlist is intentionally conservative. In particular, `sed -n` accepts numeric/`$` address print programs only, and some harmless platform-specific diagnostic options will be denied until explicitly tested and added.
- `mvdan.cc/sh/v3@latest` currently requires the repository's `golang.org/x/term` selection to move from v0.40.0 to v0.41.0.

## Code-review security fixes

### Findings addressed

- Replaced the shared `optionsOnly` validator with exact, per-command option allowlists. `ps -eE`, `ps --everything`, and all unrecognized process-reporting options now deny, preventing environment-output modifiers.
- Replaced unrestricted `printenv` with a safe-name allowlist (`PATH`, `LANG`, `LC_ALL`, `TERM`, `SHELL`, `USER`, `LOGNAME`, `HOSTNAME`, and `TZ`). A zero-argument full dump and all other names deny.
- Replaced the generic path command validator with command-specific option grammars. Unknown options and path-consuming forms such as both spellings of `wc --files0-from` deny.
- Replaced the `find` mutation blacklist with an explicit grammar for global traversal flags, boolean operators, safe tests, and output-only actions. Unknown predicates/actions deny.
- Restricted supported `ethtool` queries to exactly an option plus one interface operand; trailing arguments deny.
- Narrowed `type` option handling and added systematic unknown-option, secret-output, safe-positive, and path-option tests.

### Review-fix RED evidence

Command:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/policy -run 'TestCommonPolicyRejectsUnknownOptionsAndSecretOutput|TestCommonPolicyAllowsExactDiagnosticForms' -v
```

The first run failed on the intended vulnerable cases, including `ps -eE`, unrestricted `printenv`, unknown path-command options, `wc --files0-from=/etc/shadow`, unknown `find` predicates/actions, trailing `ethtool` argv, and `df -h /` missing from the prior positive grammar. A second RED cycle failed on arbitrary `type` options and the missing safe `find -L` global flag.

### Review-fix GREEN evidence

Focused/core command and output:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/policy ./internal/core/...
ok github.com/allenpark2-coder/ai-debug-gateway/internal/policy 0.028s
ok github.com/allenpark2-coder/ai-debug-gateway/internal/core/audit (cached)
ok github.com/allenpark2-coder/ai-debug-gateway/internal/core/command (cached)
ok github.com/allenpark2-coder/ai-debug-gateway/internal/core/framing (cached)
ok github.com/allenpark2-coder/ai-debug-gateway/internal/core/id (cached)
ok github.com/allenpark2-coder/ai-debug-gateway/internal/core/secret (cached)
ok github.com/allenpark2-coder/ai-debug-gateway/internal/core/session (cached)
ok github.com/allenpark2-coder/ai-debug-gateway/internal/core/transcript (cached)
```

Full-suite command:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./...
```

Output: every repository package reported `ok`, including `internal/integration`, `internal/ipc`, and `internal/transport/ssh`; exit status 0. The command ran outside the restricted sandbox because existing tests bind Unix/TCP sockets.

### Review-fix concerns

- Exact option grammars intentionally reject harmless unlisted platform-specific flags and combined short-option spellings. They can be added only with focused tests and documented read-only semantics.
- The safe `find` grammar is deliberately small and excludes `-printf`, `-newer`, and every command-execution or file-output action.

## Remaining Important security findings

### Findings addressed

- Reject `/proc/{self,thread-self,numeric PID}/root` aliases before lexical path cleaning, including traversal forms such as `/proc/self/root/../etc/shadow` where kernel symlink resolution differs from `path.Clean`.
- Deny `/proc/{self,thread-self,numeric PID}/fd` aliases because their targets cannot be classified from the path text. Other ordinary procfs diagnostics, including `/proc/self/exe`, remain allowed.
- Generalize SSH host private-key recognition to every `ssh_host_*_key` basename, including DSA and legacy `ssh_host_key`, while allowing corresponding `.pub` files.

### Remaining-findings RED evidence

Focused command:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/policy -run 'TestCommonPolicyAdversarialForms|TestCommonPolicyAllowsExactDiagnosticForms' -count=1 -v
```

The first run failed on all new private cases: procfs root aliases for self, thread-self, and numeric PIDs; procfs fd aliases; `ssh_host_dsa_key`; and legacy `ssh_host_key`. Both `.pub` positive controls passed. A second RED cycle specifically demonstrated that `/proc/self/root/../etc/shadow` bypassed suffix reclassification after lexical cleaning.

### Remaining-findings GREEN evidence

Focused regressions and positive controls:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/policy -run 'TestCommonPolicyAdversarialForms|TestCommonPolicyAllowsExactDiagnosticForms' -count=1
ok github.com/allenpark2-coder/ai-debug-gateway/internal/policy
```

Required policy/core suite:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/policy ./internal/core/...
all packages PASS; exit status 0
```

Full repository suite:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./...
all packages PASS, including internal/integration and internal/transport/ssh; exit status 0
```

### Remaining-findings concerns

- Procfs process `root` and `fd` aliases are intentionally denied as opaque alias boundaries, even when a particular invocation might resolve to a non-sensitive target. This preserves deny-by-default behavior without relying on runtime filesystem state.

## Final pseudo-filesystem review fixes

### Findings addressed

- Replaced procfs/sysfs/devfs blacklist handling with a separate explicit pseudo-filesystem classifier. Direct reads allow only exact global procfs facts: `cpuinfo`, `meminfo`, `uptime`, `loadavg`, `version`, `filesystems`, and `mounts`.
- Deny every per-process procfs operand and every other procfs node, including `/proc/kcore`; deny all direct `/sys` and `/dev` operands by default.
- Allow user SSH public keys matching `.ssh/id_*.pub` while continuing to deny the corresponding private keys and `authorized_keys`.
- Added a central informational-form validator that permits exactly one `--help` or `--version` argument for already allowlisted commands. Extra operands still go through the command-specific grammar and deny.

### Final-review RED evidence

Command:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/policy -run 'TestCommonPolicy(AdversarialForms|AllowsExactDiagnosticForms)$' -v
```

The intended failures were observed for `/proc/kcore`, `/proc/123/status`, an arbitrary `/proc/sys` node, an arbitrary `/sys` node, `/dev/null`, `.ssh/id_ed25519.pub`, and exact `ps --help`, `cat --version`, and `ip --help` forms.

### Final-review GREEN evidence

Focused policy/core suite:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/policy ./internal/core/...
all packages PASS; exit status 0
```

Full repository suite:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./...
all packages PASS; exit status 0
```

### Final-review concerns

- The pseudo-filesystem allowlist is deliberately narrow. Safe platform-specific procfs/sysfs facts require a focused positive test and explicit addition; no pattern-based or per-process procfs reads are currently supported.
