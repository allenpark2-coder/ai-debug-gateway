# Auto-readonly Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in gateway mode that atomically validates and executes daemon-approved read-only diagnostics, returns their scoped output to an AI client, and retains both interactive and chat-confirmed manual approval paths.

**Architecture:** A new `internal/policy` package parses shell text with `mvdan.cc/sh/v3/syntax` and applies deny-by-default argv, syntax, and path rules. An opt-in diagnose socket has a narrowly constrained IPC role and a single atomic `diagnose.execute` operation; it reuses the coordinator's proposal, transaction, marker, result, transcript, audit, and secret-redaction lifecycle. The existing attach role remains unchanged, while `gateway approve` provides a non-interactive UI over that same capability and records a confirmation note.

**Tech Stack:** Go 1.25, `mvdan.cc/sh/v3/syntax`, Unix domain sockets, existing v1 JSON protocol, Go unit/integration tests with Linux PTYs.

## Global Constraints

- Manual mode remains the default; `gatewayd --auto-readonly` is required to create `gatewayd.diagnose.sock`.
- Control and diagnose connections must never gain raw write, retry, takeover, secret, session start/end, host-key acceptance, or arbitrary proposal approval capabilities.
- Unknown syntax, executables, subcommands, options, paths, vendor tools, and Ambarella tools are denied automatically.
- Policy validation and transaction creation must be atomic inside one daemon request; no editable pending proposal may exist between them.
- Existing transcript, audit, secret redaction, timeout, marker, transport-loss, reboot, and one-active-transaction behavior must be reused.
- External board policy can only add exact read-only argv patterns and denials; it cannot override common denials or rejected syntax.
- No untested Ambarella rules ship in this release.
- Preserve the user's untracked `internal/integration/security_test.go`; do not add or modify unrelated files.

---

### Task 1: Shell policy classifier

**Files:**
- Create: `internal/policy/policy.go`
- Create: `internal/policy/common.go`
- Test: `internal/policy/policy_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: `type Decision struct { Allowed bool; Rule string; Reason string }`
- Produces: `type Policy struct { ... }`, `func Common() *Policy`, and `func (p *Policy) Evaluate(text string) Decision`
- Uses: `mvdan.cc/sh/v3/syntax` solely for parsing; execution remains in the coordinator.

- [ ] **Step 1: Add table-driven failing tests for common safe commands and syntax denials**

```go
func TestCommonPolicy(t *testing.T) {
    tests := []struct{ text string; allowed bool }{
        {`ps`, true}, {`cat /etc/os-release`, true},
        {`ip -brief address`, true}, {`dmesg | tail -n 20`, true},
        {`echo x > /tmp/x`, false}, {`cat $(touch /tmp/pwn)`, false},
        {`sh -c 'id'`, false}, {`find / -exec rm {} \;`, false},
        {`unknown-vendor status`, false}, {`cat /etc/shadow`, false},
    }
    p := Common()
    for _, tt := range tests {
        if got := p.Evaluate(tt.text); got.Allowed != tt.allowed {
            t.Errorf("Evaluate(%q) = %+v, allowed=%v", tt.text, got, tt.allowed)
        }
    }
}
```

- [ ] **Step 2: Run the focused test and verify red**

Run: `go test ./internal/policy -run TestCommonPolicy -v`

Expected: FAIL because `internal/policy` and `Common` do not exist.

- [ ] **Step 3: Add the parser dependency and implement AST-level rejection**

Run: `go get mvdan.cc/sh/v3@latest`

Implement `Evaluate` by parsing exactly one shell file, rejecting redirects, substitutions, background jobs, assignments, functions, loops, conditionals, arithmetic, `eval`, interpreters, and unsupported AST nodes before classifying every `CallExpr`. Permit only `;`, `&&`, `||`, and pipelines whose component calls all pass.

```go
type Decision struct {
    Allowed bool   `json:"allowed"`
    Rule    string `json:"rule"`
    Reason  string `json:"reason"`
}

