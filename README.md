# ai-debug-gateway

A host-side debug gateway that lets a human and an AI assistant share a
persistent ARM target console over UART. `gatewayd` is the only
process allowed to own the serial device; every AI-proposed command
must be explicitly approved by a human before it ever reaches the
target.

This is the **first release**: Linux only, UART only. SSH, automatic
U-Boot interruption, file transfer, and Windows support are not
implemented yet (see `docs/superpowers/specs/2026-07-15-ai-debug-gateway-design.md`
for the full design and what's deferred).

## Build

```sh
go build ./cmd/gatewayd
go build ./cmd/gateway
```

Or `make verify` to build both, run `go vet`, `gofmt -l`, the full
test suite with `-race`, and confirm the core packages still cross-compile
for `GOOS=windows` (they must stay free of any OS-specific import, so a
later Windows implementation doesn't require rewriting them).

## Quick start

```sh
# Terminal 1: start the daemon (owns the serial device)
gatewayd

# Terminal 2: create a board profile interactively
gateway profile create

# start a session and attach
gateway start --board myboard
gateway attach
```

Inside `attach`, ordinary keystrokes go straight to the target. Press
`Ctrl-]` to enter local command mode:

```
approve <id>       # approve a pending AI proposal
reject <id>        # reject one
edit <id> <text>   # replace a pending proposal's command text
secret             # manually open the secret-entry window
secret-done        # manually close it
retry uart         # human-approved reconnect after a transport loss
takeover           # end the running AI transaction now, regain control
detach             # leave the session running, exit the terminal
end                # end the session, then exit
```

A doubled `Ctrl-]` sends one literal escape byte to the target instead
of entering command mode.

See `docs/uart-operator-guide.md` for the full command reference,
including the AI-facing (`ports` / `start` / `status` / `output` /
`propose` / `export`) commands and file locations.

## Layout

- `internal/core/*` -- platform-independent session, command,
  transcript, audit, and secret-window logic. No `/dev`, socket, or
  terminal API imports.
- `internal/transport`, `internal/transport/serial` -- the common
  `Stream` interface and the Linux UART implementation.
- `internal/gateway` -- the `Coordinator` that wires the above to one
  board session: marker-based command completion, reboot-vs-transport-
  loss handling, retry.
- `internal/protocol/v1`, `internal/ipc` -- the versioned wire protocol
  and the owner-only Unix domain socket transport.
- `internal/profile` -- board profile storage (UART settings +
  identity, never secrets).
- `internal/cli` -- the attach terminal and control-connection client
  shared by `cmd/gateway`.
- `cmd/gatewayd`, `cmd/gateway` -- the daemon and CLI binaries.
- `internal/integration` -- end-to-end tests against a real Linux PTY
  standing in for a UART target, and against the real compiled
  binaries for the parts that need no hardware.

## Status

Phase 1 (this repository) is a complete, tested, UART-only vertical
slice. Known follow-ups before hardware acceptance:

- The pinned `go.bug.st/serial` backend has no public way to enable
  hardware or software flow control on Linux; `Open` rejects a
  `FlowHardware`/`FlowSoftware` request rather than silently ignoring
  it (see `internal/transport/serial/discovery_linux.go`).
- `records.export`'s retention is enforced by capping what one call
  returns, not by pruning the underlying files on disk yet.
