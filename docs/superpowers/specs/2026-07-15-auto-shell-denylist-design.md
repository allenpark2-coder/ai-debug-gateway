# Auto Shell (Denylist) Mode Design

## Goal

Add a second, independently opt-in gateway mode for boards where the operator
has explicitly accepted the risk of automatic, unattended state-changing
execution -- for example, a bench board whose only recovery path is a
firmware reflash and that carries no OTP/eFuse or other unrecoverable
hardware state. This mode lets an AI client run any shell command the
target's BusyBox/Linux environment supports without a human pressing
approve, except for a small, non-configurable set of structural denials and
an explicit, board-scoped denylist.

This mode must not weaken any guarantee `--auto-readonly` makes. The two
modes are independent, separately activated, and share only the parts of the
daemon that are not part of either mode's safety argument: the coordinator's
proposal, transaction, marker, transcript, audit, and secret-redaction
lifecycle. Enabling one never enables or degrades the other, and a client
authorized for one role can never reach the other's operation.

## Scope

The first release targets the same Buildroot/BusyBox board as
`--auto-readonly` and requires the operator to explicitly record, per board,
that this mode's risk has been accepted for that board (see Configuration).
It adds no execution primitive beyond what the target's shell already
exposes: no file transfer, no privileged host-side operation, no
cross-board default, and no relaxation of secret redaction.

## Assumptions and operator responsibility

This design assumes, per board that enables it:

- The target is recoverable by reflashing firmware.
- The target has no OTP/eFuse, write-once bootloader region, or other
  operation whose damage a reflash cannot undo. The daemon has no way to
  verify this; the operator attests to it once per board (see
  Configuration).
- Losing the transport -- disabling `sshd`, changing network configuration,
  an unexpected reboot -- is an inconvenience, not a failure, because UART
  or physical access remains available as a fallback.

If any of these does not hold for a board, that board must not enable this
mode. `--auto-readonly` remains the correct opt-in for it, and the ordinary
propose/approve path remains available regardless.

## User experience

Activation and invocation mirror `--auto-readonly`'s shape but are entirely
separate, and deliberately named so the two are never confused for one
another:

```sh
gatewayd --unsafe-auto-shell=myboard
```

```sh
gateway unsafe-shell --session ID --text 'mount -o remount,rw /' \
  --purpose 'need rw to patch config' --timeout-ms 15000
```

`unsafe-shell` returns the same result shape as `diagnose`: the policy
decision, transaction identity, target output, exit status, and truncation
metadata. The command text is printed before execution, same as `diagnose`,
so a chat surface can show the operator what is about to run.

If `--unsafe-auto-shell` was not passed for a board, its socket and CLI verb
do not exist, exactly as `diagnose` does not exist without
`--auto-readonly`. `propose`/`approve`, `diagnose`, and `unsafe-shell` can
all coexist on the same running daemon; each is gated by its own flag and
reaches its own socket.

## Architecture and capability boundary

`gatewayd --unsafe-auto-shell=<board>` creates a fourth owner-only Unix
socket, independent of the diagnose socket:

```
$XDG_DATA_HOME/ai-debug-gateway/gatewayd.unsafeshell.sock
```

IPC gains `RoleUnsafeShell`. Its allowlist is the same shape as
`RoleDiagnose`'s and exactly as narrow: `session.status`, `output.read`, and
one atomic operation, `unsafeshell.execute`. It cannot reach `command.approve`,
raw transport write, retry, takeover, secret operations, session start/end,
or host-key acceptance. `RoleDiagnose` and `RoleUnsafeShell` are two distinct
roles: a connection authenticated for one is never dispatched against the
other's operation table. This is verified with a permission-matrix test in
the same style as the existing `TestDiagnoseRoleCapabilityMatrix`.

`unsafeshell.execute` reuses the same atomic shape as `diagnose.execute`:
classify, propose, approve, execute through the existing
coordinator/marker lifecycle, wait, return output scoped to that
transaction. It is a different policy object evaluating the same request
shape, not a different execution path. This is the load-bearing design
choice that keeps the two modes isolated: the dispatcher holds two
independent policy values, loaded from two independent files at startup,
so a bug or misconfiguration in one can only ever affect that mode.

## Denylist policy model

A new `policy.Denylist(...)` constructor implements the same
`Evaluate(text string) Decision` contract `Common()` does, so no coordinator
or dispatcher code needs to know which mode it is serving. Internally it
inverts the default:

- Every syntactically valid simple command is **allowed** unless its
  executable matches a hard denial or a board denial.
