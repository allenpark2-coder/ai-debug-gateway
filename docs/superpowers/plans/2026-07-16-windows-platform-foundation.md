# Windows Platform Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce Windows 10/11 x64 `gateway.exe` and `gatewayd.exe` binaries with native paths, per-user single-instance locking, SID-restricted Named Pipe IPC, and Windows console attach behavior while leaving Linux behavior unchanged.

**Architecture:** Shared protocol, CLI, dispatcher, and coordinator code remain platform-independent. Build-tagged Windows files implement Known Folder paths, ACLs, named mutexes, Named Pipes, console mode, and console-control shutdown; Linux-only code is split behind `//go:build linux`. This phase uses fake transports and does not yet open COM ports.

**Tech Stack:** Go 1.25, `golang.org/x/sys/windows`, `github.com/Microsoft/go-winio`, Windows Named Pipes and console APIs, existing protocol v1.

## Global Constraints

- Target Windows 10 and Windows 11 x64 (`GOOS=windows GOARCH=amd64`).
- `gatewayd.exe` runs in the foreground; no Windows Service behavior.
- Named Pipes are per-user and ACL-restricted to the current SID; the SID in the name is not the security boundary.
- Control, attach, and diagnose role allowlists must remain byte-for-byte equivalent to Linux behavior.
- No Windows-specific import may enter `internal/core`, `internal/gateway`, `internal/profile`, `internal/policy`, or `internal/protocol/v1`.
- Linux tests and runtime behavior must remain unchanged.
- Windows callbacks must signal ordinary Go cleanup; they must not perform unbounded work inside the callback.
- MSI, ARM64, code signing, driver installation, and Windows Service installation are excluded.

---

### Task 1: Establish complete build-tag boundaries

**Files:**
- Modify: `internal/xdgpaths/xdgpaths.go`
- Create: `internal/xdgpaths/xdgpaths_linux.go`
- Create: `internal/xdgpaths/paths_windows.go`
- Modify: `cmd/gateway/main.go`
- Create: `cmd/gateway/terminal_linux.go`
- Create: `cmd/gateway/terminal_windows.go`
- Modify: `cmd/gatewayd/lock.go`
- Create: `cmd/gatewayd/lock_linux.go`
- Create: `cmd/gatewayd/lock_windows.go`
- Test: `internal/integration/windows_compile_test.go`

**Interfaces:**
- Produces: `xdgpaths.Resolve() (Dirs, error)` on both platforms.
- Produces command-local `enterRawMode(*os.File) (func(), error)` on both platforms.
- Produces command-local `acquireLock(path string) (instanceLock, error)` and `releaseLock(instanceLock)` on both platforms.

- [ ] **Step 1: Add a failing complete-binary cross-compile test**

```go
func TestWindowsRuntimeCrossCompiles(t *testing.T) {
    for _, pkg := range []string{"./cmd/gateway", "./cmd/gatewayd"} {
        cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), filepath.Base(pkg)+".exe"), pkg)
        cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
        if out, err := cmd.CombinedOutput(); err != nil {
            t.Fatalf("%s: %v\n%s", pkg, err, out)
        }
    }
}
```

- [ ] **Step 2: Run the test and record the Linux-symbol compile failures**

Run: `go test ./internal/integration -run TestWindowsRuntimeCrossCompiles -v`

Expected: FAIL on `syscall.Flock`, Unix IPC, Linux serial discovery, or POSIX terminal symbols.

- [ ] **Step 3: Split platform-neutral types from Linux implementations**

Keep `Dirs` and shared helpers in `xdgpaths.go`; move XDG resolution to a Linux-tagged file. Move Flock and POSIX terminal code into Linux-tagged files. Add Windows functions that return explicit `windows platform foundation not initialized` errors only where later tasks have not implemented behavior, so compile errors expose missing call sites instead of importing Unix code.

- [ ] **Step 4: Run Linux and Windows compile gates**

Run: `go test ./internal/xdgpaths ./cmd/gateway ./cmd/gatewayd ./internal/integration -run 'WindowsRuntimeCrossCompiles|Resolve|RawMode|Lock'`

Expected: PASS on Linux and successful Windows binary creation.

- [ ] **Step 5: Commit**

```bash
git add internal/xdgpaths cmd/gateway cmd/gatewayd internal/integration/windows_compile_test.go
git commit -m "refactor: establish Windows platform boundaries"
```

---

### Task 2: Windows Known Folder paths and owner ACLs

