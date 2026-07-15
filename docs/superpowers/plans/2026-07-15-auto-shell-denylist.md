# Auto Shell (Denylist) Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a second, independently opt-in gateway mode (`--unsafe-auto-shell=<board>`) that lets an AI client run any shell command the target's BusyBox/Linux environment supports without interactive approval, subject only to a small non-configurable set of structural denials and an explicit board-scoped denylist -- without touching any guarantee `--auto-readonly` makes.

**Architecture:** `internal/policy` gains a `Denylist` constructor implementing the same `Evaluate(text string) Decision` contract `Common()` does, built over a shell-line parsing contract extracted so both share it. A new owner-only `gatewayd.unsafeshell.sock` carries a new `RoleUnsafeShell` IPC role with exactly one atomic operation, `unsafeshell.execute`, reusing the coordinator's existing proposal/transaction/marker/`WaitResult` lifecycle already built for `diagnose.execute`. `RoleDiagnose` and `RoleUnsafeShell` are disjoint roles on disjoint sockets driven by disjoint policy files; neither daemon flag implies or weakens the other.

**Tech Stack:** Go 1.25, `mvdan.cc/sh/v3/syntax` (already a dependency), Unix domain sockets, existing v1 JSON protocol, Go unit/integration tests with Linux PTYs.

**Prerequisite:** This plan assumes `feat/auto-readonly-agent` (or its equivalent: `internal/policy`, `RoleDiagnose`, `diagnose.execute`, `Coordinator.WaitResult`) is already merged. Do not start Task 1 against a tree that lacks it -- rebase onto that branch first.

## Global Constraints

- `--auto-readonly` and its socket, role, policy, and tests must not change behavior when this mode is enabled, disabled, or enabled alongside it. Any diff that touches `internal/policy/common.go`'s existing rules, `RoleDiagnose`'s allowlist, or `diagnose.execute` semantics is out of scope for this plan.
- `RoleUnsafeShell`'s allowlist contains only `session.status`, `output.read`, and `unsafeshell.execute` -- never approve, raw write, retry, takeover, secret, session start/end, or host-key acceptance.
- The hard denial list (interpreters, `eval`, command/process substitution, indirect execution, background execution) is compiled into the binary and cannot be overridden, weakened, or removed by any board file.
- Sensitive-path denials (`/etc/shadow`, private keys, `/proc/*/environ`, secret-bearing state, gateway state) apply identically to this mode; do not special-case them.
- The board unsafe-shell file must set `"risk_accepted": true` or the daemon must refuse to start that socket, with a clear error, while leaving manual mode and `--auto-readonly` unaffected.
- Policy validation and transaction creation remain atomic inside one daemon request, exactly as in `diagnose.execute`; no editable pending proposal may exist between classification and execution.
- Preserve the user's untracked `internal/integration/security_test.go`; do not add or modify unrelated files.

---

### Task 1: Denylist shell classifier

**Files:**
- Create: `internal/policy/shellparse.go` (extract the shared parse contract from `policy.go`)
- Create: `internal/policy/denylist.go`
- Test: `internal/policy/denylist_test.go`
- Modify: `internal/policy/policy.go` (switch `Common()` to call the extracted parser)
- Test: `internal/policy/policy_test.go` (must still pass unmodified in behavior)

**Interfaces:**
- Produces: `func parseShellLine(text string) (*syntax.File, error)` -- one physical line, no NUL, balanced quotes, no heredoc; shared by both policies.
- Produces: `type DenylistRules struct { DenyExecutables []string; DenyExact []ArgvRule }` and `func Denylist(rules DenylistRules) *Policy` (or a distinct type implementing the same `Evaluate` method set as `*Policy`; keep the coordinator/dispatcher code policy-shape-agnostic).
- Uses: `mvdan.cc/sh/v3/syntax`, `ArgvRule` from Task 2 of the auto-readonly-agent plan.

- [ ] **Step 1: Add failing tests for the shared parse extraction**

Run: `go test ./internal/policy -run TestCommonPolicy -v` before touching anything, to record the current green baseline for `Common()`.

- [ ] **Step 2: Extract `parseShellLine` and re-point `Common()` at it**

