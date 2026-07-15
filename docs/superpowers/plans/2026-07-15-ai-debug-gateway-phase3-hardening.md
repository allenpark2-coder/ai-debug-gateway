# AI Debug Gateway Phase 3: Hardening and Release Validation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove responsiveness, bounded memory, secret safety, and no replay under failure, then package and physically validate Linux release 0.1.0.

**Architecture:** Deterministic fault/load harnesses drive public boundaries and binaries. Release artifacts include both binaries, documentation, version metadata, and checksums; hardware results are recorded without unredacted secrets.

**Tech Stack:** Go 1.26.5, race detector, fuzzing, benchmarks, PTY/SSH test servers, `go vet`, reproducible cross-builds.

## Global Constraints

- Phase 1 and 2 gates pass first.
- At synthetic 10 MiB/s output with one stalled AI client, p99 human-input forwarding is below 100 ms.
- Memory settles within configured ring/queues plus 64 MiB runtime overhead.
- Secrets appear nowhere in durable files, AI output, panic/test logs, or recovery state.
- Restart/reconnect never replays a proposal or transaction.
- Windows and `arm-autoboot-interrupt` remain documented, not implemented.

---

### Task 1: Backpressure and Latency Harness

**Files:** Create `internal/perftest/harness.go`, `harness_test.go`, `cmd/gateway-bench/main.go`; modify `Makefile`.

**Interfaces:** JSON metrics: `bytes_per_second`, `input_latency_p50_ms`, `input_latency_p99_ms`, `peak_rss_bytes`, `dropped_client_frames`.

- [ ] **Step 1: Write failing load test** — Feed 10 MiB/s for 30 s, stall AI, keep human active; fail on producer block, stopped offsets, excess queues, or p99 >=100 ms.
- [ ] **Step 2: Implement harness** — Timestamp human submission/fake transport write; compute percentiles; Linux command reads `/proc/self/status`, core does not.
- [ ] **Step 3: Add gate**

```make
perf:
	go run ./cmd/gateway-bench -rate 10MiB -duration 30s -max-p99 100ms -runtime-overhead 64MiB
```

- [ ] **Step 4: Run and commit** — Run `make perf`; expect exit 0 JSON. Commit `test: enforce gateway responsiveness bounds`.

### Task 2: Recovery, Secret, and Capability Fault Injection

**Files:** Create `internal/integration/recovery_test.go`, `security_test.go`; modify `internal/core/secret/window.go`, `internal/ipc/server_linux.go` only if red tests expose defects.

**Interfaces:** Existing public behavior only.

- [ ] **Step 1: Kill daemon after proposal, approval, command write, partial marker, and full marker** — Restart must expire pending, finalize incomplete as `daemon-restarted`, preserve complete results, and add zero target writes.
- [ ] **Step 2: Test adversarial secrets** — Fragmented/delayed/transformed echo, timeout, `secret-done`, cancellation, and daemon kill; recursively search all output/files for random secret.
- [ ] **Step 3: Test unauthorized control clients** — Approval, write, secret, host-key accept, takeover, socket replacement, >1 MiB JSON, reused request ID; require denial and zero transport writes.
- [ ] **Step 4: Implement minimal fixes** — Duplicate capability checks at IPC dispatch and coordinator; persist redaction-active state without secret bytes.
- [ ] **Step 5: Verify and commit** — Run `go test -race ./internal/integration -run 'Recovery|Security|Secret' -count=10`; expect ten PASS. Commit `test: harden recovery and secret boundaries`.

### Task 3: Static Checks, Fuzzing, and Release Build

**Files:** Create `internal/core/command/validate_fuzz_test.go`, `internal/gateway/marker_fuzz_test.go`, `scripts/build-release.sh`; modify `Makefile`, `README.md`, `.gitignore`.

**Interfaces:** Produces `dist/ai-debug-gateway_0.1.0_linux_{amd64,arm64}.tar.gz` and SHA-256 files.

- [ ] **Step 1: Fuzz validator/marker** — Validator never panics or accepts CR/LF/NUL; random/mismatched marker never completes a transaction.
- [ ] **Step 2: Add gates**

```make
static:
	go vet ./...
	test -z "$$(gofmt -l .)"
fuzz-smoke:
	go test ./internal/core/command -run '^$$' -fuzz FuzzValidateManaged -fuzztime 30s
	go test ./internal/gateway -run '^$$' -fuzz FuzzMarker -fuzztime 30s
```

- [ ] **Step 3: Build reproducibly** — Script refuses dirty tree; uses `CGO_ENABLED=0`, `-trimpath`, version/commit ldflags; archives binaries and docs, emits checksums.
- [ ] **Step 4: Verify and commit** — Run `make static fuzz-smoke verify perf` and `scripts/build-release.sh 0.1.0`; expect two archives/checksums. Commit `build: add verified Linux release pipeline`.

### Task 4: Physical Acceptance Record

**Files:** Create `docs/acceptance/0.1.0-checklist.md`, `docs/acceptance/README.md`; modify `README.md`.

**Interfaces:** Human sign-off record; no code API.

- [ ] **Step 1: Write checklist** — Record board/firmware/host/adapter/fingerprint/timestamps and pass/fail rows for UART manual/AI/reboot/login/unplug/mismatch/retry, SSH auth/host-key/disconnect/retry, transport-switch exclusivity (UART active refuses SSH start; ending session then switching mints a new ID and drops old pending proposals), takeover, and secret searches.
- [ ] **Step 2: Execute UART acceptance** — Run gates, complete all UART rows on ARM target, attach only redacted evidence.
- [ ] **Step 3: Execute SSH acceptance** — Complete auth, deliberate controlled host-key replacement refusal, reboot/reconnect rows, and the transport-switch exclusivity row against the same board profile used for UART acceptance.
- [ ] **Step 4: Commit only real results** — Every required row is PASS; blockers remain FAIL, never silently skipped. Commit `docs: record 0.1.0 hardware acceptance`.
