# AI-Assisted Debug Gateway Design

## Goal

Build a host-side debug gateway that lets a human and an AI assistant work
through a persistent ARM target console. The gateway must preserve the normal
interactive UART or SSH experience while ensuring that every AI-proposed
command is explicitly approved by a human before it reaches the target.

The first release runs on the Linux development host connected to the target.
It is independent of Codex, Claude Code, or any other AI client and exposes a
stable local control interface that those tools can call.

## Scope

The first release includes:

- a persistent UART console with selectable port and line settings;
- a persistent interactive SSH shell;
- one active transport per board session;
- a human terminal client with direct manual input;
- an AI-facing CLI for reading output and proposing commands;
- explicit, single-use human approval for every AI command;
- structured command completion, timeout, interruption, and disconnect
  results;
- reboot observation and Linux console login handling over UART;
- password and passphrase entry that never enters AI context or logs;
- bounded buffering, asynchronous transcripts, and audit records;
- Linux pseudo-terminal and fake-transport integration tests.

The first release does not implement Windows support, automatic U-Boot
interruption, file transfer, port forwarding, or AI control of full-screen
interactive programs.

## Architecture

The system consists of a daemon and a general-purpose CLI:

```text
                              +-- UART transport
board profile -> one session -+
                              +-- SSH transport
                                      |
                                gateway daemon
                                      |
                                cmd/gateway CLI
                              /                 \
                       attach mode        control mode
                                                |
                                       Codex / Claude Code
```

The daemon is the only process allowed to own the serial device or SSH
connection. It owns session state, transport I/O, login detection, command
transactions, buffering, transcripts, and audit records.

The CLI has two roles:

- an attached terminal for ordinary human interaction, secret entry, and
  approval or rejection of AI proposals;
- a non-interactive control interface through which any AI client can inspect
  state and output and create a pending proposal.

On Linux, daemon and CLI communicate through a permission-restricted Unix
domain socket. No TCP listener is enabled by default.

## Components and boundaries

The Go codebase is divided into narrowly scoped packages:

- `core/session`: platform-independent session state and transitions;
- `core/command`: proposal, approval, execution, completion, and cancellation;
- `core/transcript`: bounded history, redaction metadata, and durable records;
- `core/audit`: security events, approval history, and independent retention;
- `transport`: the common byte-stream and lifecycle interface;
- `transport/serial`: port discovery, serial settings, and UART behavior;
- `transport/ssh`: host verification, authentication, PTY, and reconnect
  behavior;
- `platform`: local IPC, terminal mode, filesystem locations, and device
  discovery boundaries;
- `cmd/gatewayd`: daemon entry point;
- `cmd/gateway`: interactive terminal and machine-readable control commands.

Core packages must not refer directly to `/dev`, Unix sockets, POSIX terminal
APIs, COM ports, or Windows Named Pipes. Platform-specific behavior stays
behind explicit interfaces so a later Windows implementation does not require
rewriting session or approval logic.

Transcript and audit packages have separate store interfaces, files, sequence
spaces, retention settings, and export policies. They may share low-level
append-only framing utilities, but neither store is implemented as a mode of
the other.

## Board profiles and transport selection

A board profile can store both UART and SSH configuration. Starting a debug
session requires selecting exactly one transport. The gateway does not use
automatic failover and cannot open UART and SSH simultaneously for the same
session. Changing transport explicitly ends the old session, invalidates its
pending proposals, and creates a new session identifier.

UART configuration supports interactive selection and persistent profiles for:

- serial port, preferring stable `/dev/serial/by-id` names when available;
- baud rate;
- data bits, parity, and stop bits;
- hardware and software flow control.

SSH configuration supports host, port, username, key locations, agent use,
and known-hosts policy. Profiles never store passwords or private-key
passphrases.

## Human and AI input model

The command lifecycle uses these terms:

- A **proposal** is mutable, unapproved intent created by an AI or by a human
  edit. It has a proposal ID and can be pending, rejected, expired, replaced,
  or approved.
