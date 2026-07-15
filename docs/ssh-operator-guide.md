# SSH operator guide

This covers Phase 2: SSH is added alongside UART (see
`docs/uart-operator-guide.md` for UART itself, and the design spec for
what's still deferred: automatic U-Boot interruption and Windows).

## Scope and non-goals

SSH here is a **debug console transport**, not a file-transfer or
tunneling tool:

- No SFTP, no SCP, no port forwarding.
- No simultaneous UART and SSH: a board session uses exactly one
  active transport. Starting the other while one is active is refused;
  switching requires explicitly ending the current session first
  (`end`), which invalidates its pending proposals and mints a new
  session ID for whatever starts next.
- A reconnect is always a new shell: previous working directory and
  environment state are lost, and no command is ever replayed.

## Board profile

An SSH profile stores, never a secret:

| Field | Meaning |
|---|---|
| `Host`, `Port` | Where to connect. |
| `User` | The SSH account. |
| `IdentityFiles` | Private key paths to try, in order. |
| `UseAgent` | Whether to try `ssh-agent` (via `SSH_AUTH_SOCK`) first. |
| `KnownHostsFile` | Where host-key verification reads from and appends to. |

Passwords and private-key passphrases are never stored; they are
entered interactively for each connection attempt through the local
secret-entry mode (`secret` / `secret-done`), the same mechanism UART
uses for a login password.

## Authentication order

For each connection attempt, methods are tried in this order:

1. **`ssh-agent`**, if `UseAgent` is set and the agent is reachable. An
   unreachable agent is not an error; the remaining methods still
   apply.
2. **Each configured identity file**, in order. An encrypted key asks
   for its passphrase through secret entry; a file that still fails to
   read or parse is skipped, not fatal to the whole attempt.
3. **Password**, only if the human supplied one for this attempt.

If none of these produce a usable method, the connection attempt fails
immediately rather than connecting with no authentication offered.

## Host-key verification

Exactly two outcomes, deliberately with different amounts of human
control:

- **Known host, key matches:** connects normally.
- **Unknown host** (no entry yet): requires an explicit human
  confirmation before the first connection; once accepted, the key is
  appended to the known-hosts file and every later connection to that
  host needs no re-acceptance.
- **Changed or revoked key:** always a hard failure. There is no accept
  path for this case at all -- not from a human, and never from an AI
  client -- since a changed key can mean a man-in-the-middle attack,
  not just a legitimately reinstalled host.

## Persistent shell

A session opens one PTY and one shell on it, kept open for the whole
session: `cd`, `export`, and shell functions all survive across
approved commands on the same connection, exactly like UART. Terminal
resize propagates to the remote PTY.

## Automatic read-only diagnosis

Start `GATEWAYD_BOARD=myboard gatewayd --auto-readonly`, then run:

```sh
gateway diagnose --session SESSION_ID --text 'ps -ef' \
  --purpose 'inspect process state' --timeout-ms 15000
```

The diagnose socket is absent by default. Common rules default-deny unknown
vendor tools, substitutions/redirections, sensitive reads, and mutations
before they reach the SSH PTY. Auto-readonly is optional, but enabling it
requires `$XDG_CONFIG_HOME/ai-debug-gateway/policies/<board>.json`, even when
the file is only `{"allow":[],"deny":[]}`. The file must be mode `0600` or
startup fails; an owner-only directory is recommended. Extensions cannot
override common denials.

Denied work stays in the manual proposal flow. Review it in `gateway attach`,
or—only after explicit operator confirmation in chat—use `gateway approve
--proposal ID --confirmation 'operator confirmed in chat'`. This delegates
attach capability to the local agent for that mutation; control, diagnose,
and unsafe-shell sockets remain unable to approve. Confirmation is redacted
in the audit log.

## Automatic denylist-mode shell execution

A second, independently opt-in mode allows unattended state-changing
commands. Start `gatewayd --unsafe-auto-shell=myboard`, then run:

```sh
gateway unsafe-shell --session SESSION_ID --text 'mount -o remount,rw /' \
  --purpose 'need rw to patch config' --timeout-ms 15000
```

This is a deliberate transfer of risk to the operator, not a hardened
default: it never asks a human before running a command, so use it only on
a board recoverable by reflashing firmware, with no OTP/eFuse or other
operation a reflash cannot undo. The unsafe-shell socket is absent by
default. Enabling it requires
`$XDG_CONFIG_HOME/ai-debug-gateway/unsafe-shell/<board>.json`, mode `0600`,
containing at least `{"risk_accepted": true}` -- the operator's explicit
per-board attestation. A missing or invalid file disables only this socket;
manual mode and `--auto-readonly` are unaffected, and the two automatic
modes can run on the same daemon without either weakening the other's
policy. Interpreters, `eval`, command/process substitution, indirect
execution (`exec`, `env`, `xargs`, `find -exec`), and sensitive-path reads
remain denied unconditionally regardless of board configuration; the file
can only add further denials (`deny_executables`, `deny_exact`), never grant
an exception to them.

## Disconnect and reconnect

Network loss or the remote end closing the connection terminates the
active transaction as `disconnected`, invalidates pending proposals,
and enters `RECONNECTING`. From the local command mode:

```
retry ssh
```

is the human-approved reconnect: it always opens a brand new SSH
connection (new authentication, a new PTY, a new shell) and mints a
new session ID. It never resumes the old shell's working directory or
environment, and never replays a command that was in flight when the
connection dropped.

## What to verify before trusting a profile

- Confirm host-key acceptance happened against the real host, not a
  network path you don't trust (verify the fingerprint out of band the
  first time).
- If using an identity file, confirm its permissions are private
  (mode `0600` or stricter) -- this repository does not check that for
  you.
- After first connecting, `gateway export` and search the audit/
  transcript output yourself if you want to independently confirm no
  password or passphrase appears in either store.
