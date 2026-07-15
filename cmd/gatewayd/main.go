// Command gatewayd is the daemon that owns the serial device or SSH
// connection for one board session. It is the only process allowed to
// do so; cmd/gateway talks to it over an owner-only Unix domain
// socket.
package main

import (
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
	AutoReadonly bool
}

type daemonServer interface {
	Serve() error
	Close() error
}
type daemonListenFunc func(string, ipc.Role, ipc.Dispatcher) (daemonServer, error)

func parseDaemonOptions(args []string) (daemonOptions, error) {
	var options daemonOptions
	flags := flag.NewFlagSet("gatewayd", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.AutoReadonly, "auto-readonly", false, "enable board diagnostic policy")
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
	_ = diagnosticPolicy

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

	drainStop := make(chan struct{})
	go runTranscriptDrain(coord, tw, board, drainStop)
	defer close(drainStop)

	reconcileStop := make(chan struct{})
	go runOpenSetReconciler(coord, open, aw, reconcileStop)
	defer close(reconcileStop)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	return serveDaemonSockets(d.Data, options.AutoReadonly, disp, sig,
		func(path string, role ipc.Role, dispatch ipc.Dispatcher) (daemonServer, error) {
			return ipc.Listen(path, role, dispatch)
		}, log.Default())
}

func serveDaemonSockets(dataDir string, autoReadonly bool, disp ipc.Dispatcher, stop <-chan os.Signal, listen daemonListenFunc, logger *log.Logger) error {
	diagnosePath := filepath.Join(dataDir, "gatewayd.diagnose.sock")
	if !autoReadonly {
		if err := os.Remove(diagnosePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove disabled diagnose socket: %w", err)
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
	servers := make([]daemonServer, 0, len(specs))
	defer func() {
		for _, srv := range servers {
			_ = srv.Close()
		}
	}()
	for _, spec := range specs {
		srv, err := listen(spec.path, spec.role, disp)
		if err != nil {
			return fmt.Errorf("%s listener: %w", spec.name, err)
		}
		servers = append(servers, srv)
	}
	if autoReadonly {
		logger.Printf("gatewayd: control=%s attach=%s diagnose=%s", specs[0].path, specs[1].path, specs[2].path)
	} else {
		logger.Printf("gatewayd: control=%s attach=%s", specs[0].path, specs[1].path)
	}
	errs := make(chan error, len(servers))
	for i, srv := range servers {
		name := specs[i].name
		go func() {
			if err := srv.Serve(); err != nil {
				errs <- fmt.Errorf("%s listener stopped: %w", name, err)
			}
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
