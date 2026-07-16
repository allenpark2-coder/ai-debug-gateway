# Windows Portable Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a reproducible `v0.2.0` Windows x64 portable directory, ZIP, SHA-256 checksum, Windows-native CI evidence, and COM4 acceptance handoff; MSI remains deferred.

**Architecture:** Linux performs deterministic cross-build packaging, while a GitHub Actions Windows runner executes native tests and verifies the same portable layout. Release metadata is generated from an explicit version input and embedded into both binaries. Hardware acceptance consumes the unsigned portable ZIP locally and records no secrets.

**Tech Stack:** Go 1.25, GNU Make, PowerShell 7, GitHub Actions Windows runner, ZIP and SHA-256 tooling available in Go/PowerShell.

## Global Constraints

- Artifact name is `ai-debug-gateway-v0.2.0-windows-amd64.zip`.
- ZIP contains exactly `gateway.exe`, `gatewayd.exe`, `README-Windows.md`, and `LICENSE` under one versioned directory.
- Portable runtime requires no Administrator rights and makes no registry or PATH changes.
- Produce and verify a SHA-256 checksum.
- Windows CI must run native tests; Linux cross-compilation alone is insufficient.
- Secrets, private keys, passwords, and machine-specific profiles must never enter artifacts or CI logs.
- MSI/WiX, signing, ARM64, Windows Service, and driver installation are deferred.

---

### Task 1: Version metadata and Windows guide

**Files:**
- Create: `internal/buildinfo/buildinfo.go`
- Test: `internal/buildinfo/buildinfo_test.go`
- Modify: `cmd/gateway/main.go`
- Modify: `cmd/gatewayd/main.go`
- Create: `README-Windows.md`
- Modify: `README.md`

**Interfaces:**
- Produces: `buildinfo.Version`, `buildinfo.Commit`, and `buildinfo.String()` populated by `-ldflags -X`.
- Produces `gateway version` and `gatewayd --version` output.

- [ ] **Step 1: Write failing version CLI tests**

Assert default development output is deterministic, injected `v0.2.0`/commit appears exactly once, version commands do not resolve paths or connect IPC, and both binaries use the same formatter.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/buildinfo ./cmd/gateway ./cmd/gatewayd -run Version -v`

Expected: FAIL on missing buildinfo/version commands.

- [ ] **Step 3: Implement build info and operator guide**

Document extraction to a local directory, PowerShell commands, COM identity behavior, foreground daemon, UART/SSH/auto-readonly usage, ACL/busy errors, retry, logs, and checksum verification. State unsigned portable build/SmartScreen expectations without telling users to disable security controls globally.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./internal/buildinfo ./cmd/gateway ./cmd/gatewayd`

Expected: PASS.

```bash
git add internal/buildinfo cmd/gateway cmd/gatewayd README.md README-Windows.md
git commit -m "feat: add Windows release metadata"
```

---

### Task 2: Deterministic portable ZIP builder

**Files:**
- Create: `cmd/package-windows/main.go`
- Create: `cmd/package-windows/main_test.go`
- Create: `scripts/build-windows-portable.sh`
- Modify: `Makefile`
- Modify: `.gitignore`

**Interfaces:**
- Produces `make windows-portable VERSION=v0.2.0`.
- Produces `dist/ai-debug-gateway-v0.2.0-windows-amd64.zip` and matching `.sha256`.

- [ ] **Step 1: Write failing package layout/determinism tests**

Build fixture inputs, run packager twice with fixed `SOURCE_DATE_EPOCH`, assert byte-identical ZIPs, one versioned root directory, exact four filenames, normalized timestamps/modes, no absolute paths, and checksum verification.

- [ ] **Step 2: Run RED**

Run: `go test ./cmd/package-windows -v`

Expected: FAIL because the packager is missing.

- [ ] **Step 3: Implement cross-build and Go ZIP packaging**

The shell script validates a clean explicit version, sets `GOOS=windows GOARCH=amd64 CGO_ENABLED=0`, injects version/commit, builds both executables, and calls the Go packager. The packager sorts entries and fixes metadata for reproducibility.

- [ ] **Step 4: Build and inspect the real artifact**

Run: `make windows-portable VERSION=v0.2.0`

Run: `sha256sum -c dist/ai-debug-gateway-v0.2.0-windows-amd64.zip.sha256`

Expected: PASS; ZIP has exact contents.

- [ ] **Step 5: Commit**

```bash
git add cmd/package-windows scripts/build-windows-portable.sh Makefile .gitignore
git commit -m "build: create reproducible Windows portable ZIP"
```

