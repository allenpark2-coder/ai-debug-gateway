// Command gateway is the human attach terminal and the non-interactive
// control CLI. It never owns the serial device or SSH connection
// itself; gatewayd does, over an owner-only Unix domain socket this
// binary dials.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/cli"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport/serial"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/xdgpaths"
)

// version and commit are set at build time via -ldflags "-X main.version=... -X main.commit=..."
// by scripts/build-release.sh; a plain `go build` leaves them at these defaults.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	dirs, err := xdgpaths.Resolve()
	if err != nil {
		fatal(err)
	}
	controlSock := filepath.Join(dirs.Data, "gatewayd.control.sock")
	attachSock := filepath.Join(dirs.Data, "gatewayd.attach.sock")
	diagnoseSock := filepath.Join(dirs.Data, "gatewayd.diagnose.sock")
	unsafeShellSock := filepath.Join(dirs.Data, "gatewayd.unsafeshell.sock")
	if err := runCLI(os.Args[1:], socketPaths{Control: controlSock, Attach: attachSock, Diagnose: diagnoseSock, UnsafeShell: unsafeShellSock, ProfileDir: filepath.Join(dirs.Config, "profiles")}, cli.Dial, os.Stdout, os.Stderr); err != nil {
		fatal(err)
	}
}

type socketPaths struct{ Control, Attach, Diagnose, UnsafeShell, ProfileDir string }
type clientDialer func(string) (*cli.Client, error)

const maxConfirmationBytes = 4 * 1024

