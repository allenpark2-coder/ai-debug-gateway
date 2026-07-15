# UART operator guide

This covers the first release: Linux, UART only. SSH, automatic U-Boot
interruption, file transfer, and Windows are not implemented (see
`docs/superpowers/specs/2026-07-15-ai-debug-gateway-design.md`).

## File locations

`gatewayd` resolves these once at startup and creates them with
owner-only permissions if missing:

| Purpose | Path |
|---|---|
| Board profiles | `$XDG_CONFIG_HOME/ai-debug-gateway/profiles/<name>.json` (default `~/.config/...`) |
| Control socket (AI-capable) | `$XDG_DATA_HOME/ai-debug-gateway/gatewayd.control.sock` (default `~/.local/share/...`) |
| Attach socket (human-only) | `$XDG_DATA_HOME/ai-debug-gateway/gatewayd.attach.sock` |
| Single-instance lock | `$XDG_DATA_HOME/ai-debug-gateway/gatewayd.lock` |
| Durable transcript | `$XDG_DATA_HOME/ai-debug-gateway/transcript.jsonl` |
| Durable audit log | `$XDG_DATA_HOME/ai-debug-gateway/audit.jsonl` |
| Open-transaction recovery state | `$XDG_DATA_HOME/ai-debug-gateway/open-transactions.json` |

Both sockets are mode `0600`: only the owning user can connect. The
lock file ensures only one `gatewayd` instance runs against a given
data directory; a second attempt fails immediately with a clear error
instead of silently fighting over the same serial port.

## Starting the daemon

```sh
gatewayd
```

`GATEWAYD_BOARD` (default `default`) and `GATEWAYD_USERNAME` (default
`root`) are read from the environment. `gatewayd` logs the resolved
socket paths on startup and runs until it receives `SIGINT` or
`SIGTERM`.

If `gatewayd` was killed or crashed while a transaction was approved or
running, the next startup finalizes it as `daemon-restarted` in the
audit log before serving any connection -- nothing pending survives a
restart to be accidentally replayed.

## Creating a board profile

```sh
gateway profile create
```

This lists every discovered serial port (path and, when available, its
stable `/dev/serial/by-id/...` identity) and prompts for:

- profile name
- which discovered port index to use
- baud rate
- data bits (5-8)
- parity (`none`/`odd`/`even`)
- stop bits (1 or 2)
- flow control (`none`/`hardware`/`software`)

A port with no stable USB identity is refused: without one, a later
reconnect could silently attach to a different physical device that
happens to land on the same `/dev/ttyUSB*` path.

**Flow control note:** the pinned `go.bug.st/serial` backend disables
both hardware and software flow control at the termios level on Linux
with no public option to re-enable either. `hardware`/`software` are
accepted here for forward compatibility but `start` will currently
reject them; use `none`.

## Starting and attaching

```sh
gateway start --board myboard
gateway attach
```

`start` resolves the profile, matches it against currently discovered
ports by identity (never by path), and opens it. If the identity is
absent, changed, or ambiguous, `start` fails rather than guessing.

`attach` puts the terminal into raw mode (restored on normal exit and
on `SIGINT`/`SIGTERM`) and begins forwarding keystrokes. `output.read`
is polled internally to display target output as it arrives, since the
wire protocol is request/response only.

## Local command mode

Press the configured escape prefix (`Ctrl-]` by default) inside
`attach`, then type a command and Enter:

| Command | Effect |
|---|---|
| `approve <id>` | Approve a pending AI proposal; its command text is sent with a completion marker appended on the same shell line. |
| `reject <id>` | Reject a pending proposal. |
| `edit <id> <text>` | Replace a pending proposal's command text with a new one; does not approve or execute it. |
| `secret` | Manually open the secret-entry window, for a password/passphrase prompt no configured pattern recognized. |
| `secret-done` | Manually close the window, after visually confirming the prompt completed. |
| `retry uart` | Human-approved reconnect after a transport loss (unplug, hangup, persistent I/O error). Requires the device's identity to still resolve; mints a new session ID. |
| `takeover` | Immediately end the running AI transaction as `interrupted-by-user` and restore normal keystroke forwarding. |
| `detach` | Leave the session running on the daemon; exit the local terminal. |
| `end` | Disconnect the session, then exit. |

A doubled escape prefix sends one literal escape byte to the target
instead of entering command mode.

While an approved AI transaction is running, ordinary keystrokes are
paused (to keep output attribution unambiguous); `Ctrl-C` is still
forwarded to the target, and `takeover` always works.

## Secret entry

Passwords and passphrases never reach an AI client, a config file, a
transcript, or the audit log. Configured patterns (the initial login
`Password:` prompt, `sudo`, `passwd`, a nested `ssh`) automatically
open the redaction window and pause any running AI transaction for a
configured grace period; unrecognized prompts require the human to
type `secret` manually. A misconfigured remote terminal that echoes
the typed secret back is also filtered from durable storage and
AI-visible output -- the gateway never trusts remote echo state alone.

A timeout never silently ends the redaction window: the session stays
human-only until the human completes or cancels the secret flow.

## Target reboot vs. transport loss

These are handled differently on purpose:

- **Target reboot** (the host's serial connection stays open and
  usable, but a configured boot-banner pattern appears): the session
  ID is preserved, any active transaction is finalized as
  `target-rebooted` with no fabricated exit code, and the session
  returns to re-authenticate.
- **Transport loss** (USB-serial adapter unplugged, device hangup, a
  persistent I/O error): the session enters `RECONNECTING`. `retry
  uart` is required to resume, and always mints a new session ID --
  previous working directory and shell state are considered lost.

## Reading logs and shutting down

`gateway export` (control-capable, works over either socket) returns
the durable transcript and audit records after a given sequence
number, already separated into two independent stores. Retention is
enforced per export call, capping how much history one call returns.

To shut down cleanly, send `SIGTERM` (or `Ctrl-C` in the foreground) to
`gatewayd`; it stops accepting connections and exits.
