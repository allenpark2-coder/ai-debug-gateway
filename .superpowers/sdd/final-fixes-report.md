# Final whole-branch fixes report

## Implemented

- IPC servers now register every accepted connection before starting its
  handler and close all registered connections during `Close`, so idle frame
  reads cannot hold shutdown open.
- The dispatcher owns a diagnostic cancellation context. Daemon shutdown first
  stops the coordinator (recording `daemon-restarted` for an active
  transaction), then cancels remaining diagnostic waits, before listener
  handlers are awaited.
- Timeout expiry writes Ctrl-C and records the existing terminal `timeout`
  result. UART-like sessions reject subsequent transactions until the
  configured shell prompt proves resynchronization. Transports without prompt
  recognition (including SSH) are closed and enter reconnecting.
- Removed automatic follow modes `logread -f`, `logread -F`, `dmesg -w`, and
  `dmesg --follow`.
- Diagnose validation is daemon-side and precedes policy/proposal/write:
  session ID, text, and purpose must be nonempty; `timeout_ms` is restricted to
  1..300000. The shared maximum is also enforced by the CLI, preventing
  duration overflow.
- Content-reading commands accept only exact kernel-defined proc facts. Normal
  filesystem paths, including benign-looking paths and aliases, are denied
  because the host daemon cannot resolve target symlinks or hard links. Board
  exact argv additions remain explicit operator trust.
- Updated design/operator documentation and regression/integration tests.

## Verification

- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race ./internal/policy ./internal/ipc ./internal/gateway ./cmd/gatewayd ./internal/integration`
- `GOCACHE=/tmp/ai-debug-gateway-go-cache make verify`
- `git diff --check`

All completed with exit status 0.

## Residual considerations

- A UART profile needs a sufficiently precise prompt pattern to regain
  automatic execution after Ctrl-C. Until it matches, the safe behavior is to
  remain AI-disabled.
- Exact board-policy argv extensions can open files internally; installing
  such an extension is intentionally an operator trust decision.

## Final lifecycle follow-up

- `resyncPending` is now scoped to one transport generation: successful fresh
  starts and SSH retries clear it. Tests cover SSH timeout, disconnect,
  operator retry, and immediate eligibility of the new shell, plus a fresh
  session that must not inherit stale resynchronization state.
- Dispatcher/coordinator teardown is now a centralized LIFO defer in the socket
  supervisor. It runs before server `Close` waits on handlers for signal exits,
  unexpected listener errors, and startup/partial-start returns alike. A
  blocking-handler regression test proves an unrelated listener error cannot
  deadlock teardown and that shutdown releases the in-flight dispatch.
