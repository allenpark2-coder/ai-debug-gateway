# Auto-readonly agent design

## Goal

Add an opt-in agent workflow that lets an operator ask for diagnostics in a
chat, see the proposed shell command, and receive the result and analysis
without switching to `gateway attach`, typing `approve`, or copying target
output back into the chat.

The existing interactive proposal/approval workflow remains unchanged and is
the default. Automatic execution is limited to commands that a daemon-side
policy proves are read-only. Unknown or state-changing commands never pass the
automatic path.

The first release targets the current Buildroot/BusyBox "數美拍" board. It
reserves a board-specific policy extension point for future Ambarella tools,
but includes no Ambarella rules until they can be tested on real hardware.

## User experience

The operator explicitly enables the additional local endpoint by starting the
daemon with auto-readonly support. The exact activation is:

```sh
gatewayd --auto-readonly
```

An agent runs one diagnostic command with:

```sh
gateway diagnose --session ID --text 'ps' --purpose 'list processes' --timeout-ms 15000
```

`diagnose` returns one JSON result containing the policy decision, transaction
identity, target output, exit status, and any truncation or timeout metadata.
The command text is printed before execution so the chat can show it to the
operator.

If auto-readonly support was not enabled, the diagnostic endpoint does not
exist and the existing `propose` plus interactive `approve` workflow is the
only path. Both modes can therefore coexist without weakening the default.

For a command that changes state, the agent shows the full command and impact
in chat and asks for explicit confirmation. After the operator replies, the
agent creates a normal proposal and invokes a new non-interactive operator
command:

```sh
gateway approve --proposal ID --confirmation 'operator confirmed in chat'
```

This uses the attach socket, records the supplied confirmation note in the
audit event, and removes the need to switch to the attach terminal. It is an
alternative user interface for the existing approval operation, not part of
`diagnose.execute`.

The gateway cannot cryptographically distinguish an operator's chat reply from
an AI client's assertion about that reply. Deployments that do not delegate
attach-socket access to their chat agent retain the original `Ctrl-] approve`
boundary. Enabling unattended chat operation therefore explicitly delegates
approval transport to that local agent and relies on the agent/UI to invoke
`gateway approve` only after a real operator confirmation. Automatic read-only
execution remains daemon-enforced and does not rely on that assertion.

## Architecture and capability boundary

`gatewayd --auto-readonly` creates a third owner-only Unix socket:

```text
$XDG_DATA_HOME/ai-debug-gateway/gatewayd.diagnose.sock
```

IPC gains `RoleDiagnose`. It has only session status, bounded output reads, and
a new atomic `diagnose.execute` operation. It cannot call `command.approve`,
raw transport write, secret operations, retry, takeover, session start/end, or
host-key acceptance. The existing control and attach allowlists do not change.

The ordinary CLI gains a non-interactive `approve` command which deliberately
dials the existing attach socket. It requires both a proposal ID and a nonempty
confirmation note. Manual command mode and this CLI command reach the same
dispatcher and transaction lifecycle.

`diagnose.execute` performs the following in the daemon:

1. Validate the request and current session.
2. Parse and classify the complete shell text with the active board policy.
3. Reject unless every command and shell construct is explicitly read-only.
4. Create an ordinary proposal and immutable transaction for auditability.
5. Execute it through the existing coordinator and marker protocol.
6. Wait for completion, timeout, transport loss, reboot, takeover, or daemon
   shutdown.
7. Return only the output attributed to that transaction plus its terminal
   status.

Classification and approval happen inside one daemon operation. No proposal ID
is exposed for a second client to edit between policy validation and execution.
The transcript, audit log, secret redaction, output bounds, marker handling,
and one-active-transaction rule remain common with manually approved commands.

## Read-only policy

The policy is an argv- and syntax-aware allowlist, not substring matching. The
first version accepts a deliberately small BusyBox/Linux diagnostic set,
including read-only forms of:

