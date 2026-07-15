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
