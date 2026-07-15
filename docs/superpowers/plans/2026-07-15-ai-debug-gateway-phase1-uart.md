# AI Debug Gateway Phase 1: UART Vertical Slice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a Linux UART gateway with an ordinary human terminal and an AI read/propose interface whose commands require interactive human approval.

**Architecture:** `gatewayd` alone owns UART and exposes versioned JSON over an owner-only Unix socket. `gateway` supplies attach and control modes; platform-free core packages own state, approval, bounded transcripts, separate audits, and secret redaction.

**Tech Stack:** Go 1.25 language baseline, Go 1.26.5 toolchain, `go.bug.st/serial`, `golang.org/x/term`, standard-library JSON/Unix sockets, Linux PTYs.

## Global Constraints

- Put `go 1.25.0` in `go.mod`; verify with Go 1.26.5 or newer Go 1.26 security patch.
- Transport reads never wait for AI clients, approval, or durable writes.
- AI capability is read/propose only; approval, secret entry, takeover, and raw writes are human-only.
- Managed commands are one physical line with balanced quotes, no NUL, heredoc, or async/background list.
- Restart never restores or replays pending/running work.
- Secret-window input and target output never reach AI, transcript, or audit payloads.
- Run `go test -race ./...` at every task gate.

---

### Task 1: Module and Session State Machine

**Files:** Create `go.mod`, `.gitignore`, `internal/core/id/id.go`, `internal/core/session/state.go`; test `internal/core/session/state_test.go`.

**Interfaces:** Produces `id.New(prefix string) string`, `session.Machine`, `State`, `Event`, and `Apply(Event) error`.

- [ ] **Step 1: Verify toolchain** — Run `go version`; expect Go 1.26.5+ within the 1.26 line. Install the official toolchain first if not.
- [ ] **Step 2: Write the failing reconnect test**

```go
func TestRetryRotatesSession(t *testing.T) {
	m := NewMachine("old")
	for _, e := range []Event{Connect, TransportReady, Authenticated, TransportLost} {
		if err := m.Apply(e); err != nil { t.Fatal(err) }
	}
	if err := m.Apply(HumanRetry); err != nil { t.Fatal(err) }
	if m.State() != Connecting || m.SessionID() == "old" { t.Fatalf("%+v", m) }
}
```

- [ ] **Step 3: Confirm red** — Run `go test ./internal/core/session -run TestRetryRotatesSession -v`; expect undefined symbols.
- [ ] **Step 4: Implement table-driven transitions**

```go
const (
	Disconnected State = "DISCONNECTED"; Connecting State = "CONNECTING"
	Authenticating State = "AUTHENTICATING"; Ready State = "READY"
	RunningCommand State = "RUNNING_COMMAND"; Reconnecting State = "RECONNECTING"
	Error State = "ERROR"
)
```

Cover connect, auth, run/result, target reboot preserving ID, transport loss, human retry rotating ID, exhaustion, and shutdown.
- [ ] **Step 5: Verify and commit** — Run `go test -race ./internal/core/session`; expect PASS. Commit `feat: add gateway session state machine`.

### Task 2: Managed Command Lifecycle

**Files:** Create `internal/core/command/types.go`, `validate.go`, `store.go`; test `validate_test.go`, `store_test.go`.

**Interfaces:** Produces `Proposal`, immutable `Transaction`, `Result`, `Store.Propose/Edit/Approve/InvalidateSession`, and `ValidateManaged(string) error`.

- [ ] **Step 1: Write validator cases**

```go
tests := []struct{text string; ok bool}{
	{"uname -a", true}, {"cd /tmp && pwd", true}, {"pwd\nid", false},
	{"printf 'oops", false}, {"cat <<EOF", false}, {"sleep 1 &", false},
}
```

- [ ] **Step 2: Confirm red** — Run `go test ./internal/core/command -run TestValidateManaged`; expect undefined validator.
- [ ] **Step 3: Implement lexer** — Track quote/escape state; reject CR/LF/NUL, `<<`/`<<-`, and unquoted single `&`; permit quoted text, `;`, `&&`, `||`.
- [ ] **Step 4: Write lifecycle test**

```go
p, _ := s.Propose(Input{SessionID:"s1", Text:"pwd", Purpose:"cwd", Timeout:time.Second})
tx, err := s.Approve(p.ID)
if err != nil || tx.ID == p.ID || tx.SourceProposalID != p.ID { t.Fatal(tx, err) }
if _, err = s.Approve(p.ID); !errors.Is(err, ErrNotPending) { t.Fatal(err) }
```