- Approval snapshots one proposal into an immutable **transaction** with a new
  transaction ID and a `source_proposal_id`. Editing does not approve or
  execute anything; it replaces the old proposal with a new pending proposal.
- Executing a transaction produces exactly one terminal **result**. A result
  retains both IDs and its completion status. Transactions are never retried
  implicitly.

Human input in the attached terminal behaves like an ordinary UART or SSH
terminal. AI clients never receive a raw write capability. An AI client can
only create a proposed command containing:

- a unique proposal identifier;
- the exact command text;
- a human-readable purpose;
- a requested timeout;
- creation and expiration timestamps;
- the board, session, and transport identifiers.

The terminal displays the complete proposal. Only an interactive human client
can approve, reject, or edit it:

```text
approve <id>
reject <id>
edit <id>
```

Local gateway commands are entered through a configurable terminal escape
prefix, initially `Ctrl-]`, followed by a command-mode prompt. Escape commands
are consumed locally and are never forwarded to the target. A doubled escape
prefix sends one literal escape byte to the target. Approval, rejection,
secret entry, transport switching, and forced takeover are available only in
this local command mode.

Approval is single-use. It expires when its deadline passes or when the
session or transport changes. Editing marks the original proposal as replaced
and creates a new human-modified proposal requiring its own approval. The
audit record retains the original proposal, replacement, approval decision,
immutable transaction, and executed command.

While an approved AI transaction is executing, normal human text input is
paused to preserve output attribution. The human retains Ctrl-C, forced
takeover, and disconnect controls. Takeover marks the transaction
`interrupted-by-user` and immediately restores manual control.

## Command execution and results

UART and SSH use a persistent interactive shell so `cd`, `export`, shell
functions, and other session state survive between commands.

For an ordinary shell transaction, the daemon appends a unique completion
marker that reports the command's exit status. This avoids relying only on
prompt text. The marker is scoped to the current session and removed from the
human-facing result where appropriate.

First-release managed commands use a deliberately restricted shell contract.
The proposal must be one physical line, contain no NUL, have balanced shell
quotes, contain no here-document operator, and contain no asynchronous or
background list. A shell-aware lexer performs this validation for both AI
creation and human edit; invalid text cannot become pending or approved.
Sequential and conditional lists using `;`, `&&`, and `||` are allowed. A
trailing unquoted `&` and other unquoted asynchronous-list forms are rejected
because marker completion would describe job launch rather than job finish.

The daemon writes the immutable command snapshot followed on the same shell
line by a session-random marker and captured `$?`. A marker is accepted only
when its nonce and active transaction ID both match. Commands that replace or
terminate the shell, reboot the target, or otherwise prevent marker emission
complete only by timeout or disconnect; they are not reported with a fabricated
exit code.

Each completed transaction reports:

- combined console output for its execution interval;
- exit code when the shell marker was observed;
- duration;
- one of `completed`, `timeout`, `disconnected`, `interrupted-by-user`,
  `protocol-error`, or `daemon-restarted`; only a recovered daemon uses
  `daemon-restarted` to finalize an interrupted durable record;
- bounded transcript context before and after execution.

Daemon restart or crash invalidates every pending proposal and marks every
approved or running transaction as `daemon-restarted`; neither is restored or
replayed. The AI must create a new proposal against the new session.
Full-screen programs such as `top`, `vi`, and `menuconfig` are manual-only in
the first release. Before approval, configurable command-name patterns provide
an advisory warning for known interactive programs. This warning is not a
security boundary and cannot identify arbitrary scripts that enter an
interactive mode; forced takeover remains the recovery mechanism.

## Session state and login handling

Sessions use explicit states:

```text
DISCONNECTED --human request--> CONNECTING -> AUTHENTICATING -> READY
                                                         <-> RUNNING_COMMAND

READY or RUNNING_COMMAND --disconnect--> RECONNECTING
RECONNECTING --human-approved retry/new session ID--> CONNECTING
RECONNECTING --cancel or exhausted retries--> ERROR or DISCONNECTED
any active state --human shutdown--> DISCONNECTED
```