- `cat`, `head`, `tail`, `sed -n`, and `wc` only on exact kernel-defined
  proc facts (`/proc/cpuinfo`, `/proc/meminfo`, `/proc/uptime`,
  `/proc/loadavg`, `/proc/version`, `/proc/filesystems`, `/proc/mounts`), plus
  pipeline input where applicable; normal target paths are never content-read;
- `ps`, batch `top`, `free`, `uptime`, `df`, `mount`, `findmnt`, `ls`, `stat`,
  `du`, and `find` without mutation actions;
- `ip` show/list forms, `ss`, `netstat`, `route -n`, and read-only `ethtool`;
- `dmesg`, `logread`, `uname`, `hostname` without setters, `date` without
  setters, `id`, `whoami`, `groups`, `pwd`, and `printenv`;
- `command -v`, `type`, `which`, and known tools' help/version forms.

The parser permits sequencing used for reports (`;`, `&&`, `||`) and pipes only
when every simple command passes policy. It permits fixed string output with
`echo` and `printf`. It rejects redirections, command substitution, background
execution, assignments that affect a command environment, shell functions,
loops, conditionals, `eval`, interpreters, and arbitrary shell scripts in the
first release. It also rejects ambiguous or unsupported syntax rather than
trying to interpret it.

Paths are checked after lexical normalization. The daemon cannot resolve
target-side symlinks or hard links, so content-reading commands deny all normal
filesystem paths, including `/etc/os-release`, `/etc/hostname`, and benign
aliases. Board exact argv extensions remain explicit operator trust and may
deliberately add a vendor reader; they do not weaken common command rules.

Board policies add exact executable/subcommand rules on top of the common
policy. An unknown `opsis-*`, Ambarella, vendor, or locally installed command
is denied by default. Future Ambarella support will classify exact subcommands
as auto-readonly, confirmation-required, or never-automatic based on hardware
documentation and acceptance tests.

## Failure behavior

Policy rejection returns a structured decision with the rejected syntax or
argv element and a safe explanation. It does not create or execute a
transaction.

Execution timeout sends Ctrl-C, records terminal `timeout`, and disables new AI
transactions until a configured target shell prompt proves resynchronization.
If prompt recognition is unavailable (including SSH), the transport is closed
and enters reconnecting. `timeout_ms` must be 1..300000 (five minutes) and is
validated by CLI and daemon before duration conversion or proposal creation.
Transport loss, target reboot, secret
window entry, daemon shutdown, and user takeover retain their existing terminal
states. The diagnostic client never retries or reconnects automatically.

Output is bounded by the existing limits. A result explicitly states whether
its beginning or end was truncated so an agent cannot mistake partial output
for a complete diagnostic.

## Configuration

Common policy ships in the binary. Optional board additions live under:

```text
$XDG_CONFIG_HOME/ai-debug-gateway/policies/<board>.json
```

The initial external schema supports only additional exact read-only argv
patterns and explicit denials; it cannot override common denials or grant shell
syntax that the parser rejects. Policy files are owner-only, validated fully at
daemon startup, and never reloaded during a running session. Invalid policy
prevents the diagnostic socket from starting but does not prevent manual mode.

## Testing

Unit tests cover accepted and rejected shell syntax, argv/subcommand rules,
path normalization, sensitive paths, and adversarial attempts such as hidden
redirection, substitutions, interpreter escapes, unsafe `find` actions, and
option confusion.

IPC tests prove that control and diagnose roles cannot approve arbitrary
proposals or write transport bytes, and that attach retains its existing
capabilities. Coordinator tests prove atomic validation/execution, output
attribution, timeout, reboot, transport loss, takeover, and secret redaction.

End-to-end PTY tests run the real binaries in both modes. They verify that a
read-only diagnostic completes without interactive approval, a mutation is
rejected before reaching the PTY, manual approval still works, and the
diagnostic socket is absent unless explicitly enabled.

## Non-goals

- Automatically executing state-changing commands based on an AI-supplied
  `confirmed` flag.
- Inferring safety from command names, natural-language purpose, or model
  output.
- Shipping untested Ambarella command rules.
- File transfer, firmware update, flash/OTP/eFuse operations, or automatic
  reconnect.
- Replacing the interactive attach terminal.
