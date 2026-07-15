# AI Debug Gateway Phase 2: SSH Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a verified persistent SSH PTY that reuses Phase 1 approval, records, secrets, and reconnect behavior without changing UART semantics.

**Architecture:** SSH implements the existing transport boundary. Authentication and host-key decisions use only human attach capability; EOF enters the common reconnect state and an approved retry creates a fresh session generation.

**Tech Stack:** Go 1.25 language baseline, Go 1.26.5 toolchain, `golang.org/x/crypto/ssh`, `ssh/agent`, `ssh/knownhosts`, Phase 1 IPC.

## Global Constraints

- Phase 1 `make verify` passes before and after every task.
- Host-key mismatch is a hard error and has no AI or human bypass.
- New host acceptance, passwords, and key passphrases are interactive human operations.
- Reconnect loses SSH shell state and never replays commands.
- No SFTP, SCP, forwarding, or simultaneous UART/SSH connection.

---

### Task 1: SSH Profiles and Authentication

**Files:** Modify `go.mod`, `internal/profile/profile.go`; create `internal/transport/ssh/auth.go`; test `auth_test.go` and profile tests.

**Interfaces:** Produces `AuthRequest`, `SecretPrompt`, `AuthFactory.Build`; profiles add host/port/user/key paths/agent/known-hosts without secrets.

- [ ] **Step 1: Pin dependency** — Run `go get golang.org/x/crypto@v0.48.0`.
- [ ] **Step 2: Write profile tests** — Round-trip SSH fields; reject serialized password/passphrase keys.
- [ ] **Step 3: Write auth-order test**

```go
want := []Method{MethodAgent, MethodPrivateKey, MethodPassword}
got, err := factory.Build(ctx, profile, humanSecrets)
if err != nil || !slices.Equal(methodKinds(got), want) { t.Fatal(got, err) }
```

- [ ] **Step 4: Implement agent, encrypted-key, and password callbacks** — Secret callbacks return bytes only to the handshake and zero temporary slices afterward; do not enable agent forwarding.
- [ ] **Step 5: Verify and commit** — Run `go test -race ./internal/profile ./internal/transport/ssh -run 'Profile|Auth'`; expect PASS. Commit `feat: add SSH authentication profiles`.

### Task 2: Host Keys and Persistent PTY

**Files:** Create `internal/transport/ssh/hostkey.go`, `stream.go`; tests `hostkey_test.go`, `stream_test.go`.

**Interfaces:** Produces `HostKeyVerifier.Check/AcceptNewHuman` and `Open(Profile, HumanAuth) (transport.Stream, error)`.

- [ ] **Step 1: Test host-key matrix** — Known succeeds; unknown requires human token; AI token fails; changed/revoked fails without accept path; human acceptance atomically appends mode `0600`.
- [ ] **Step 2: Implement verifier** — Use `knownhosts.New`; an empty `KeyError.Want` means unknown, non-empty means mismatch.
- [ ] **Step 3: Test persistent PTY** — Local server receives one PTY/shell request, `cd /tmp` affects next `pwd`, resize propagates, cancellation closes.
- [ ] **Step 4: Implement stream**

```go
type Stream struct { client *gossh.Client; session *gossh.Session; stdin io.WriteCloser; stdout io.Reader }
func (s *Stream) Kind() string { return "ssh" }
```

Request `xterm-256color`; merge stdout/stderr into ordered console events.
- [ ] **Step 5: Verify and commit** — Run `go test -race ./internal/transport/ssh`; expect PASS. Commit `feat: add verified SSH PTY transport`.

### Task 3: Coordinator Reconnect and Human UI

**Files:** Modify `internal/gateway/coordinator.go`, `internal/protocol/v1/message.go`, `internal/cli/commandmode.go`; create `internal/gateway/ssh_test.go`.

**Interfaces:** Adds `Coordinator.StartSSH`, `RetrySSH`; human command `retry ssh`.

- [ ] **Step 1: Write disconnect test** — EOF finalizes `disconnected`, invalidates proposals, enters reconnecting, and never reconnects on network recovery alone.
- [ ] **Step 2: Write transport-exclusivity test** — For one board profile: `StartUART` then `StartSSH` on the same board returns `ErrTransportActive` and opens no second connection; ending the UART session then `StartSSH` succeeds, mints a new session ID distinct from the ended UART session's, and leaves the old session's pending proposals invalidated.

```go
if err := c.StartUART(profile); err != nil { t.Fatal(err) }
uartID := c.SessionID()
if _, err := c.StartSSH(profile); !errors.Is(err, ErrTransportActive) { t.Fatal(err) }
if err := c.EndSession(humanCapability); err != nil { t.Fatal(err) }
if err := c.StartSSH(profile); err != nil { t.Fatal(err) }
if c.SessionID() == uartID { t.Fatal("session ID reused across transport switch") }
if len(store.PendingForSession(uartID)) != 0 { t.Fatal("old session proposals not invalidated") }
```

- [ ] **Step 3: Write approved retry test**

```go
old := c.SessionID()
if err := c.RetrySSH(humanCapability); err != nil { t.Fatal(err) }
if c.SessionID() == old { t.Fatal("session ID reused") }
if fakeServer.CommandCount() != 0 { t.Fatal("command replayed") }
```

- [ ] **Step 4: Implement through common state events** — Keep only opener/auth callbacks transport-specific; bounded backoff applies only after a human retry request; the coordinator tracks one active transport per board and rejects starting a second one until the active session ends.
- [ ] **Step 5: Verify both transports and commit** — Run `go test -race ./internal/gateway ./internal/cli ./internal/integration`; expect UART unchanged and SSH PASS, including transport-exclusivity. Commit `feat: integrate approved SSH reconnect`.

### Task 4: SSH End-to-End Gate and Docs

**Files:** Create `internal/integration/ssh_test.go`, `docs/ssh-operator-guide.md`; modify `README.md`.

**Interfaces:** Exercises public binaries; no new internal API.

- [ ] **Step 1: Build controlled server** — Generate ephemeral host/user keys, password/public-key auth, PTY shell, disconnect hook, host-key replacement.
- [ ] **Step 2: Test auth/security/session** — Key, agent, encrypted key, password, unknown host acceptance, changed host refusal, repeated shell state, disconnect and approved new-ID retry; search stores for secrets.
- [ ] **Step 3: Document exact profile/auth/reconnect workflow and transfer/forwarding non-goals.**
- [ ] **Step 4: Run and commit** — Run `make verify`; expect UART+SSH PASS. Commit `test: verify SSH gateway end to end`.