**Files:**
- Modify: `internal/xdgpaths/paths_windows.go`
- Create: `internal/xdgpaths/paths_windows_test.go`
- Create: `internal/winsecurity/acl_windows.go`
- Create: `internal/winsecurity/acl_windows_test.go`
- Create: `internal/winsecurity/acl_stub.go`

**Interfaces:**
- Produces: Windows `Resolve()` using `%LOCALAPPDATA%\ai-debug-gateway\{config,data}`.
- Produces: `winsecurity.CurrentUserSID() (string, error)`.
- Produces: `winsecurity.EnsureOwnerOnly(path string, directory bool) error`.

- [ ] **Step 1: Write Windows-native failing tests**

```go
func TestResolveUsesLocalAppDataAndCreatesSeparateRoots(t *testing.T) {
    t.Setenv("LOCALAPPDATA", t.TempDir())
    got, err := Resolve()
    if err != nil { t.Fatal(err) }
    if got.Config == got.Data { t.Fatal("config and data must differ") }
    if !strings.HasSuffix(got.Config, `ai-debug-gateway\config`) { t.Fatal(got.Config) }
}
```

Add ACL tests that create a file/directory, apply `EnsureOwnerOnly`, read its DACL, and assert the current user has required access while `Everyone` and `Authenticated Users` do not receive inherited broad access.

- [ ] **Step 2: Cross-compile and run Windows-native RED in CI/local Windows**

Run on Windows: `go test ./internal/xdgpaths ./internal/winsecurity -v`

Expected: FAIL because Known Folder and ACL functions are incomplete.

- [ ] **Step 3: Implement path and ACL creation**

Use `windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)` rather than trusting a mutable environment variable for the default. Preserve explicit test root overrides through documented `GATEWAY_CONFIG_HOME` and `GATEWAY_DATA_HOME`. Construct a protected DACL for the current user SID and SYSTEM, apply it to newly created roots/files, and return contextual errors.

- [ ] **Step 4: Verify on both platforms**

Run on Windows: `go test -race ./internal/xdgpaths ./internal/winsecurity`

Run on Linux: `go test ./internal/xdgpaths ./...`

Expected: PASS; Linux uses XDG paths unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/xdgpaths internal/winsecurity
git commit -m "feat: add secure Windows runtime paths"
```

---

### Task 3: Per-user Windows named mutex

**Files:**
- Modify: `cmd/gatewayd/lock_windows.go`
- Create: `cmd/gatewayd/lock_windows_test.go`
- Modify: `cmd/gatewayd/main.go`

**Interfaces:**
- Consumes: `winsecurity.CurrentUserSID()`.
- Produces: a lock name `Local\ai-debug-gateway-<sid>-<sha256(data-path)[:16]>`.

- [ ] **Step 1: Write failing Windows tests for exclusivity and isolated roots**

Test first acquisition succeeds, second acquisition for the same data root returns `another instance is already running`, release permits reacquisition, and two distinct canonical data paths do not collide.

- [ ] **Step 2: Run RED on Windows**

Run: `go test ./cmd/gatewayd -run 'Lock|Instance' -v`

Expected: FAIL because the Windows lock implementation is incomplete.

- [ ] **Step 3: Implement named mutex ownership**

Use `windows.CreateMutex` with a security descriptor restricted to the current SID. Canonicalize the data path with `filepath.Abs` and case-fold it before hashing. Treat `ERROR_ALREADY_EXISTS` as failure and close every handle on every error/release path.

- [ ] **Step 4: Run Windows race tests and commit**

Run: `go test -race ./cmd/gatewayd -run 'Lock|Instance'`

Expected: PASS.

```bash
git add cmd/gatewayd/lock_windows.go cmd/gatewayd/lock_windows_test.go cmd/gatewayd/main.go
git commit -m "feat: enforce one Windows daemon per data root"
```

---

### Task 4: SID-restricted Windows Named Pipe IPC

**Files:**
- Create: `internal/ipc/client_windows.go`
- Create: `internal/ipc/server_windows.go`
- Create: `internal/ipc/pipe_windows_test.go`
- Modify: `internal/ipc/client_linux.go`
- Modify: `internal/ipc/server_linux.go`
- Modify: `cmd/gateway/main.go`
- Modify: `cmd/gatewayd/main.go`
- Test: `cmd/gatewayd/main_test.go`

**Interfaces:**
- Produces: `ipc.EndpointName(dataRoot string, role Role) (string, error)`.
- Produces the existing `ipc.Dial(endpoint)` and `ipc.Listen(endpoint, role, dispatcher)` APIs on Windows.
- Consumes: `winsecurity.CurrentUserSID()` and the existing `permitted` role matrix.

- [ ] **Step 1: Write failing Windows pipe and ACL tests**

Test round-trip protocol calls for all three roles, exact capability denial, MaxFrameBytes, concurrent clients, active-client shutdown, and DACL inspection. Assert a control pipe cannot approve even if a request payload claims attach role.

- [ ] **Step 2: Run RED on Windows**

Run: `go test ./internal/ipc -run 'Pipe|Capability|Frame' -v`

Expected: FAIL because Windows `Dial`/`Listen` do not exist.

- [ ] **Step 3: Implement Named Pipe client/server**

Use `go-winio` with byte-stream pipes, a current-user/SYSTEM SDDL, bounded input/output buffers, and cancellation-aware listeners. Reuse shared frame parsing and dispatcher logic; do not copy the allowlist. Track active connections and close them before waiting for handlers, matching Linux shutdown semantics.

- [ ] **Step 4: Route command paths through role endpoint names**

Replace Unix socket path construction in the command packages with `ipc.EndpointName`. Preserve Linux filesystem socket names in the Linux implementation and return Windows pipe names in the Windows implementation.

- [ ] **Step 5: Run cross-platform IPC tests**

Run on Windows: `go test -race ./internal/ipc ./cmd/gateway ./cmd/gatewayd`

Run on Linux: `go test -race ./internal/ipc ./cmd/gateway ./cmd/gatewayd`

Expected: PASS on both platforms.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/ipc cmd/gateway cmd/gatewayd
git commit -m "feat: add Windows Named Pipe IPC"
```