`RUNNING_COMMAND` returns to `READY` after an ordinary result. A disconnect
from `READY` or `RUNNING_COMMAND` enters `RECONNECTING`; only a human-approved
retry can move it to `CONNECTING`. Entering `RECONNECTING` invalidates pending
proposals and terminates any active transaction. The transition back to
`CONNECTING` allocates a new session ID, after which authentication runs again.
Failed or exhausted retries enter `ERROR`; human shutdown enters
`DISCONNECTED`. State changes are timestamped and included in the audit
stream.

### UART

The daemon keeps the serial port open across a target reboot. It records boot
output and recognizes configurable login, password, and shell-prompt patterns.
At `login:` it may submit the profile username. If the target then presents a
shell prompt, authentication is complete. If it presents `Password:`, the
gateway pauses and requests hidden human input.

If the prompt cannot be recognized, raw human terminal access remains
available, but AI transactions stay disabled until the human confirms the
session is ready or a configured prompt is detected.

### SSH

SSH supports username/password, private keys, `ssh-agent`, and hidden entry of
private-key passphrases. It verifies host keys against known hosts. A new host
requires explicit human confirmation; a changed key is rejected and reported
as a security error.

Network loss or reboot terminates the current transaction as `disconnected`.
Reconnect requires human approval, uses bounded retries with backoff, and
creates a new session. Previous working directory and environment state are
therefore considered lost.

## Secrets and local security

Passwords and passphrases are entered only through an attached interactive
terminal with echo disabled. Secret bytes are written to the selected
transport but are never sent to an AI client, configuration file, transcript,
or audit record. Secret input intervals carry explicit redaction metadata so
the normal byte-recording path cannot accidentally persist them.

Secret input is an explicit local terminal mode, entered with the command-mode
operation `secret`. Configured password-prompt patterns may automatically
pause an AI transaction and request this mode, including prompts from `sudo`,
`passwd`, or a nested `ssh`, not only initial login. A human can enter secret
mode manually when an unknown prompt appears. While active, local echo is off,
input bytes bypass transcript and audit byte capture, and a redaction window
also suppresses target output from AI clients and durable stores so a
misconfigured remote echo cannot leak the submitted secret. Exact secret echo
bytes are also removed from the live human display.

The redaction window begins before the first secret byte is accepted and ends
only when a configured post-authentication prompt is recognized or the human
uses the local `secret-done` operation after inspecting the live console. A
timeout does not silently end redaction: it leaves the session in human-only
mode until the human completes or cancels the secret flow. Cancellation closes
the active authentication attempt rather than replaying buffered bytes. The
gateway never infers safety from remote terminal echo state alone. If a
password prompt occurs during an AI transaction, its command timeout is paused
only for the configured secret-entry grace period; expiry interrupts the
transaction but preserves the redaction window and human-only mode.

The daemon socket is accessible only to the owning user. AI clients have
read-and-propose capability; approval capability is available only to an
interactive human terminal. Host-key verification is strict, and a host-key
change cannot be approved through the AI interface.

## Responsiveness and backpressure

The transport read loop must never wait for AI processing, approval, a slow
client, or durable log I/O. Each transport has a dedicated goroutine that
publishes bytes into a bounded in-memory ring buffer. Terminal clients, AI
clients, and the transcript writer consume through independent bounded
channels.

A slow client may lose its live view and must resume from a transcript offset;
it cannot stall the session. Transcript writes are asynchronous and batched.
Queue sizes, ring-buffer capacity, and flush intervals are configurable with
safe defaults and hard upper bounds.

UART and SSH terminal data remain byte streams. AI parsing and redaction run
off the direct human-display path. The acceptance tests include sustained
burst output, concurrent reads, and slow-client simulation to demonstrate no
deadlock, bounded memory use, and responsive human input.