Move the "one physical line, no NUL, balanced quotes, no heredoc" checks out of `policy.go`'s `Evaluate` into `shellparse.go` as a standalone function returning a parsed `*syntax.File`. Re-run `TestCommonPolicy` to confirm no behavior changed.

- [ ] **Step 3: Add failing table-driven tests for the denylist classifier**

```go
func TestDenylistPolicy(t *testing.T) {
    p := Denylist(DenylistRules{DenyExecutables: []string{"mkfs.ext4"}})
    tests := []struct{ text string; allowed bool }{
        {`mount -o remount,rw /`, true},
        {`echo hi > /tmp/x`, true},
        {`ip link set eth0 down`, true},
        {`sh -c 'id'`, false},
        {`cat $(touch /tmp/pwn)`, false},
        {`eval 'rm -rf /'`, false},
        {`find / -exec rm {} \;`, false},
        {`env rm -rf /`, false},
        {`rm -rf / &`, false},
        {`mkfs.ext4 /dev/mtd0`, false},
        {`cat /etc/shadow`, false},
    }
    for _, tt := range tests {
        if got := p.Evaluate(tt.text); got.Allowed != tt.allowed {
            t.Errorf("Evaluate(%q) = %+v, allowed=%v", tt.text, got, tt.allowed)
        }
    }
}

func TestDenylistCannotOverrideHardDenials(t *testing.T) {
    p := Denylist(DenylistRules{}) // no board rules at all
    for _, text := range []string{`sh -c 'id'`, `eval 'ls'`, "`ls`", `env ls`} {
        if p.Evaluate(text).Allowed {
            t.Errorf("Evaluate(%q) must remain denied with no board rules", text)
        }
    }
}
```

- [ ] **Step 4: Run and verify red**

Run: `go test ./internal/policy -run 'Denylist' -v`

Expected: FAIL because `Denylist` does not exist.

- [ ] **Step 5: Implement the AST walker and hard denial set**

Reuse `parseShellLine`. Permit redirection, pipelines, and `;`/`&&`/`||` sequencing (unlike `Common()`, which rejects redirection outright). For every `CallExpr`, reject unconditionally if the executable is an interpreter, `eval`, `exec`, `env`, `xargs`, or a `find` invocation containing `-exec`; reject any AST node representing command/process substitution or a background list regardless of executable. Otherwise allow unless the executable matches `DenyExecutables` or an exact `DenyExact` entry, or the path operand touches a sensitive path (reuse the sensitive-path helper from `common.go`).

- [ ] **Step 6: Run full policy suite and commit**

Run: `go test ./internal/policy ./internal/core/...`

Expected: PASS, including the unchanged `TestCommonPolicy`.

```bash
git add internal/policy
git commit -m "feat: classify denylist-mode diagnostic commands"
```

---

### Task 2: Board unsafe-shell file loading

**Files:**
- Create: `internal/policy/unsafefile.go`
- Test: `internal/policy/unsafefile_test.go`
- Modify: `cmd/gatewayd/main.go`
- Test: `cmd/gatewayd/main_test.go`

**Interfaces:**
- Consumes: `Denylist(DenylistRules)` from Task 1.
- Produces: `type UnsafeShellFile struct { RiskAccepted bool; DenyExecutables []string; DenyExact []ArgvRule }` and `func LoadUnsafeShellFile(path string) (*Policy, error)`.

- [ ] **Step 1: Write failing tests for strict loading and the risk-acceptance gate**

```go
func TestLoadUnsafeShellFileRequiresRiskAccepted(t *testing.T) {
    // risk_accepted absent, and risk_accepted: false, both return an error.
}
func TestLoadUnsafeShellFileMergesDenials(t *testing.T) {
    // 0600 JSON with deny_executables and deny_exact produces a Policy
    // that rejects exactly those and nothing else beyond the hard set.
}
```

Also assert: group/world-readable file, malformed JSON, unknown fields, empty executable names, and an attempt to name a hard-denied executable in some future "allow" field (there is none -- confirm the schema has no allow path at all) all error or are structurally impossible.

- [ ] **Step 2: Run and verify red**

Run: `go test ./internal/policy -run TestLoadUnsafeShellFile -v`

