# Task 6 report

## Scope

- Added compiled `gatewayd` + compiled `gateway` end-to-end tests over the local SSH server's persistent Linux PTY.
- Covered diagnose socket opt-in, automatic `ps`, rejection-before-write for redirection/substitution/unknown vendor/sensitive read/mutation, manual proposal approval through the real CLI, redacted confirmation audit metadata, secret absence, and exact timeout/disconnected results.
- Restored the board name in daemon startup logs and removed a stale unreachable CLI return found by the release gate.
- Documented activation, fallback, policy placement/permissions, default deny, and attach-capability delegation in README and both operator guides.

## RED evidence

1. `go test ./cmd/gatewayd -run TestServeDaemonSocketsLogsBoardName -count=1`
   failed to compile because `serveDaemonSockets` had no board argument. This demonstrated the startup graph could not log the configured board.
2. First `go test -race ./internal/integration -run Diagnose -v -count=1`
   failed because automatic mode correctly requires an owner-only board policy file; the isolated real-process harness did not yet create it.
3. The next integration run exposed that confirmation text is deliberately redacted (`confirmation=[redacted] bytes=26`), so the security assertion was corrected to require metadata while forbidding plaintext.
4. First `make verify` reached `go vet` and failed at `cmd/gateway/main.go:140:2: unreachable code`. The unreachable return was deleted.

## GREEN evidence

- Focused command: `GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race ./internal/integration -run Diagnose -v -count=1`
  passed all three tests.
- Focused daemon command: `GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race ./cmd/gatewayd -count=1`
  passed.
- Full release command: `GOCACHE=/tmp/ai-debug-gateway-go-cache make verify`
  passed build, empty `gofmt -l`, vet, all race-enabled packages (integration 22.613s; SSH 8.961s), Windows core/transport cross-build, and final gateway/gatewayd builds.

## Review notes

- Rejected inputs are checked against the SSH server's received-command list, proving no PTY write rather than merely inspecting a policy response.
- The real-binary suite directly verifies timeout and SSH disconnect. Existing real-PTY UART integration continues to verify `target-rebooted`; attempting to model a target boot banner over SSH would test behavior SSH intentionally classifies as a connection lifecycle rather than UART reboot detection.
- No parent-checkout binaries or untracked `internal/integration/security_test.go` were touched.

## Review remediation

RED findings reproduced:

- The original manual-approval test could pass after its polling deadline and
  did not prove the approved text reached the PTY. It now fails on deadline,
  requires the final command count, and matches the exact `echo mutation;
  printf` managed-command prefix.
- Reading the running daemon's `bytes.Buffer` while checking the canary produced
  a race-detector failure. The subprocess capture now uses a synchronized
  buffer.
- A compiled-daemon UART test could not populate the hard-coded privileged
  `/dev/serial/by-id`. Serial discovery now supports
  `GATEWAYD_SERIAL_BY_ID_DIR` and includes valid identities from that private
  directory; a serial-package test and the full compiled-binary UART test cover
  this path.

GREEN evidence:

- The known authentication canary is also sent through `secret.begin` and a
  deliberately echoing target shell. The target-received line proves the
  canary traversed the secret path. Assertions inspect diagnose stdout and
  stderr, synchronized daemon stdout/stderr, returned transaction output, and
  exported transcript plus audit; none contains it.
- `TestDiagnoseRealUARTBinariesReportTargetRebooted` builds both binaries,
  starts a UART session over a real PTY, runs `gateway diagnose`, injects the
  boot banner during its active transaction, and requires exact
  `target-rebooted` with no exit code.
- Focused review run: `GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race
  ./internal/integration -run Diagnose -v -count=1` passed all four tests in
  7.635s.
- Documentation now says auto-readonly is optional but its 0600 per-board file
  is required when enabled (including an empty JSON extension), recommends
  rather than claims enforcement of directory ownership, and refers to all
  enabled sockets.

## Final seam hardening

- Removed the production-active serial discovery override. Normal Linux builds
  compile `byid_production_linux.go`: the root is the constant trusted
  `/dev/serial/by-id`, environment cannot redirect it, and by-id entries cannot
  introduce devices omitted by upstream serial enumeration. The default race
  test proves both properties.
- Only `-tags integrationtest` compiles the private by-id root and PTY
  enumeration seam. The UART end-to-end test builds its real `gatewayd` binary
  with that explicit tag; ordinary binaries and the Windows cross-build do not
  contain the seam.
- Replaced the fixed secret-path sleep with bounded polling of the target's
  received command list and an explicit timeout failure.
- Fresh focused evidence:
  `GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race -tags integrationtest
  ./internal/integration -run Diagnose -v -count=1` passed all four tests in
  8.184s. Both default and tagged serial-package race tests also passed.
- Final-head release evidence: `GOCACHE=/tmp/ai-debug-gateway-go-cache make
  verify` passed build, empty gofmt check, vet, every race-enabled package
  (integration 19.190s; serial 1.029s), Windows core/transport cross-build, and
  final gateway/gatewayd builds.
