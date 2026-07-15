# Task 5 report: opt-in daemon socket and CLI workflows

## Outcome

- Added opt-in `gatewayd.diagnose.sock` lifecycle under `--auto-readonly`, including owner-only mode, stale-socket removal when disabled, coordinated shutdown, listener-error propagation, and conditional startup logging.
- Added `gateway diagnose` with local validation, diagnose-only routing, command display on stderr before dialing/execution, and JSON output.
- Added non-interactive `gateway approve` with required proposal/confirmation flags and attach-only routing.
- Extended approval payloads and audit ordering so an `approval` event containing the proposal and escaped confirmation note precedes the `transaction` event; manual attach approvals remain compatible with an omitted note.
- Updated stale two-role socket comments for the diagnostic role.

## RED evidence

Initial focused test command:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./cmd/gateway ./cmd/gatewayd
```

Failed as intended because the new CLI runner/socket paths and daemon server lifecycle APIs did not exist (`undefined: runCLI`, `socketPaths`, `daemonServer`, and `serveDaemonSockets`).

The approval-audit test then failed to compile on the intentionally absent `commandApprovePayload`. The stale diagnose socket regression failed with:

```text
--- FAIL: TestOrdinaryStartupRemovesStaleDiagnoseSocket
    stale diagnose socket remains: <nil>
```

## GREEN evidence

Focused race verification:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race ./cmd/gateway ./cmd/gatewayd ./internal/cli
```

Passed all three packages.

Full repository verification:

```text
GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./...
```

Passed all packages, including integration tests.

## Self-review

- Confirmed diagnose and approve each dial exactly one intended socket with no fallback.
- Confirmed validation occurs before command display/dial, and valid diagnose prints command text before dial/execution.
- Confirmed invalid policy loading remains before lock/listener creation in daemon startup.
- Confirmed all successfully created listeners are closed on signal, later-listener creation failure, or Serve error.
- Confirmed approval audit ordering and backward-compatible optional confirmation payload.
