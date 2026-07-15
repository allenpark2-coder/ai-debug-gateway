// Command gatewayd is the daemon that owns the serial device or SSH
// connection for one board session. It is the only process allowed to
// do so; cmd/gateway talks to it over an owner-only Unix domain
// socket.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/audit"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/transcript"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/gateway"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/policy"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/xdgpaths"
)

type daemonOptions struct {
	AutoReadonly    bool
	UnsafeAutoShell string
}

type daemonServer interface {
	Serve() error
	Close() error
}
type daemonListenFunc func(string, ipc.Role, ipc.Dispatcher) (daemonServer, error)
type daemonShutdown interface{ Shutdown() }

type listenerGroup []daemonServer

func (g listenerGroup) Close() error {
	errs := make(chan error, len(g))
	for _, srv := range g {
		go func() { errs <- srv.Close() }()
	}
	var joined []error
	for range g {
		if err := <-errs; err != nil {
			joined = append(joined, err)
		}
	}
	return errors.Join(joined...)
}

func parseDaemonOptions(args []string) (daemonOptions, error) {
	var options daemonOptions
	flags := flag.NewFlagSet("gatewayd", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.AutoReadonly, "auto-readonly", false, "enable board diagnostic policy")
	flags.StringVar(&options.UnsafeAutoShell, "unsafe-auto-shell", "", "enable unattended denylist-mode shell execution for the named board; the board's risk_accepted unsafe-shell file is required")
	if err := flags.Parse(args); err != nil {
		return daemonOptions{}, err
	}
	if flags.NArg() != 0 {
		return daemonOptions{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return options, nil
}

func loadDaemonPolicy(options daemonOptions, configDir, board string) (*policy.Policy, error) {
	base := policy.Common()
	if !options.AutoReadonly {
		return base, nil
	}
	loaded, err := policy.LoadFile(filepath.Join(configDir, "policies", board+".json"), base)
	if err != nil {
		return nil, fmt.Errorf("board policy for %q: %w", board, err)
	}
	return loaded, nil
}

// loadUnsafeShellDaemonPolicy loads the denylist-mode policy only when
// options.UnsafeAutoShell names a board; otherwise it returns nil,nil so
// the unsafe-shell socket is never created. Deliberately a different
// directory from loadDaemonPolicy's policies/<board>.json, so this mode
// cannot be enabled by editing the file --auto-readonly already uses.
func loadUnsafeShellDaemonPolicy(options daemonOptions, configDir string) (*policy.DenylistPolicy, error) {
	if options.UnsafeAutoShell == "" {
		return nil, nil
	}
	loaded, err := policy.LoadUnsafeShellFile(filepath.Join(configDir, "unsafe-shell", options.UnsafeAutoShell+".json"))
	if err != nil {
		return nil, fmt.Errorf("unsafe-shell policy for %q: %w", options.UnsafeAutoShell, err)
	}
	return loaded, nil
}

// defaultLoginConfig is a reasonable first-release default for
// recognizing a Linux console login sequence. A future release makes
// these patterns per-profile configurable.
func defaultLoginConfig(username string) gateway.LoginConfig {
	return gateway.LoginConfig{
		Username:              username,
		LoginPromptPattern:    regexp.MustCompile(`(?i)login:\s*$`),
		PasswordPromptPattern: regexp.MustCompile(`(?i)password:\s*$`),
		ShellPromptPattern:    regexp.MustCompile(`[#$]\s*$`),
		BootBannerPattern:     regexp.MustCompile(`(?i)booting linux`),
		SecretPromptPatterns: []*regexp.Regexp{
			regexp.MustCompile(`\[sudo\] password`),
			regexp.MustCompile(`(?i)\(current\) UNIX password`),
		},
		SecretGracePeriod: 30 * time.Second,
	}
}

// transcriptDrainInterval bounds how far behind the durable transcript
// store can lag the live ring; it is asynchronous and decoupled from
// the transport read loop, per the design spec.
const transcriptDrainInterval = 100 * time.Millisecond

func runTranscriptDrain(coord *gateway.Coordinator, tw *transcript.Writer, board string, stop <-chan struct{}) {
	var lastSeq uint64
	ticker := time.NewTicker(transcriptDrainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			chunk := coord.ReadAfter(lastSeq, 1<<16)
			lastSeq = chunk.Next
			if len(chunk.Data) == 0 {
				continue
			}
			_, _ = tw.Append(transcript.Record{
				Board:     board,
				Session:   coord.SessionID(),
				Transport: "uart",
				Direction: "from-target",
				Source:    "daemon",
				Data:      chunk.Data,
			})
		}
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("gatewayd: %v", err)
	}
}

func run() error {
	options, err := parseDaemonOptions(os.Args[1:])
	if err != nil {
		return err
	}
	board := os.Getenv("GATEWAYD_BOARD")
	if board == "" {
		board = "default"
	}
	username := os.Getenv("GATEWAYD_USERNAME")
	if username == "" {
		username = "root"
	}

	d, err := xdgpaths.Resolve()
	if err != nil {
		return err
	}
	// Policies are deliberately loaded once during startup. The resulting
	// snapshot is handed to diagnose functionality in the startup graph.
	diagnosticPolicy, err := loadDaemonPolicy(options, d.Config, board)
	if err != nil {
		return err
	}
	// Unlike diagnosticPolicy above, a load failure here does not abort
	// startup: per the design (docs/superpowers/specs/2026-07-15-auto-
	// shell-denylist-design.md), an invalid or risk-not-accepted board
	// file disables only the unsafe-shell socket, never manual mode or
	// --auto-readonly.
	unsafeShellPolicy, unsafeShellErr := loadUnsafeShellDaemonPolicy(options, d.Config)
	unsafeShellEnabled := options.UnsafeAutoShell != ""
	if unsafeShellErr != nil {
		log.Printf("gatewayd: unsafe-shell mode disabled: %v", unsafeShellErr)
		unsafeShellPolicy = nil
		unsafeShellEnabled = false
	}

	lock, err := acquireLock(filepath.Join(d.Data, "gatewayd.lock"))
	if err != nil {
		return err
	}
	defer releaseLock(lock)

	aw := audit.NewWriter(filepath.Join(d.Data, "audit.jsonl"))
	tw := transcript.NewWriter(filepath.Join(d.Data, "transcript.jsonl"))

	open, err := loadOpenSet(filepath.Join(d.Data, "open-transactions.json"))
	if err != nil {
		return err
	}
	if err := recoverIncompleteTransactions(open, aw); err != nil {
		return err
	}

	coord := gateway.NewCoordinator(board)
	defer coord.Stop()

	profileDir := filepath.Join(d.Config, "profiles")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}

	disp := newDispatcher(board, profileDir, coord, open, aw, tw, defaultLoginConfig(username))
	disp.policy = diagnosticPolicy
	disp.unsafeShellPolicy = unsafeShellPolicy

	drainStop := make(chan struct{})
	go runTranscriptDrain(coord, tw, board, drainStop)
	defer close(drainStop)

	reconcileStop := make(chan struct{})
	go runOpenSetReconciler(coord, open, aw, reconcileStop)
	defer close(reconcileStop)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	return serveDaemonSockets(d.Data, board, options.AutoReadonly, unsafeShellEnabled, disp, sig,
		func(path string, role ipc.Role, dispatch ipc.Dispatcher) (daemonServer, error) {
			return ipc.Listen(path, role, dispatch)
		}, log.Default())
}