Expected: FAIL because `LoadUnsafeShellFile` is undefined.

- [ ] **Step 3: Implement strict JSON decoding, permission checks, and the risk gate**

Mirror `LoadFile`'s `os.Stat` permission check (`0600` exactly) and `json.Decoder.DisallowUnknownFields`. Fail closed if `risk_accepted` is missing or `false`. The resulting schema has no allow-rule concept at all -- there is nothing to validate against "overriding a common denial" because there is no allow path, which is the structural guarantee that this file can only narrow, never widen.

- [ ] **Step 4: Add daemon startup wiring and tests**

Extend daemon option parsing (from the auto-readonly-agent plan's `daemonOptions`) with:

```go
type daemonOptions struct {
    AutoReadonly    bool
    UnsafeAutoShell string // board name, empty when disabled
}
```

Test that `--unsafe-auto-shell=board` loads
`$XDG_CONFIG_HOME/ai-debug-gateway/unsafe-shell/<board>.json` once at startup;
a missing or invalid file disables only the unsafe-shell socket with a clear
error; omitting the flag ignores the file entirely; no live reload occurs;
`--auto-readonly` and `--unsafe-auto-shell` can be combined or used
independently without either affecting the other's startup path.

- [ ] **Step 5: Run focused tests and commit**

Run: `go test ./internal/policy ./cmd/gatewayd`

Expected: PASS.

```bash
git add internal/policy cmd/gatewayd/main.go cmd/gatewayd/main_test.go
git commit -m "feat: load board unsafe-shell denylists"
```

---

### Task 3: Unsafe-shell protocol role and capability isolation

**Files:**
- Modify: `internal/protocol/v1/message.go`
- Test: `internal/protocol/v1/message_test.go`
- Modify: `internal/ipc/server_linux.go`
- Test: `internal/ipc/server_test.go`
- Modify: `internal/cli/control.go`
- Test: `internal/cli/control_test.go`

**Interfaces:**
- Produces: `v1.OpUnsafeShellExecute = "unsafeshell.execute"`.
- Produces: `ipc.RoleUnsafeShell` whose allowlist contains only `session.status`, `output.read`, and `unsafeshell.execute`.
- Produces: `cli.UnsafeShellRequest`, `cli.UnsafeShellResult`, and `func (c *Client) UnsafeShellExecute(req UnsafeShellRequest) (UnsafeShellResult, error)`.

- [ ] **Step 1: Write failing permission-matrix tests proving disjointness from `RoleDiagnose`**

```go
func TestRoleDisjointness(t *testing.T) {
    // RoleDiagnose cannot dispatch OpUnsafeShellExecute.
    // RoleUnsafeShell cannot dispatch OpDiagnoseExecute.
    // Neither can dispatch OpCommandApprove, OpTransportWrite, OpRetryUART,
    // OpRetrySSH, OpSecretBegin, OpSessionStart, OpSessionEnd, OpHostKeyAccept.
    // RoleControl and RoleAttach behavior is unchanged.
}
```

- [ ] **Step 2: Run and verify red**

Run: `go test ./internal/ipc -run Disjoint -v`

Expected: FAIL because `RoleUnsafeShell` and `OpUnsafeShellExecute` do not exist.

- [ ] **Step 3: Add the protocol constant, role allowlist, and typed client DTOs**

```go
type UnsafeShellRequest struct {
    SessionID string `json:"session_id"`
    Text      string `json:"text"`
    Purpose   string `json:"purpose"`
    TimeoutMS int64  `json:"timeout_ms"`
}
type UnsafeShellResult struct {
    Decision       policy.Decision      `json:"decision"`
    Transaction    *command.Transaction `json:"transaction,omitempty"`
    Result         *command.Result      `json:"result,omitempty"`
    TruncatedStart bool                 `json:"truncated_start"`
    TruncatedEnd   bool                 `json:"truncated_end"`
}
```

Keep the DTO field shape identical to `DiagnoseRequest`/`DiagnoseResult` on purpose: the two modes differ in policy and role, not in request/response shape.

- [ ] **Step 4: Run protocol, IPC, and CLI tests and commit**

Run: `go test ./internal/protocol/v1 ./internal/ipc ./internal/cli`

Expected: PASS.

```bash
git add internal/protocol/v1 internal/ipc internal/cli
git commit -m "feat: add isolated unsafe-shell IPC role"
```

---

### Task 4: Atomic unsafe-shell execution

**Files:**
- Modify: `cmd/gatewayd/dispatcher.go`
- Test: `cmd/gatewayd/dispatcher_test.go`

**Interfaces:**
- Consumes: `policy.Policy` (denylist-mode), `Coordinator.WaitResult` (already built for `diagnose.execute`), Task 3 DTOs.
- Produces: `dispatcher.unsafeShellExecute(payload json.RawMessage)`, guarded by its own dispatcher mutex, independent of `diagnosticMu`.

- [ ] **Step 1: Write failing dispatcher atomicity and rejection tests**

Use a fake stream to prove: a hard-denied or board-denied command writes zero bytes and creates no proposal; an allowed mutating command writes exactly one marked transaction; concurrent unsafe-shell requests yield one transaction and one busy error from the dedicated mutex; a concurrent `diagnose.execute` and `unsafeshell.execute` do not deadlock each other and both correctly serialize against the coordinator's single-active-transaction state; secret-active and non-READY sessions reject; output belongs only to the requesting transaction.

- [ ] **Step 2: Run and verify red**

Run: `go test ./cmd/gatewayd -run UnsafeShell -v`

Expected: FAIL because `unsafeShellExecute` does not exist.

- [ ] **Step 3: Implement `unsafeshell.execute` mirroring `diagnoseExecute`**

```go
func (d *dispatcher) unsafeShellExecute(payload json.RawMessage) (any, *v1.ProtocolError) {
    var p unsafeShellExecutePayload
    if err := json.Unmarshal(payload, &p); err != nil {
        return nil, badPayload(err)
    }
    if d.unsafeShellPolicy == nil {
        return nil, internalErr(fmt.Errorf("unsafe-shell mode is disabled"))
    }
    if !d.unsafeShellMu.TryLock() {
        return nil, badPayload(fmt.Errorf("unsafe-shell execution is busy"))
    }
    defer d.unsafeShellMu.Unlock()
    decision := d.unsafeShellPolicy.Evaluate(p.Text)
    // same propose/approve/WaitResult/audit/open-set sequence as diagnoseExecute
}
```

Validate `session_id`/`text`/`purpose` nonempty and `timeout_ms` in the same bound `diagnose.execute` enforces. Append `proposal`, `unsafe-shell-approval`, `transaction`, and `result` audit events (name the approval-kind audit event distinctly from `auto-readonly-approval` so audit logs can distinguish which automatic path approved a transaction).

- [ ] **Step 4: Run gateway and dispatcher tests with race detector and commit**

Run: `go test -race ./internal/gateway ./cmd/gatewayd`

Expected: PASS with zero race reports.

```bash
git add cmd/gatewayd/dispatcher.go cmd/gatewayd/dispatcher_test.go
git commit -m "feat: execute denylist-approved commands atomically"
```

---

### Task 5: Opt-in daemon socket and CLI workflow

**Files:**
- Modify: `cmd/gatewayd/main.go`
- Test: `cmd/gatewayd/main_test.go`
- Modify: `cmd/gateway/main.go`
- Test: `cmd/gateway/main_test.go`

**Interfaces:**
- Consumes: `ipc.RoleUnsafeShell`, `cli.Client.UnsafeShellExecute`, and the dispatcher wiring from Task 4.
- Produces CLI: `gateway unsafe-shell --session ID --text TEXT --purpose TEXT --timeout-ms MS`.

- [ ] **Step 1: Write failing daemon lifecycle tests**

Assert: no unsafe-shell socket without `--unsafe-auto-shell`; mode `0600` socket with the flag; all four servers (control, attach, diagnose, unsafe-shell) close cleanly together on SIGTERM/error, reusing the centralized LIFO teardown already built for `diagnose`; startup log includes the unsafe-shell path only when enabled; an invalid or risk-not-accepted board file fails only that socket's startup, leaving control/attach/diagnose unaffected.

- [ ] **Step 2: Write failing CLI parsing and socket-routing tests**

Inject dialers and assert `unsafe-shell` dials only `gatewayd.unsafeshell.sock`, prints the command text to stderr before the JSON result (same UX as `diagnose`), and validates positive timeout/session/text/purpose, mirroring `diagnose`'s validation exactly.

- [ ] **Step 3: Implement the opt-in listener and CLI command**

Create the unsafe-shell server only when `daemonOptions.UnsafeAutoShell != ""`. Add the `unsafe-shell` case to `cmd/gateway/main.go` alongside `diagnose`, calling `cli.Client.UnsafeShellExecute` against the unsafe-shell socket path.

- [ ] **Step 4: Run command tests with race detector and commit**

Run: `go test -race ./cmd/gateway ./cmd/gatewayd ./internal/cli`

Expected: PASS.

```bash
git add cmd/gateway cmd/gatewayd internal/cli
git commit -m "feat: expose opt-in unsafe-shell CLI workflow"
```

---

### Task 6: End-to-end security, documentation, and release verification

**Files:**
- Create: `internal/integration/unsafeshell_test.go`
- Modify: `README.md`
- Modify: `docs/uart-operator-guide.md`
- Modify: `docs/ssh-operator-guide.md`

**Interfaces:**
- Verifies the real `gatewayd` and `gateway` binaries and every interface from Tasks 1-5, plus non-interference with the existing `--auto-readonly` end-to-end suite.

- [ ] **Step 1: Write end-to-end PTY tests**

Build real binaries in a test temp directory. Verify: with `--unsafe-auto-shell` enabled and a `risk_accepted: true` board file, a previously-forbidden mutating command (e.g. `mount -o remount,rw /` or a benign stand-in on the fake target) completes through `gateway unsafe-shell` without interactive approval; a hard-denied interpreter invocation, `eval`, and a command-substitution attempt are rejected before reaching the PTY even with an empty board denylist; a board `deny_executables` entry is rejected while an equivalent non-denied command is not; sensitive-path reads remain denied; manual proposal/approval and `--auto-readonly`'s `diagnose` path continue to work unmodified when both flags are enabled on the same daemon; the unsafe-shell socket is absent by default and absent when the board file is missing `risk_accepted: true`; timeout and disconnect return their exact existing statuses.

- [ ] **Step 2: Run integration tests**

Run: `go test -race ./internal/integration -run UnsafeShell -v`

Expected: PASS.

- [ ] **Step 3: Run the full existing integration suite to confirm no regression**

Run: `go test -race ./internal/integration -v`

Expected: PASS, including every `diagnose`/auto-readonly test unchanged.

- [ ] **Step 4: Document activation, the risk model, and examples**

Add README/operator-guide sections for `gatewayd --unsafe-auto-shell=<board>`, `gateway unsafe-shell`, the `risk_accepted` gate and what it means, the hard denial list and why it cannot be overridden, board `deny_executables`/`deny_exact` placement and permissions, and an explicit statement that this mode trades away the state-change protection `--auto-readonly` preserves -- it is a deliberate operator risk acceptance, not a hardened default.

- [ ] **Step 5: Run formatting and full repository verification**

Run: `gofmt -w internal/policy internal/protocol/v1 internal/ipc internal/cli internal/gateway cmd/gateway cmd/gatewayd internal/integration/unsafeshell_test.go`

Run: `make verify`

Expected: builds succeed, `go vet` succeeds, `gofmt -l` prints nothing, all race-enabled tests pass (including the full pre-existing suite), and the Windows core/transport cross-compile check passes.

- [ ] **Step 6: Confirm only intended changes and commit**

Run: `git status --short` and `git diff --check`

Expected: no whitespace errors; the pre-existing untracked `internal/integration/security_test.go` and local `gateway`/`gatewayd` binaries remain untouched and uncommitted; no file under `internal/policy/common.go`, `RoleDiagnose`, or `diagnose.execute` was modified by this plan.

```bash
git add README.md docs/uart-operator-guide.md docs/ssh-operator-guide.md internal/integration/unsafeshell_test.go
git commit -m "test: verify auto shell denylist mode workflow"
```