func deny(rule, reason string) Decision {
    return Decision{Allowed: false, Rule: rule, Reason: reason}
}
```

- [ ] **Step 4: Implement exact command, option, and sensitive-path rules**

Populate explicit validators for the commands listed in the spec. Normalize path operands with `path.Clean`; reject `/etc/shadow`, SSH private keys, `/proc/*/environ`, `/proc/*/mem`, unsafe `/dev` nodes, gateway state, `find` mutation actions, hostname/date setters, and write-capable `ip`/`ethtool` forms. `echo` and `printf` may only emit literal arguments to stdout and cannot contain expansions.

- [ ] **Step 5: Run policy tests and the core platform-independence test set**

Run: `go test ./internal/policy ./internal/core/...`

Expected: PASS.

- [ ] **Step 6: Commit the classifier**

```bash
git add go.mod go.sum internal/policy
git commit -m "feat: classify read-only diagnostic commands"
```

---

### Task 2: Board policy loading and monotonic merge

**Files:**
- Create: `internal/policy/file.go`
- Test: `internal/policy/file_test.go`
- Modify: `cmd/gatewayd/main.go`
- Test: `cmd/gatewayd/main_test.go`

**Interfaces:**
- Consumes: `policy.Common()` and `(*Policy).Evaluate(string)` from Task 1.
- Produces: `type File struct { Allow []ArgvRule; Deny []ArgvRule }`, `func LoadFile(path string, base *Policy) (*Policy, error)`, and `type ArgvRule struct { Executable string; Args []string }`.

- [ ] **Step 1: Write failing tests for exact matching, explicit denial, permissions, and invalid escalation**

```go
func TestLoadFileAddsExactRulesWithoutOverridingDenials(t *testing.T) {
    // 0600 JSON allows ["opsis-inspect","status"] exactly.
    // Extra argv, shell syntax, and a rule for "sh" remain denied.
}
```

Also assert that group/world-readable policy files, malformed JSON, wildcard executable names, empty argv, duplicate contradictory rules, and attempts to allow a common forbidden executable return errors.

- [ ] **Step 2: Run the focused tests and verify red**

Run: `go test ./internal/policy -run TestLoadFile -v`

Expected: FAIL because `LoadFile` is undefined.

- [ ] **Step 3: Implement strict JSON decoding and monotonic merge**

Use `json.Decoder.DisallowUnknownFields`, `os.Stat` permission checks, exact executable/argument arrays, and copy-on-load policy construction. Apply deny rules before allow rules. Refuse rules that name interpreters, mutators, or syntax rather than argv.

- [ ] **Step 4: Add daemon startup policy selection tests**

Refactor daemon argument handling into:

```go
type daemonOptions struct { AutoReadonly bool }
func parseDaemonOptions(args []string) (daemonOptions, error)
```

Test that `--auto-readonly` loads `<config>/policies/<board>.json` once, an invalid policy disables diagnose startup with a clear error while ordinary startup without the flag ignores the file, and no live reload occurs.

- [ ] **Step 5: Run focused tests and commit**

Run: `go test ./internal/policy ./cmd/gatewayd`

Expected: PASS.

```bash
git add internal/policy cmd/gatewayd/main.go cmd/gatewayd/main_test.go
git commit -m "feat: load board diagnostic policies"
```

---

### Task 3: Diagnose protocol role and capability isolation

**Files:**
- Modify: `internal/protocol/v1/message.go`
- Test: `internal/protocol/v1/message_test.go`
- Modify: `internal/ipc/server_linux.go`
- Test: `internal/ipc/server_test.go`
- Modify: `internal/cli/control.go`
- Test: `internal/cli/control_test.go`

**Interfaces:**
- Produces: `v1.OpDiagnoseExecute = "diagnose.execute"`.
- Produces: `ipc.RoleDiagnose` whose allowlist contains only `session.status`, `output.read`, and `diagnose.execute`.
- Produces: `cli.DiagnoseRequest`, `cli.DiagnoseResult`, and `func (c *Client) DiagnoseExecute(req DiagnoseRequest) (DiagnoseResult, error)`.

- [ ] **Step 1: Write failing permission-matrix tests**

```go
func TestDiagnoseRoleCapabilityMatrix(t *testing.T) {
    allowed := []string{v1.OpSessionStatus, v1.OpOutputRead, v1.OpDiagnoseExecute}
    denied := []string{v1.OpCommandApprove, v1.OpTransportWrite, v1.OpRetryUART,
        v1.OpSecretBegin, v1.OpSessionStart, v1.OpSessionEnd, v1.OpHostKeyAccept}
    // Assert each exact decision for RoleDiagnose.
}
```

Also retain assertions that `RoleControl` cannot diagnose or approve and `RoleAttach` behavior is unchanged.

- [ ] **Step 2: Run IPC tests and verify red**

Run: `go test ./internal/ipc -run Diagnose -v`

Expected: FAIL because `RoleDiagnose` and `OpDiagnoseExecute` do not exist.

- [ ] **Step 3: Add the protocol constant, role allowlist, and typed client DTOs**

```go
type DiagnoseRequest struct {
    SessionID string `json:"session_id"`
    Text string `json:"text"`
    Purpose string `json:"purpose"`
    TimeoutMS int64 `json:"timeout_ms"`
}
type DiagnoseResult struct {
    Decision policy.Decision `json:"decision"`
    Transaction *command.Transaction `json:"transaction,omitempty"`
    Result *command.Result `json:"result,omitempty"`
    TruncatedStart bool `json:"truncated_start"`
    TruncatedEnd bool `json:"truncated_end"`
}
```

Keep protocol DTO JSON stable and avoid transport-specific fields.

- [ ] **Step 4: Run protocol, IPC, and CLI tests and commit**

Run: `go test ./internal/protocol/v1 ./internal/ipc ./internal/cli`

Expected: PASS.

```bash
git add internal/protocol/v1 internal/ipc internal/cli
git commit -m "feat: add isolated diagnose IPC role"
```

---

### Task 4: Atomic diagnostic execution and result waiting

**Files:**
- Modify: `internal/gateway/coordinator.go`
- Test: `internal/gateway/coordinator_test.go`
- Modify: `cmd/gatewayd/dispatcher.go`
- Test: `cmd/gatewayd/dispatcher_test.go`

**Interfaces:**
- Consumes: `policy.Policy`, `policy.Decision`, and Task 3 DTO semantics.
- Produces: `func (c *Coordinator) WaitResult(ctx context.Context, transactionID string) (*command.Result, error)`.
- Produces: `dispatcher.diagnoseExecute(payload json.RawMessage)` that policy-checks and starts the transaction while holding a dispatcher diagnostic mutex.

- [ ] **Step 1: Write failing coordinator wait tests**

Test immediate completed results, a waiter awakened by marker completion, context cancellation, timeout status, target reboot, transport loss, takeover, and daemon stop. Assert no goroutine leak with repeated cancellation.

- [ ] **Step 2: Run coordinator tests and verify red**

Run: `go test ./internal/gateway -run 'WaitResult|Diagnose' -v`

Expected: FAIL because `WaitResult` does not exist.

- [ ] **Step 3: Implement result notifications without polling**

Add a bounded waiter registry keyed by transaction ID. Every existing terminal-result recording path closes the matching channel after the result is stored. `WaitResult` checks the store before registration and again under the same coordinator lock to avoid missed wakeups.

- [ ] **Step 4: Write failing dispatcher atomicity and rejection tests**

Use a fake stream to prove: rejected input writes zero bytes and creates no proposal; accepted input writes one marked command; concurrent diagnose requests yield one transaction and one busy error; editing cannot race between policy and approval; secret-active and non-READY sessions reject; result output belongs only to the transaction.

- [ ] **Step 5: Implement `diagnose.execute` using the existing lifecycle**

The dispatcher must:

```go
decision := d.policy.Evaluate(p.Text)
if !decision.Allowed { return DiagnoseResult{Decision: decision}, nil }
prop, err := d.coord.Propose(...)
tx, err := d.coord.Approve(prop.ID)
res, err := d.coord.WaitResult(ctx, tx.ID)
```

Guard the sequence with a dedicated diagnostic mutex, append proposal/auto-readonly-approval/transaction/result audit events, update the open set exactly like manual approval, cap returned output, and set truncation flags.

- [ ] **Step 6: Run gateway and dispatcher tests and commit**

Run: `go test -race ./internal/gateway ./cmd/gatewayd`

Expected: PASS with zero race reports.

```bash
git add internal/gateway cmd/gatewayd/dispatcher.go cmd/gatewayd/dispatcher_test.go
git commit -m "feat: execute policy-approved diagnostics atomically"
```

---

### Task 5: Opt-in daemon socket and CLI workflows

**Files:**
- Modify: `cmd/gatewayd/main.go`
- Test: `cmd/gatewayd/main_test.go`
- Modify: `cmd/gateway/main.go`
- Test: `cmd/gateway/main_test.go`
- Modify: `cmd/gatewayd/dispatcher.go`
- Test: `cmd/gatewayd/dispatcher_test.go`

**Interfaces:**
- Consumes: `ipc.RoleDiagnose`, `cli.Client.DiagnoseExecute`, and the dispatcher policy from Tasks 2–4.
- Produces CLI: `gateway diagnose --session ID --text TEXT --purpose TEXT --timeout-ms MS`.
- Produces CLI: `gateway approve --proposal ID --confirmation NOTE` over the attach socket.

- [ ] **Step 1: Write failing daemon lifecycle tests**

Assert no diagnose socket without the flag; mode `0600` socket with the flag; all three servers close on SIGTERM/error; startup log includes diagnose path only when enabled; invalid policy fails before listener creation.

- [ ] **Step 2: Write failing CLI parsing and socket-routing tests**

Inject dialers and assert `diagnose` dials only `gatewayd.diagnose.sock`, prints command text to stderr before the JSON result, and validates positive timeout/session/text/purpose. Assert `approve` requires both flags, dials only the attach socket, sends `confirmation`, and never falls back to control or diagnose sockets.

- [ ] **Step 3: Implement opt-in diagnose listener and CLI commands**

Create the diagnose server only when `daemonOptions.AutoReadonly` is true. Extend approval payload as:

```go
type commandApprovePayload struct {
    ProposalID string `json:"proposal_id"`
    Confirmation string `json:"confirmation,omitempty"`
}
```

Manual attach approval may omit the note; non-interactive `gateway approve` rejects an empty note locally. Append an `approval` audit record containing the redacted note and proposal ID before the transaction record.

- [ ] **Step 4: Run command tests and commit**

Run: `go test -race ./cmd/gateway ./cmd/gatewayd ./internal/cli`

Expected: PASS.

```bash
git add cmd/gateway cmd/gatewayd internal/cli
git commit -m "feat: expose opt-in diagnostic CLI workflow"
```

---

### Task 6: End-to-end security, documentation, and release verification

**Files:**
- Create: `internal/integration/diagnose_test.go`
- Modify: `README.md`
- Modify: `docs/uart-operator-guide.md`
- Modify: `docs/ssh-operator-guide.md`

**Interfaces:**
- Verifies the real `gatewayd` and `gateway` binaries and all interfaces from Tasks 1–5.

- [ ] **Step 1: Write end-to-end PTY tests for both modes**

Build real binaries in a test temp directory. Verify `ps` completes through `gateway diagnose` without attach approval; `echo x > /tmp/x`, substitutions, unknown vendor commands, sensitive reads, and state-changing commands never reach the PTY; manual proposal/approval still works; chat-confirmed CLI approval records its note; secret output is absent; timeout, reboot, and disconnect return their exact statuses; diagnose socket is absent by default.

- [ ] **Step 2: Run integration tests and verify behavior**

Run: `go test -race ./internal/integration -run Diagnose -v`

Expected: PASS.

- [ ] **Step 3: Document activation, trust boundary, and examples**

Add README/operator-guide examples for `gatewayd --auto-readonly`, `gateway diagnose`, manual fallback, `gateway approve --confirmation`, policy path/permissions, default-deny vendor behavior, and the fact that chat-confirmed mutation approval delegates attach capability to the local agent.

- [ ] **Step 4: Run formatting and full repository verification**

Run: `gofmt -w internal/policy internal/protocol/v1 internal/ipc internal/cli internal/gateway cmd/gateway cmd/gatewayd internal/integration/diagnose_test.go`

Run: `make verify`

Expected: builds succeed, `go vet` succeeds, `gofmt -l` prints nothing, all race-enabled tests pass, and the existing core cross-compile check passes.

- [ ] **Step 5: Confirm only intended changes and commit**

Run: `git status --short` and `git diff --check`

Expected: no whitespace errors; the pre-existing untracked `internal/integration/security_test.go` and local `gateway`/`gatewayd` binaries remain untouched and uncommitted.

```bash
git add README.md docs/uart-operator-guide.md docs/ssh-operator-guide.md internal/integration/diagnose_test.go
git commit -m "test: verify auto-readonly agent workflow"
```
