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