- [ ] **Step 5: Implement expiry, replacement edit, atomic single-use approval, advisory interactive-command warnings, result duration/context, statuses, and invalidation.**
- [ ] **Step 6: Verify and commit** — Run `go test -race ./internal/core/command`; expect PASS. Commit `feat: add approved command lifecycle`.

### Task 3: Transcript, Audit, and Secret Window

**Files:** Create `internal/core/transcript/ring.go`, `store.go`, `internal/core/audit/store.go`, `internal/core/secret/window.go`; matching `_test.go` files.

**Interfaces:** Produces `Ring.Append/ReadAfter`, separate `transcript.Writer` and `audit.Writer`, and `secret.Window.Begin/Submit/FilterTarget/Finish/Active`.

- [ ] **Step 1: Write wraparound test**

```go
r := NewRing(8); r.Append([]byte("123456")); r.Append([]byte("7890"))
c := r.ReadAfter(0, 8)
if !c.Gap || string(c.Data) != "34567890" { t.Fatalf("%+v", c) }
```

- [ ] **Step 2: Implement bounded ring** — Writes copy and never wait; reads return `Start`, `Next`, `Data`, `Gap`.
- [ ] **Step 3: Write store/redaction tests** — Use different temp files and sequence spaces. Submit random secret, feed fragmented target echo, and assert the secret is absent from both stores and AI output.
- [ ] **Step 4: Implement independent `0600` JSONL stores and redaction window** — Suppress durable/AI target bytes until recognized prompt or human `secret-done`; timeout stays human-only.
- [ ] **Step 5: Verify and commit** — Run `go test -race ./internal/core/transcript ./internal/core/audit ./internal/core/secret`; expect PASS. Commit `feat: add bounded redacted records`.

### Task 4: Serial Profiles and Identity-Safe Transport

**Files:** Modify `go.mod`; create `internal/profile/profile.go`, `internal/transport/transport.go`, `internal/transport/serial/serial.go`, `discovery_linux.go`; matching tests.

**Interfaces:** Produces `transport.Stream`, `serial.List`, `serial.Open`, `serial.Match`; profiles contain UART settings and identity but no secrets.

- [ ] **Step 1: Pin dependency** — Run `go get go.bug.st/serial@v1.7.1`.
- [ ] **Step 2: Write profile and matching tests** — Cover invalid line settings, by-id match, USB serial after tty rename, reused tty path, no identity, and multiple matches.
- [ ] **Step 3: Confirm red** — Run `go test ./internal/profile ./internal/transport/serial`; expect missing implementations.
- [ ] **Step 4: Implement storage/discovery/open**

```go
type Stream interface { io.Reader; io.Writer; io.Closer; Identity() Identity; Kind() string }
type MatchResult struct { Port *Port; NeedsHumanSelection bool; Reason string }
```

Use atomic `0600` profile writes. Never automatically select changed, absent, or ambiguous identity.
- [ ] **Step 5: Verify and commit** — Run `go test -race ./internal/profile ./internal/transport/...`; expect PASS without hardware. Commit `feat: add identity-safe UART transport`.

### Task 5: Coordinator, Marker, Reboot, and Retry

**Files:** Create `internal/gateway/coordinator.go`, `marker.go`, `subscriber.go`; matching tests.

**Interfaces:** Produces `Coordinator.StartUART/Propose/Approve/RetryUART/ReadAfter/SubscribeHuman/Takeover`.

- [ ] **Step 1: Write stalled-subscriber test** — Stall an AI queue, burst fake transport output, and assert human output and keystroke forwarding continue.
- [ ] **Step 2: Write marker/reboot/login tests** — Marker must match transaction ID plus 128-bit nonce. Boot banner yields `target-rebooted`, preserves UART/session ID, clears shell state, and enters authentication. Configurable `login:` sends only the profile username; `Password:`, `sudo`, `passwd`, and nested SSH prompts pause for the human secret window; unknown prompts leave AI disabled and human raw access available.
- [ ] **Step 3: Implement serialized event loop** — Reader only appends ring/enqueues bounded events. Send immutable command plus `$?` marker on one line; never invent exit code. Prompt matchers are profile-configurable and secret grace expiry interrupts the transaction without ending redaction.
- [ ] **Step 4: Implement loss/retry** — EOF/hangup/ENODEV/persistent EIO enters reconnecting. `RetryUART` requires exact trusted identity and creates a new ID; otherwise return `ErrHumanSelectionRequired`.
- [ ] **Step 5: Verify and commit** — Run `go test -race ./internal/gateway`; expect marker, reboot, unplug, retry, takeover, and no-replay PASS. Commit `feat: coordinate approved UART sessions`.