---

### Task 5: Windows console attach and shutdown events

**Files:**
- Modify: `cmd/gateway/terminal_windows.go`
- Create: `cmd/gateway/terminal_windows_test.go`
- Create: `cmd/gatewayd/signals_windows.go`
- Create: `cmd/gatewayd/signals_windows_test.go`
- Create: `cmd/gatewayd/signals_linux.go`
- Modify: `cmd/gatewayd/main.go`
- Test: `internal/cli/attach_test.go`

**Interfaces:**
- Produces raw-equivalent Windows console mode with idempotent restore.
- Produces: `waitForShutdown() os.Signal`-equivalent internal notification on Linux and Windows.

- [ ] **Step 1: Write Windows console-mode and callback tests**

Inject console API calls so tests assert input mode disables line/echo processing, preserves required processed control input, output remains byte-compatible, partial setup errors restore the original mode, and restore is idempotent. Test redirected stdin is a no-op. Test the console-control handler only closes/signals a channel and returns promptly.

- [ ] **Step 2: Run RED on Windows**

Run: `go test ./cmd/gateway ./cmd/gatewayd -run 'Console|RawMode|Shutdown' -v`

Expected: FAIL on incomplete Windows functions.

- [ ] **Step 3: Implement native console behavior**

Use `GetConsoleMode`/`SetConsoleMode` and `SetConsoleCtrlHandler`. Keep the shared `cli.Attach` byte loop unchanged so `Ctrl-]`, doubled escape, `Ctrl-C`, approve/reject/edit/secret/retry/takeover/detach/end retain their tested semantics.

- [ ] **Step 4: Run Windows and Linux regression suites**

Run on Windows: `go test -race ./cmd/gateway ./cmd/gatewayd ./internal/cli`

Run on Linux: `make verify`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/gateway cmd/gatewayd internal/cli
git commit -m "feat: support Windows console attach"
```

---

### Task 6: Foundation vertical-slice verification

**Files:**
- Create: `internal/integration/windows_ipc_test.go`
- Modify: `Makefile`
- Modify: `README.md`

**Interfaces:**
- Verifies compiled Windows binaries can start, enforce singleton behavior, exchange protocol messages over all role pipes, and shut down cleanly with fake/injected transports.

- [ ] **Step 1: Add Windows-native real-binary IPC tests**

Build both executables, launch `gatewayd.exe` with isolated roots, wait for pipes, run `gateway.exe status`, verify control rejection of approval, verify diagnose absence by default/presence with flag, hold an idle client during shutdown, and assert bounded exit plus removed pipe instances.

- [ ] **Step 2: Expand build gates**

Change `windows-boundary` to build `./cmd/gateway` and `./cmd/gatewayd` for Windows x64 in addition to platform-independent packages. Add a `windows-test` target documented for a native Windows runner.

- [ ] **Step 3: Run full gates**

Run on Linux: `make verify`

Run on Windows: `go test -race ./...`

Expected: both pass; complete Windows binaries exist.

- [ ] **Step 4: Commit**

```bash
git add internal/integration/windows_ipc_test.go Makefile README.md
git commit -m "test: verify Windows platform foundation"
```