func serveDaemonSockets(dataDir, board string, autoReadonly, unsafeAutoShell bool, disp ipc.Dispatcher, stop <-chan os.Signal, listen daemonListenFunc, logger *log.Logger) (retErr error) {
	diagnosePath := filepath.Join(dataDir, "gatewayd.diagnose.sock")
	if !autoReadonly {
		if err := os.Remove(diagnosePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove disabled diagnose socket: %w", err)
		}
	}
	unsafeShellPath := filepath.Join(dataDir, "gatewayd.unsafeshell.sock")
	if !unsafeAutoShell {
		if err := os.Remove(unsafeShellPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove disabled unsafe-shell socket: %w", err)
		}
	}
	type listenerSpec struct {
		name, path string
		role       ipc.Role
	}
	specs := []listenerSpec{{"control", filepath.Join(dataDir, "gatewayd.control.sock"), ipc.RoleControl}, {"attach", filepath.Join(dataDir, "gatewayd.attach.sock"), ipc.RoleAttach}}
	if autoReadonly {
		specs = append(specs, listenerSpec{"diagnose", diagnosePath, ipc.RoleDiagnose})
	}
	if unsafeAutoShell {
		specs = append(specs, listenerSpec{"unsafeshell", unsafeShellPath, ipc.RoleUnsafeShell})
	}
	servers := make(listenerGroup, 0, len(specs))
	defer func() { retErr = errors.Join(retErr, servers.Close()) }()
	// This defer is registered after server Close, so LIFO teardown always
	// stops/cancels daemon work before waiting for connection handlers.
	defer func() {
		if shutdown, ok := disp.(daemonShutdown); ok {
			shutdown.Shutdown()
		}
	}()
	for _, spec := range specs {
		srv, err := listen(spec.path, spec.role, disp)
		if err != nil {
			return fmt.Errorf("%s listener: %w", spec.name, err)
		}
		servers = append(servers, srv)
	}
	logLine := fmt.Sprintf("gatewayd: board=%s control=%s attach=%s", board, specs[0].path, specs[1].path)
	for _, spec := range specs[2:] {
		logLine += fmt.Sprintf(" %s=%s", spec.name, spec.path)
	}
	logger.Print(logLine)
	errs := make(chan error, len(servers))
	for i, srv := range servers {
		name := specs[i].name
		go func() {
			err := srv.Serve()
			if err == nil {
				err = errors.New("Serve returned unexpectedly")
			}
			errs <- fmt.Errorf("%s listener stopped: %w", name, err)
		}()
	}
	select {
	case <-stop:
		logger.Print("gatewayd: shutting down")
		return nil
	case err := <-errs:
		return err
	}
}