### Task 6: Owner-Only IPC and Daemon

**Files:** Create `internal/protocol/v1/message.go`, `internal/ipc/server_linux.go`, `client_linux.go`, `cmd/gatewayd/main.go`; matching tests.

**Interfaces:** Versioned newline JSON operations: `ports.list`, `session.start/status/end`, `output.read`, `command.propose/list`, `records.export`; no control capability for approve/secret/raw-write/takeover.

- [ ] **Step 1: Write permission/capability tests** — Socket mode `0600`; reject unknown version, >1 MiB frames, `command.approve`, and `transport.write` on control connections. `records.export` returns separately redacted transcript and audit summary streams by sequence/offset and enforces configured retention.
- [ ] **Step 2: Implement protocol**

```go
type Request struct { Version, RequestID, Operation string; Payload json.RawMessage }
type Response struct { Version, RequestID string; Result json.RawMessage; Error *ProtocolError }
```

- [ ] **Step 3: Implement daemon** — XDG paths, safe parent permissions, single-instance lock, graceful shutdown, restart finalization as `daemon-restarted`, no automatic transport reopen.
- [ ] **Step 4: Verify and commit** — Run `go test -race ./internal/ipc ./internal/protocol/... ./cmd/gatewayd`; expect PASS. Commit `feat: expose owner-only gateway API`.

### Task 7: Attach and Control CLI

**Files:** Modify `go.mod`; create `cmd/gateway/main.go`, `internal/cli/control.go`, `attach.go`, `commandmode.go`; matching tests.

**Interfaces:** Commands `ports`, interactive `profile create`, `start`, `status`, `output --after --json`, `propose`, `export`, `attach`; human escape commands `approve/reject/edit/secret/secret-done/retry uart/takeover/detach/end`.

- [ ] **Step 1: Pin terminal dependency** — Run `go get golang.org/x/term@v0.40.0`.
- [ ] **Step 2: Test escape/capability behavior** — `Ctrl-]` is local, doubled prefix sends one literal byte, control CLI cannot approve or write. Interactive profile creation lists every port and line setting; `export` preserves transcript/audit separation.
- [ ] **Step 3: Implement CLI** — Attach restores raw terminal on all exits/signals; transaction mode forwards only Ctrl-C/takeover; secret input has no echo/error payload.
- [ ] **Step 4: Verify and commit** — Run `go test -race ./internal/cli ./cmd/gateway` and `go build ./cmd/gateway ./cmd/gatewayd`; expect success. Commit `feat: add human and AI gateway clients`.

### Task 8: PTY End-to-End Gate and Docs

**Files:** Create `internal/integration/uart_test.go`, `fakeconsole_test.go`, `README.md`, `docs/uart-operator-guide.md`, `Makefile`.

**Interfaces:** Exercises public binaries/protocol; no new internal API.

- [ ] **Step 1: Build PTY fake target** — Emit boot/login/password/prompt, parse marker commands, simulate reboot and hangup.
- [ ] **Step 2: Test full workflow** — Start daemon/profile, attach human, propose/approve `pwd`, check scoped output/exit 0; test echoed secret omission, reboot same ID, unplug/retry new ID, mismatch refusal.
- [ ] **Step 3: Document exact operator and AI commands** — Include XDG paths, port settings, approval, secret, takeover, reboot, retry, logs, shutdown; state SSH/U-Boot/Windows are absent.
- [ ] **Step 4: Add gate**

```make
.PHONY: verify
verify:
	go test ./...
	go test -race ./...
	go build ./cmd/gateway ./cmd/gatewayd
```

- [ ] **Step 5: Run platform boundary and Linux gates** — Run `GOOS=windows GOARCH=amd64 go test ./internal/core/... ./internal/transport`; expect compile/PASS without Linux symbols. Run `make verify`; expect all Linux tests/builds PASS.
- [ ] **Step 6: Commit** — Commit `test: verify UART gateway end to end` with only Task 8 files.