Under a synthetic continuous 10 MiB/s transport-output test with one stalled
AI client and transcript writing enabled, forwarding of human keystrokes to
the transport must remain below 100 ms at the 99th percentile on the reference
development host. Process memory must settle within the configured ring and
queue allocation bounds plus 64 MiB of runtime overhead. The actual UART line
rate is much lower; the synthetic rate provides deliberate headroom.

## Transcript and audit model

Each record carries board, session, transport, monotonic sequence, timestamp,
direction, source, and redaction state. Raw console transcripts and security
audit events are logically separate:

- transcripts capture permitted target input and output for debugging;
- audit records capture proposals, human decisions, executed command text,
  state transitions, interruptions, and terminal results.

Records use append-only files in the platform configuration/data directory.
The CLI reads incrementally by sequence or offset so an AI does not need the
entire historical transcript on every turn. Retention limits are configurable.

## Public control operations

The first release provides stable, machine-readable commands for:

- listing serial ports and board profiles;
- starting, inspecting, attaching to, and ending a session;
- reading console output after a sequence or offset;
- creating and inspecting a pending proposal;
- reconnecting an SSH session after human approval;
- exporting a redacted transcript and audit summary.

Human approval, secret entry, first-use host-key confirmation, and forced
takeover require an interactive terminal and are deliberately absent from the
AI-capable control surface.

## Testing and acceptance

Unit tests use fake transports and deterministic clocks to cover:

- valid and invalid state transitions;
- proposal expiry, single-use approval, rejection, and session invalidation;
- proposal-to-transaction ID linkage and immutable command snapshots;
- timeout, reconnect paths, disconnect, Ctrl-C, and human takeover;
- invalidation and prevention of replay after daemon restart;
- managed-command validation, nonce matching, and marker extraction;
- rejection of multiline, unbalanced, heredoc, and background commands;
- initial-login and ad hoc secret redaction, echoed-secret suppression,
  redaction timeout behavior, and capability boundaries;
- ring-buffer wraparound, slow consumers, and bounded backpressure;
- advisory interactive-command warnings without treating them as enforcement.

Linux integration tests use pseudo-terminals to simulate a UART target,
including boot output, `login:`, optional `Password:`, shell prompts, reboot,
large output bursts, and disconnects. SSH integration tests run against a
controlled local test server and cover PTY state, key/password authentication,
known-host behavior, and reconnect semantics.

Hardware acceptance is divided into:

1. UART selection, manual terminal behavior, repeated approved AI commands,
   and the normative responsiveness test defined above;
2. target reboot, boot-log capture, username-only login, and password login
   with verified secret omission;
3. SSH key/agent and password login, repeated commands, disconnect reporting,
   and approved reconnect.

## Deferred work

### One-shot U-Boot autoboot interruption

The future `arm-autoboot-interrupt` feature must require explicit one-time
authorization before reboot. Once armed, the local daemon may recognize a
configured autoboot prompt and send the interrupt byte immediately without an
AI round trip. The authorization is consumed on success or cleared on timeout.
It must never become a permanently enabled implicit behavior. This feature is
specified for compatibility but is not implemented in the first release.

### Windows support

The second platform phase will implement:

- COM port discovery and configuration;
- Windows Named Pipe IPC;
- Windows Console or ConPTY terminal integration;
- Windows-standard configuration, data, and log locations;
- Windows service behavior if background service installation is later
  required.

The core session, approval, command, transcript, and transport interfaces must
compile without Linux-specific assumptions from the first release. Windows is
an architectural compatibility target, not a first-release deliverable.

### Optional adapters

A future MCP server or other AI-specific adapter may translate its protocol to
the control CLI/API. It must retain the same read-and-propose capability and
must not gain approval or raw transport-write privileges.

## Non-goals

- Running the gateway or an AI model on the ARM target.
- Allowing AI clients to bypass human command approval.
- Simultaneously controlling one board through UART and SSH.
- Automatic UART-to-SSH failover.
- Automatic U-Boot interruption in the first release.
- AI automation of full-screen interactive applications.
- SFTP, SCP, SSH port forwarding, or firmware deployment.
- First-release Windows runtime support.