- **Hard denials are not configurable** and ship in the binary, because a
  denylist is only as good as its ability to name every path to a program,
  and a handful of constructs make that impossible in principle:
  - direct interpreter invocation (`sh`, `bash`, `python`, `perl`, `lua`, and
    the rest of the existing `forbiddenExecutable` list);
  - `eval`;
  - command substitution (`$(...)`, backticks) and process substitution;
  - indirect execution (`exec`, `env <cmd>`, `find ... -exec`, `xargs`
    running an arbitrary program);
  - background/detached execution -- rejected for the same reason
    `--auto-readonly` rejects it: marker-based completion cannot attribute
    output to a job that outlives the transaction, independent of any
    security argument.

  These stay hard-denied because a denylist that names `rm` but allows
  `sh -c 'rm -rf /'` or `` `cat /path/to/rm` `` is not a denylist, it is
  theater. Redirection, pipes, and `;`/`&&`/`||` sequencing remain
  permitted: they move bytes and control flow, they do not let a denied
  program run under a different name.
- **Board denials** are additional operator-supplied executable names or
  exact argv entries (the same `ArgvRule` shape the existing
  `policies/<board>.json` file uses) for programs this specific board's
  operator wants blocked even though the hard list does not cover them --
  a vendor flashing tool, or a `dd` invocation known to target the wrong
  device on this board's storage layout.

Sensitive-path reads (`/etc/shadow`, private keys, `/proc/*/environ`, and
other secret-bearing state) remain denied in this mode. They are not a
mutation risk, so "own board, reflash if it breaks" does not bear on them,
and there is no reason to relax them because the mode is otherwise
permissive.

## Failure behavior

Same shape as `--auto-readonly`: a denial returns a structured `Decision`
without creating a proposal or writing a byte to the transport. Timeout,
transport loss, target reboot, takeover, secret-window entry, and daemon
shutdown retain their existing terminal states, handled by the coordinator
code both modes share.

## Configuration

```
$XDG_CONFIG_HOME/ai-debug-gateway/unsafe-shell/<board>.json
```

Deliberately a different directory from `policies/<board>.json`, so an
operator cannot enable this mode by editing the file they already maintain
for `--auto-readonly` board extensions, and so listing one directory never
reveals whether the other mode is configured for a board.

```json
{
  "risk_accepted": true,
  "deny_executables": ["mkfs.ext4", "flash_erase", "nandwrite"],
  "deny_exact": [
    { "executable": "dd", "args": ["if=/dev/zero", "of=/dev/mtd0"] }
  ]
}
```

`risk_accepted` must be present and `true`; its absence or falsity fails
daemon startup for this mode with an explicit error, requiring the operator
to state in the file, per board, that they accept this mode's risk for that
board. As with the existing policy file, this one is validated fully at
startup (owner-only `0600`, strict JSON, no unknown fields), never reloaded
live, and an invalid file prevents only the unsafe-shell socket from
starting -- it must not prevent manual mode or `--auto-readonly` from
starting.

## Testing

Unit tests cover: every hard denial is rejected even when a board file
attempts to allow it; `deny_executables`/`deny_exact` block exactly what
they name and nothing else; sensitive paths remain denied; redirection,
pipes, and sequencing pass when their components are not denied;
`risk_accepted: false` or a missing field fails to load.

IPC tests prove `RoleUnsafeShell` and `RoleDiagnose` are disjoint: a client
on one socket cannot reach the other's operation, and neither can reach
approve, write, retry, takeover, secret, session, or host-key operations.
Coordinator/dispatcher tests reuse the existing atomic-execution suite,
parameterized over both policy objects, to prove the shared execution path
behaves identically regardless of which policy approved the command.

End-to-end PTY tests run the real binaries with `--unsafe-auto-shell`
enabled and prove: a previously forbidden mutating command (e.g.
`mount -o remount,rw /`) now completes without interactive approval; a hard-
denied interpreter invocation and a command-substitution attempt are still
rejected before reaching the PTY even against an empty board denylist;
`--auto-readonly`'s own behavior and tests are unaffected by this mode being
enabled at the same time; the unsafe-shell socket is absent unless
explicitly enabled; a missing `risk_accepted` field blocks only this
socket's startup, not the daemon.

## Non-goals

- Making any state-changing command "safe" -- this mode is an explicit
  transfer of risk to the operator, not a safety feature.
- Overriding the hard denial list from a board file, under any
  configuration.
- Relaxing sensitive-path read denials.
- Automatic reconnection, file transfer, or firmware operations.
- Any change to `--auto-readonly`'s guarantees, socket, role, or policy when
  this mode is enabled alongside it.
