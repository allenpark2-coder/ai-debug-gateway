# Windows UART and SSH Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connect the Windows platform foundation to stable-identity COM ports and the existing SSH transport so Windows matches Linux session, approval, reconnect, secret, and auto-readonly behavior.

**Architecture:** Windows serial discovery combines `go.bug.st/serial/enumerator` metadata with SetupAPI Device Instance IDs, stores identity rather than COM number, and resolves the current COM name at start/retry. The existing coordinator remains unchanged except for injected platform serial discovery/open functions. SSH shares the existing transport with Windows path and agent adapters.

**Tech Stack:** Go 1.25, `go.bug.st/serial` v1.7.1, `golang.org/x/sys/windows` SetupAPI/Configuration Manager wrappers, existing `x/crypto/ssh` transport.

## Global Constraints

- Target Windows 10/11 x64 and the user's Silicon Labs adapter currently at COM4.
- A new profile must store Device Instance ID; COM4 is display/current location only.
- Absent, changed, or ambiguous identity must fail rather than fall back to COM-number guessing.
- UART and SSH must retain one-active-transport, secret redaction, approval, timeout, reconnect, and new-session-ID semantics.
- Requested serial line settings must be applied or rejected explicitly.
- No test injection may alter production discovery behavior.
- Linux runtime and tests remain unchanged.
- MSI, Windows Service, ARM64, U-Boot interruption, and file transfer are excluded.

---

### Task 1: Extend the portable serial identity model

**Files:**
- Modify: `internal/transport/transport.go`
- Modify: `internal/profile/profile.go`
- Test: `internal/profile/profile_test.go`
- Modify: `internal/transport/serial/serial.go`
- Test: `internal/transport/serial/serial_test.go`

**Interfaces:**
- Produces identity kind `windows-device-instance-id` with the exact Device Instance ID as `Key`.
- Produces metadata fields `PortName`, `VID`, `PID`, `USBSerialNumber`, `Manufacturer`, and `Product` for display/migration, while equality/security uses identity kind+key.

- [ ] **Step 1: Write failing JSON compatibility and identity tests**

