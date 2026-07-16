# Windows runtime design

## Goal and scope

Add a native Windows runtime for `ai-debug-gateway` without changing the Linux
security or session model. The first Windows release supports UART and SSH on
Windows 10 and Windows 11 x64. It ships as a portable ZIP and runs
`gatewayd.exe` in the foreground from PowerShell or Windows Terminal.

The target release is `v0.2.0`. MSI packaging is intentionally deferred until
the portable runtime passes COM4 hardware acceptance. The portable layout is
kept stable so a later WiX package can install the same files without changing
runtime behavior.

Windows Service installation, ARM64, automatic U-Boot interruption, driver
installation, code signing, file transfer, and MSI generation are outside this
phase.

## Shared architecture

Windows and Linux use the same platform-independent implementations for:

- session state and command lifecycle;
- coordinator behavior for UART and SSH;
- proposal, approval, timeout, reconnect, transcript, and audit semantics;
- secret redaction and auto-readonly policy;
- profile data model and protocol v1 messages;
- the control, attach, and diagnose capability matrix.

Platform behavior stays behind build-tagged files. The intended organization
is:

```text
internal/ipc/
  client_linux.go
  server_linux.go
  client_windows.go
  server_windows.go

internal/transport/serial/
  discovery_linux.go
  discovery_windows.go
  open_linux.go
  open_windows.go

internal/xdgpaths/
  xdgpaths_linux.go
  paths_windows.go
```

Equivalent build-tagged files provide terminal handling, single-instance
locking, signal/console-event handling, and any path-formatting behavior in the
two command packages. Shared packages must not import Linux or Windows APIs.

## COM port discovery and stable identity

`go.bug.st/serial` remains the serial backend. Its Windows implementation
opens COM ports with overlapped I/O and its enumerator already exposes COM
name, VID, PID, USB serial number, manufacturer, and product.

Windows discovery adds SetupAPI/Configuration Manager access for the complete
Device Instance ID. A displayed port contains:

```text
COM4
Product: Silicon Labs CP2102
VID: 10C4
PID: EA60
Serial: 0001
Device Instance ID: USB\VID_10C4&PID_EA60\0001
```

The profile never treats `COM4` as permanent identity. It stores the stable
Device Instance ID and the USB VID, PID, and serial metadata used to explain
or validate the match. At session start the daemon enumerates present devices
and resolves that identity to the current COM name.

Matching rules are:

1. Prefer an exact Device Instance ID match.
2. Use exact VID + PID + nonempty USB serial only for profiles migrated from a
   representation that lacks Device Instance ID.
3. Reject absent, changed, or ambiguous matches rather than selecting a COM
   number heuristically.
4. Refuse creation of a new UART profile when no stable identity is available.

Replugging a device may change COM4 to COM5 without invalidating its profile.
Two indistinguishable devices, missing stable identity, or SetupAPI failure
produce actionable errors and never fall back silently.

The existing UART line settings remain baud, data bits, parity, stop bits, and
flow control. The Windows backend must either apply a requested setting or
reject it; it must not silently ignore flow control or another line setting.

## Windows IPC and capability security

Unix domain sockets are replaced by three local Windows Named Pipes:

```text
\\.\pipe\ai-debug-gateway-<user-sid>-control
\\.\pipe\ai-debug-gateway-<user-sid>-attach
\\.\pipe\ai-debug-gateway-<user-sid>-diagnose
```

The user SID is part of the name for diagnostics and collision avoidance, but
the security boundary is the pipe security descriptor. Each pipe grants access
only to the current user SID and the required Windows system identities. It
must not rely on the pipe name or a client-supplied role.

Each listener assigns its role before dispatch, exactly like the Linux socket
listeners. Control clients cannot approve or write; diagnose clients only use
the existing status, bounded output, and atomic diagnose operations; attach
clients retain the human-only capabilities. Frames retain protocol v1's
newline-delimited JSON and one-MiB limit.

Server shutdown closes listening pipe instances and active client connections,
cancels in-flight diagnostic waits, and waits for handlers without deadlock.
A broken client connection never replays a request. Owner-only file modes used
on Linux are replaced by Windows ACL checks and tests.

## Single instance and runtime paths

Windows uses a per-user named mutex to ensure one `gatewayd.exe` instance owns
one data directory. A second process fails immediately with a clear message.
The mutex contains the user SID and a stable digest of the selected data path
so deliberately isolated test/data roots do not conflict.

Default roots use Windows Known Folders beneath `%LOCALAPPDATA%`:

```text
%LOCALAPPDATA%\ai-debug-gateway\config\profiles\<board>.json
%LOCALAPPDATA%\ai-debug-gateway\config\policies\<board>.json
%LOCALAPPDATA%\ai-debug-gateway\data\transcript.jsonl
%LOCALAPPDATA%\ai-debug-gateway\data\audit.jsonl
%LOCALAPPDATA%\ai-debug-gateway\data\open-transactions.json
```

Tests and advanced users may override roots through the existing configuration
mechanism, adapted to Windows path rules. Directories and files receive
current-user-only ACLs where the process creates them. Profiles never contain
passwords or private-key passphrases.

## Windows console attach

`gateway attach` supports Windows Terminal and PowerShell consoles. It switches
stdin to a raw-equivalent console mode, restores the original mode on every
exit path, and preserves the current interaction model:

- ordinary keys go to the target;
- `Ctrl-]` enters local command mode;
- doubled `Ctrl-]` sends one literal escape byte;
- `Ctrl-C` can interrupt a running target command;
- approve, reject, edit, secret, retry, takeover, detach, and end behave as on
  Linux.

The implementation uses native Windows console APIs for console handles and
keeps a non-console fallback for redirected stdin/stdout. ConPTY is not
required merely to attach to the gateway; it is used only if tests prove a
native console API cannot preserve the existing byte-stream semantics.

Console close, logoff, shutdown, and `Ctrl-C` events initiate the same bounded
daemon cleanup as Linux signals. No handler performs unbounded blocking work
inside the Windows callback itself.

## UART and SSH behavior

UART starts by resolving stable identity to the current COM name, then opens
the port exclusively. If PuTTY, Tera Term, or another process owns it, the
error identifies the COM port and explains that it is busy or access was
denied. USB removal enters `RECONNECTING`; only the existing human-approved
`retry uart` operation may resume, and the new connection receives a new
session ID.

SSH reuses the existing Go SSH transport and authentication ordering. Windows
path handling supports identity files and `known_hosts` under normal user
locations. Passwords and key passphrases stay transient and redacted. Agent
support is enabled only when a compatible Windows SSH agent endpoint is
detected; absence of an agent falls through to configured keys/password rather
than failing unrelated authentication methods.

One board still uses exactly one active transport. Switching between UART and
SSH ends the previous session and creates a new session ID.

## CLI workflow

The Windows commands intentionally match Linux:

```powershell
.\gateway.exe ports
.\gateway.exe profile create

# Terminal 1
.\gatewayd.exe

# Terminal 2
.\gateway.exe start --board myboard --transport uart
.\gateway.exe attach
```

Auto-readonly remains opt-in:

```powershell
.\gatewayd.exe --auto-readonly
.\gateway.exe diagnose `
  --session <ID> `
  --text "ps" `
  --purpose "list processes" `
  --timeout-ms 15000
```

The daemon, not the Windows client, enforces diagnostic policy. Manual mode
remains available and is the default.

## Error handling

Windows errors retain their native details while adding operator context:

- COM port busy or access denied names the current COM port;
- stable device missing or ambiguous names the stored identity and candidates;
- Named Pipe ACL, creation, connection, or frame failures name the pipe role;
- mutex collision explains that another daemon owns the selected data root;
- invalid profile, policy, identity-file, and known-hosts paths name the exact
  file;
- console-mode failures restore any mode already changed before returning.

Daemon or client restart never replays pending or running transactions. The
existing recovery record finalizes incomplete transactions as
`daemon-restarted` before accepting clients.

## Portable distribution

Linux produces the first portable artifact by cross-compiling Windows x64
binaries:

```text
dist/ai-debug-gateway-v0.2.0-windows-amd64/
  gateway.exe
  gatewayd.exe
  README-Windows.md
  LICENSE
```

The release process also produces a ZIP and SHA-256 checksum. The ZIP requires
no Administrator rights and makes no registry or PATH changes. Operators copy
the ZIP through a shared folder if desired, then extract it to a local Windows
directory before use.

The directory layout and version metadata are stable inputs for the deferred
WiX MSI. MSI generation, upgrades, signing, and PATH registration are not part
of this phase.

## Verification and acceptance

Verification has three layers.

### Linux development and CI

- Run the existing Linux race and integration suite unchanged.
- Cross-compile the complete Windows runtime, not only core packages, for
  `GOOS=windows GOARCH=amd64`.
- Build the portable directory, ZIP, and deterministic SHA-256 checksum.
- Verify Windows-specific packages do not enter Linux binaries.

### Windows CI

- Run protocol/core/profile/policy tests natively.
- Test Named Pipe role isolation, ACLs, frame bounds, concurrent clients,
  cancellation, and shutdown.
- Test known-folder paths, ACL creation, and the per-user mutex.
- Test console mode restoration and command-mode parsing with redirected and
  console handles.
- Test SSH against a local server.
- Test serial discovery and identity matching with injectable SetupAPI data;
  hardware-free tests must not weaken production discovery.
- Verify portable ZIP contents and clean execution outside a source checkout.

### COM4 hardware acceptance

On the user's Windows 10/11 x64 machine and USB-UART currently enumerated as
COM4:

1. List COM4 with product and stable identity.
2. Create a 115200 8N1, no-flow-control profile.
3. Start and attach to the Linux console.
4. Exercise manual input, detach/reattach, proposal approval, and secret entry.
5. Run an auto-readonly diagnostic and verify scoped output.
6. Unplug and replug the adapter; accept a COM-number change and resolve by
   identity.
7. Verify `retry uart` creates a new session ID.
8. Verify a second serial program produces a clear busy error.
9. Start an SSH session and repeat a managed command.
10. Confirm transcript and audit contain no supplied secret canary.

Portable Windows v1 is accepted only when Linux verification, Windows-native
CI, and COM4 hardware acceptance all pass.

## Deferred work

- WiX MSI packaging and installer lifecycle;
- Windows Service installation;
- code signing and SmartScreen reputation;
- Windows ARM64;
- automatic driver installation;
- automatic U-Boot interruption;
- file transfer and forwarding.