---

### Task 3: Windows-native GitHub Actions CI

**Files:**
- Create: `.github/workflows/windows.yml`
- Create: `scripts/windows/verify-portable.ps1`
- Test: `scripts/windows/verify-portable.Tests.ps1`
- Modify: `README.md`

**Interfaces:**
- Produces a Windows x64 CI job that runs native Go tests and uploads portable artifacts/checksums.

- [ ] **Step 1: Write PowerShell verification tests**

Test checksum success/failure, exact ZIP layout, version output, execution from outside checkout, and rejection of extra files. Use Pester if preinstalled; otherwise invoke script fixtures directly with PowerShell and assert exit codes.

- [ ] **Step 2: Implement CI workflow**

Pin action major versions, set Go from `go.mod`, run `go test -race ./...`, build portable artifact, run verification script, and upload ZIP/checksum. Do not expose repository secrets to pull-request artifact execution.

- [ ] **Step 3: Validate workflow syntax and local scripts**

Run: `pwsh -NoProfile -File scripts/windows/verify-portable.ps1 -Zip dist/ai-debug-gateway-v0.2.0-windows-amd64.zip -Checksum dist/ai-debug-gateway-v0.2.0-windows-amd64.zip.sha256 -ExpectedVersion v0.2.0`

Expected on Windows: exit 0.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/windows.yml scripts/windows README.md
git commit -m "ci: verify Windows runtime natively"
```

---

### Task 4: Release and COM4 acceptance checklist

**Files:**
- Create: `docs/windows-release-checklist.md`
- Modify: `docs/windows-hardware-acceptance.md`
- Modify: `README-Windows.md`

**Interfaces:**
- Produces an auditable checklist for CI evidence, artifact hash, Windows version, COM identity, UART/SSH behavior, reconnect, busy errors, auto-readonly, audit, and secret canary.

- [ ] **Step 1: Write the exact acceptance record format**

Require fields for git commit/tag, ZIP SHA-256, Windows build, adapter product/VID/PID/serial/instance ID, initial/final COM number, baud/line settings, UART/SSH session IDs, commands tested, reconnect result, and secret-canary search result. Never record the secret itself.

- [ ] **Step 2: Dry-run the portable acceptance script**

Run on Windows: `pwsh -NoProfile -File scripts/windows/acceptance.ps1 -GatewayDir dist\ai-debug-gateway-v0.2.0-windows-amd64 -Board myboard -ExpectedPort COM4 -Baud 115200 -DryRun`

Expected: exit 0 with all required manual steps.

- [ ] **Step 3: Run release gates**

Run on Linux: `make verify && make windows-portable VERSION=v0.2.0`

Run on Windows: `go test -race ./...` and `scripts/windows/verify-portable.ps1`.

Expected: PASS before hardware acceptance begins.

- [ ] **Step 4: Commit**

```bash
git add docs/windows-release-checklist.md docs/windows-hardware-acceptance.md README-Windows.md
git commit -m "docs: add Windows release acceptance gate"
```

---

### Task 5: Final release verification

**Files:**
- Modify only files required by verified defects found during the gates.

**Interfaces:**
- Confirms portable Windows v1 is ready for COM4 hardware acceptance; it does not create MSI artifacts.

- [ ] **Step 1: Run formatting, vet, race, and cross-build gates**

Run: `make verify`

Expected: PASS with complete Windows binaries cross-compiled.

- [ ] **Step 2: Run Windows-native tests and package verification**

Run on Windows: `go test -race ./...`

Run on Windows: `pwsh -NoProfile -File scripts/windows/verify-portable.ps1 -Zip dist/ai-debug-gateway-v0.2.0-windows-amd64.zip -Checksum dist/ai-debug-gateway-v0.2.0-windows-amd64.zip.sha256 -ExpectedVersion v0.2.0`

Expected: PASS.

- [ ] **Step 3: Confirm repository and artifact hygiene**

Run: `git status --short`, `git diff --check`, inspect ZIP listing, and scan repository/artifact strings for the test secret canary.

Expected: clean tracked worktree, no whitespace errors, exact ZIP layout, and no canary.

- [ ] **Step 4: Close the release gate**

If any preceding command fails, return to the task that owns the failing file,
add a regression test, implement the narrow fix, and repeat Steps 1–3. When all
commands pass, record the commit SHA and artifact SHA-256 in
`docs/windows-release-checklist.md`; do not create an empty gate-only commit.