Test Linux profiles decode unchanged, Windows identity round-trips, COM name changes without changing identity, empty Device Instance ID is unknown, and legacy VID/PID/serial profiles migrate only when exactly one present port matches.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/profile ./internal/transport/serial -v`

Expected: FAIL on missing Windows metadata/migration APIs.

- [ ] **Step 3: Implement additive profile fields and validation**

Keep existing JSON field names and add only optional fields. Implement `ResolveIdentity(profile Identity, ports []Port) (Port, error)` with exact-match, legacy exact triplet, absent, and ambiguous outcomes. Never compare COM name as identity.

- [ ] **Step 4: Run compatibility tests and commit**

Run: `go test ./internal/profile ./internal/transport/serial`

Expected: PASS.

```bash
git add internal/transport internal/profile
git commit -m "feat: model stable Windows serial identity"
```

---

### Task 2: SetupAPI COM discovery

**Files:**
- Create: `internal/transport/serial/discovery_windows.go`
- Create: `internal/transport/serial/setupapi_windows.go`
- Create: `internal/transport/serial/discovery_windows_test.go`
- Modify: `internal/transport/serial/discovery_linux.go`

**Interfaces:**
- Produces Windows `List() ([]Port, error)` populated with COM name, complete Device Instance ID, VID/PID/serial, manufacturer, and product.
- Uses an internal `deviceEnumerator` interface for tests; production implementation is SetupAPI-only.

- [ ] **Step 1: Write failing injectable discovery tests**

Cover CP2102 at COM4, composite USB parent serial, COM-number change, two identical devices, non-USB COM ports, missing instance ID, SetupAPI partial errors, and deterministic sorting. Assert fake enumeration is compiled only in tests and cannot be selected by environment variables in production.

- [ ] **Step 2: Run RED on Windows**

Run: `go test ./internal/transport/serial -run 'Discovery|SetupAPI' -v`

Expected: FAIL on missing Windows discovery.

- [ ] **Step 3: Implement SetupAPI enumeration**

Use Ports-class present devices, retrieve `PortName`, `SPDRP_FRIENDLYNAME`, manufacturer, device instance ID, and parent identity as needed. Merge with `enumerator.GetDetailedPortsList()` by COM name only to enrich display metadata; the SetupAPI instance ID remains authoritative identity.

- [ ] **Step 4: Run native and cross-compile tests**

Run on Windows: `go test -race ./internal/transport/serial`

Run on Linux: `GOOS=windows GOARCH=amd64 go test -c ./internal/transport/serial`

Expected: PASS/compile.

- [ ] **Step 5: Commit**

```bash
git add internal/transport/serial
git commit -m "feat: discover stable Windows COM identities"
```

---

### Task 3: Windows COM open and line settings

**Files:**
- Create: `internal/transport/serial/open_windows.go`
- Create: `internal/transport/serial/open_windows_test.go`
- Modify: `internal/transport/serial/open_linux.go`
- Modify: `internal/transport/serial/serial.go`

**Interfaces:**
- Produces existing `serial.Open(port Port, line LineSettings) (transport.Stream, error)` on Windows.
- Normalizes COM names to `\\.\COMN` where required by Win32 while displaying `COMN`.

- [ ] **Step 1: Write failing mode translation tests**

Table-test baud, 5–8 data bits, parity, one/two stop bits, no/hardware/software flow control, COM4/COM12 normalization, access-denied busy errors, unplug read errors, Close cancellation, and identity preservation.

- [ ] **Step 2: Run RED on Windows**

Run: `go test ./internal/transport/serial -run 'Open|Mode|Flow|COM' -v`

Expected: FAIL on missing Windows open wrapper.

- [ ] **Step 3: Implement explicit line-mode application**

Translate every accepted `LineSettings` value to backend DCB behavior. If the pinned backend cannot express a requested flow mode correctly, reject it with an actionable error rather than silently ignoring it. Wrap Win32 access denied/file not found errors with the COM display name.

- [ ] **Step 4: Run Windows serial tests and commit**

Run: `go test -race ./internal/transport/serial`

Expected: PASS.

```bash
git add internal/transport/serial
git commit -m "feat: open Windows COM transports"
```

---

### Task 4: Windows profile creation and daemon UART wiring

**Files:**
- Modify: `cmd/gateway/main.go`
- Test: `cmd/gateway/main_test.go`
- Modify: `cmd/gatewayd/dispatcher.go`
- Test: `cmd/gatewayd/dispatcher_test.go`
- Test: `internal/integration/windows_uart_test.go`

**Interfaces:**
- Consumes Windows `serial.List`, `ResolveIdentity`, and `Open`.
- Produces the unchanged CLI commands `ports`, `profile create`, `start --transport uart`, and attach/retry lifecycle.

- [ ] **Step 1: Write failing CLI/dispatcher tests**

Test port output includes COM4/product/VID/PID/serial/instance ID, profile creation stores stable identity but not COM4 as key, start resolves a changed COM name, ambiguous/missing devices do not open, and busy errors name the port.

- [ ] **Step 2: Run RED on Windows**

Run: `go test ./cmd/gateway ./cmd/gatewayd -run 'Windows|COM|UART' -v`

Expected: FAIL before wiring.

- [ ] **Step 3: Wire platform discovery/open without branching shared lifecycle**

Keep dispatcher session operations platform-neutral; inject Windows functions through the same fields used by Linux tests. Do not duplicate coordinator logic.

- [ ] **Step 4: Add real-binary fake-COM lifecycle test**

Use a build-tagged Windows-only fake serial seam that is absent from production builds. Exercise compiled binaries through profile/start/attach/proposal/auto-readonly/unplug/retry and assert new session ID plus no secret canary.

- [ ] **Step 5: Run Windows race tests and commit**

Run: `go test -race ./cmd/gateway ./cmd/gatewayd ./internal/integration -run 'Windows|COM|UART'`

Expected: PASS.

```bash
git add cmd/gateway cmd/gatewayd internal/integration/windows_uart_test.go
git commit -m "feat: wire Windows UART sessions"
```

---

### Task 5: Windows SSH paths and authentication adapters

**Files:**
- Create: `internal/transport/ssh/agent_windows.go`
- Create: `internal/transport/ssh/agent_windows_test.go`
- Create: `internal/transport/ssh/agent_linux.go`
- Modify: `internal/transport/ssh/auth.go`
- Test: `internal/transport/ssh/auth_test.go`
- Modify: `docs/ssh-operator-guide.md`
- Test: `internal/integration/windows_ssh_test.go`

**Interfaces:**
- Produces platform `dialAgent() (net.Conn, error)` with Windows OpenSSH agent detection where available.
- Reuses existing identity-file, passphrase, password, host-key, reconnect, and result behavior.

- [ ] **Step 1: Write Windows path and auth-order tests**

Test `%USERPROFILE%\.ssh\id_ed25519`, explicit Windows absolute paths, known_hosts creation, encrypted key/passphrase redaction, agent absent fallback, agent present ordering, unknown/changed host rejection, and password flow.

- [ ] **Step 2: Run RED on Windows**

Run: `go test ./internal/transport/ssh -run 'Windows|Agent|Auth|Host' -v`

Expected: FAIL on missing Windows agent adapter/path cases.

- [ ] **Step 3: Implement Windows adapters**

Keep `AuthFactory` ordering shared. Implement only the Windows agent connection boundary and filepath-safe defaults. Agent unavailability must not prevent key/password methods.

- [ ] **Step 4: Run real local SSH integration on Windows**

Run: `go test -race ./internal/transport/ssh ./internal/integration -run 'WindowsSSH|SSH'`

Expected: PASS with a persistent shell, managed command, disconnect, approved retry, and secret canary absence.

- [ ] **Step 5: Commit**

```bash
git add internal/transport/ssh internal/integration/windows_ssh_test.go docs/ssh-operator-guide.md
git commit -m "feat: support Windows SSH sessions"
```

---

### Task 6: COM4 hardware acceptance preparation

**Files:**
- Create: `scripts/windows/acceptance.ps1`
- Create: `docs/windows-hardware-acceptance.md`
- Modify: `Makefile`

**Interfaces:**
- Produces a PowerShell acceptance script that gathers evidence without embedding credentials or automatically approving mutating commands.

- [ ] **Step 1: Implement a dry-run-tested acceptance script**

The script accepts `-GatewayDir`, `-Board`, `-ExpectedPort COM4`, and `-Baud 115200`; validates binaries/checksum, lists ports, records identity, starts daemon in an isolated data root, and prints the exact manual steps for profile/attach/approval/unplug/retry/SSH. `-DryRun` prints commands without opening hardware.

- [ ] **Step 2: Test script parsing on Windows**

Run: `pwsh -NoProfile -File scripts/windows/acceptance.ps1 -GatewayDir dist/test -Board myboard -ExpectedPort COM4 -DryRun`

Expected: exit 0 and no hardware access.

- [ ] **Step 3: Run full runtime verification**

Run on Linux: `make verify`

Run on Windows: `go test -race ./...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add scripts/windows/acceptance.ps1 docs/windows-hardware-acceptance.md Makefile
git commit -m "test: prepare COM4 Windows acceptance"
```