func runCLI(argv []string, sockets socketPaths, dial clientDialer, stdout, stderr io.Writer) error {
	if len(argv) == 0 {
		return fmt.Errorf("command is required")
	}

	args := argv[1:]
	switch argv[0] {
	case "version":
		_, err := fmt.Fprintf(stdout, "gateway version=%s commit=%s\n", version, commit)
		return err
	case "ports":
		return runClient(sockets.Control, dial, func(c *cli.Client) error { data, err := c.PortsList(); return printResultTo(stdout, data, err) })
	case "profile":
		if len(args) < 1 || args[0] != "create" {
			usage()
			os.Exit(2)
		}
		return profileCreate(sockets.ProfileDir)
	case "start":
		board := flagValue(args, "--board")
		opts := cli.SessionStartOptions{
			Transport:     flagValue(args, "--transport"),
			SSHPassword:   flagValue(args, "--ssh-password"),
			SSHAcceptHost: hasFlag(args, "--ssh-accept-host"),
		}
		if opts.Transport == "ssh" && opts.SSHPassword == "" && term.IsTerminal(int(os.Stdin.Fd())) {
			opts.SSHPassword = promptPassword("SSH password (leave blank to skip)")
		}
		// Dialed on the attach socket, not control: host-key
		// acceptance for a brand new host only ever succeeds on an
		// attach connection, and an operator typing this command is,
		// by definition, at an interactive terminal.
		return runClient(sockets.Attach, dial, func(c *cli.Client) error {
			data, err := c.SessionStartWithOptions(board, opts)
			return printResultTo(stdout, data, err)
		})
	case "status":
		return runClient(sockets.Control, dial, func(c *cli.Client) error { data, err := c.SessionStatus(); return printResultTo(stdout, data, err) })
	case "output":
		after := parseUint(flagValue(args, "--after"))
		return runClient(sockets.Control, dial, func(c *cli.Client) error {
			data, err := c.OutputRead(after, 64*1024)
			return printResultTo(stdout, data, err)
		})
	case "propose":
		text := flagValue(args, "--text")
		purpose := flagValue(args, "--purpose")
		timeoutMS := parseInt(flagValue(args, "--timeout-ms"))
		session := flagValue(args, "--session")
		return runClient(sockets.Control, dial, func(c *cli.Client) error {
			data, err := c.CommandPropose(session, text, purpose, timeoutMS)
			return printResultTo(stdout, data, err)
		})
	case "diagnose":
		req := cli.DiagnoseRequest{SessionID: flagValue(args, "--session"), Text: flagValue(args, "--text"), Purpose: flagValue(args, "--purpose"), TimeoutMS: parseInt(flagValue(args, "--timeout-ms"))}
		if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.Text) == "" || strings.TrimSpace(req.Purpose) == "" || req.TimeoutMS <= 0 {
			return fmt.Errorf("diagnose requires nonempty --session, --text, --purpose, and positive --timeout-ms")
		}
		if req.TimeoutMS > command.MaxDiagnosticTimeoutMS {
			return fmt.Errorf("diagnose --timeout-ms must not exceed %d", command.MaxDiagnosticTimeoutMS)
		}
		if _, err := fmt.Fprintln(stderr, req.Text); err != nil {
			return fmt.Errorf("display diagnostic command: %w", err)
		}
		return runClient(sockets.Diagnose, dial, func(c *cli.Client) error {
			result, err := c.DiagnoseExecute(req)
			if err != nil {
				return err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(stdout, string(data))
			return err
		})
	case "unsafe-shell":
		req := cli.UnsafeShellRequest{SessionID: flagValue(args, "--session"), Text: flagValue(args, "--text"), Purpose: flagValue(args, "--purpose"), TimeoutMS: parseInt(flagValue(args, "--timeout-ms"))}
		if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.Text) == "" || strings.TrimSpace(req.Purpose) == "" || req.TimeoutMS <= 0 {
			return fmt.Errorf("unsafe-shell requires nonempty --session, --text, --purpose, and positive --timeout-ms")
		}
		if req.TimeoutMS > command.MaxDiagnosticTimeoutMS {
			return fmt.Errorf("unsafe-shell --timeout-ms must not exceed %d", command.MaxDiagnosticTimeoutMS)
		}
		if _, err := fmt.Fprintln(stderr, req.Text); err != nil {
			return fmt.Errorf("display unsafe-shell command: %w", err)
		}
		return runClient(sockets.UnsafeShell, dial, func(c *cli.Client) error {
			result, err := c.UnsafeShellExecute(req)
			if err != nil {
				return err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(stdout, string(data))
			return err
		})
	case "approve":
		proposal, confirmation := flagValue(args, "--proposal"), flagValue(args, "--confirmation")
		if strings.TrimSpace(proposal) == "" || strings.TrimSpace(confirmation) == "" {
			return fmt.Errorf("approve requires nonempty --proposal and --confirmation")
		}
		if len(confirmation) > maxConfirmationBytes {
			return fmt.Errorf("confirmation exceeds %d bytes", maxConfirmationBytes)
		}
		return runClient(sockets.Attach, dial, func(c *cli.Client) error {
			data, err := c.CommandApproveWithConfirmation(proposal, confirmation)
			return printResultTo(stdout, data, err)
		})
	case "export":
		return runClient(sockets.Control, dial, func(c *cli.Client) error { data, err := c.RecordsExport(0, 0); return printResultTo(stdout, data, err) })
	case "attach":
		runAttach(sockets.Attach)
		return nil
	default:
		return fmt.Errorf("unknown command %q", argv[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: gateway <command> [flags]

commands:
  version                                   print the build version and commit
  ports                                    list discovered serial ports
  profile create                           interactively create a board profile
  start [--board NAME] [--transport uart|ssh] [--ssh-password PW] [--ssh-accept-host]
                                            start a session
  status                                   report session state
  output --after N                         read console output after sequence N
  propose --session ID --text TEXT --purpose P --timeout-ms MS
  diagnose --session ID --text TEXT --purpose P --timeout-ms MS
  unsafe-shell --session ID --text TEXT --purpose P --timeout-ms MS
  approve --proposal ID --confirmation NOTE
  export                                   export transcript/audit records
  attach                                   attach the interactive terminal`)
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// promptPassword reads a password from the terminal without echoing
// it. It returns "" (never an error) if reading fails or the human
// left it blank, matching the "leave blank to skip" flows that call
// it: a blank password just means this method is not being used.
func promptPassword(label string) string {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseUint(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func parseInt(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gateway:", err)
	os.Exit(1)
}

func runClient(socketPath string, dial clientDialer, fn func(*cli.Client) error) error {
	c, err := dial(socketPath)
	if err != nil {
		return err
	}
	defer c.Close()
	return fn(c)
}

func printResultTo(w io.Writer, data json.RawMessage, err error) error {
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func runAttach(socketPath string) {
	c, err := cli.Dial(socketPath)
	if err != nil {
		fatal(err)
	}
	defer c.Close()

	restore, err := enterRawMode(os.Stdin)
	if err != nil {
		fatal(err)
	}
	defer restore()

	if err := cli.Attach(c, os.Stdin, os.Stdout, cli.DefaultAttachOptions()); err != nil {
		restore()
		fatal(err)
	}
}

func profileCreate(profileDir string) error {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}

	ports, err := serial.List()
	if err != nil {
		return err
	}
	fmt.Println("discovered ports:")
	for i, p := range ports {
		fmt.Printf("  [%d] %s (by-id: %s)\n", i, p.Path, p.ByIDPath)
	}

	stdin := bufio.NewReader(os.Stdin)
	name := prompt(stdin, "profile name")
	idx := parseInt(prompt(stdin, "port index"))
	if idx < 0 || int(idx) >= len(ports) {
		return fmt.Errorf("gateway: port index %d out of range", idx)
	}
	baud := parseInt(prompt(stdin, "baud rate (e.g. 115200)"))
	dataBits := parseInt(prompt(stdin, "data bits (5-8, e.g. 8)"))
	parity := prompt(stdin, "parity (none/odd/even)")
	stopBits := parseInt(prompt(stdin, "stop bits (1 or 2)"))
	flow := prompt(stdin, "flow control (none/hardware/software)")

	line := serial.LineSettings{
		BaudRate: int(baud),
		DataBits: int(dataBits),
		Parity:   serial.Parity(parity),
		StopBits: int(stopBits),
		Flow:     serial.FlowControl(flow),
	}
	if err := line.Validate(); err != nil {
		return err
	}

	port := ports[idx]
	if !port.Identity().Known() {
		return fmt.Errorf("gateway: port %q has no stable USB identity; refusing to save a profile that could later match a different device", port.Path)
	}

	return profile.Save(profileDir, profile.Profile{
		Name: name,
		UART: &profile.UARTConfig{Identity: port.Identity(), Line: line},
	})
}

func prompt(r *bufio.Reader, label string) string {
	fmt.Printf("%s: ", label)
	line, _ := r.ReadString('\n')
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}

// enterRawMode puts f into raw terminal mode (no-op if f is not a
// terminal, e.g. piped test input) and arms SIGINT/SIGTERM handling so
// the terminal is restored on every exit path, not only a clean
// return. The returned restore func is idempotent and safe to call
// from both the normal defer and the signal handler.
func enterRawMode(f *os.File) (restore func(), err error) {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}

	restored := false
	restoreOnce := func() {
		if restored {
			return
		}
		restored = true
		_ = term.Restore(fd, oldState)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			restoreOnce()
			os.Exit(130)
		case <-done:
		}
	}()

	return func() {
		close(done)
		signal.Stop(sig)
		restoreOnce()
	}, nil
}
