// Command gatewayd is the daemon that owns the serial device or SSH
// connection for one board session. It is the only process allowed to
// do so; cmd/gateway talks to it over an owner-only Unix domain
// socket.
package main

import (
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
)

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
	board := os.Getenv("GATEWAYD_BOARD")
	if board == "" {
		board = "default"
	}
	username := os.Getenv("GATEWAYD_USERNAME")
	if username == "" {
		username = "root"
	}

	d, err := resolveXDGDirs()
	if err != nil {
		return err
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

	drainStop := make(chan struct{})
	go runTranscriptDrain(coord, tw, board, drainStop)
	defer close(drainStop)

	controlSock := filepath.Join(d.Data, "gatewayd.control.sock")
	attachSock := filepath.Join(d.Data, "gatewayd.attach.sock")

	controlSrv, err := ipc.Listen(controlSock, ipc.RoleControl, disp)
	if err != nil {
		return err
	}
	defer controlSrv.Close()

	attachSrv, err := ipc.Listen(attachSock, ipc.RoleAttach, disp)
	if err != nil {
		return err
	}
	defer attachSrv.Close()

	go func() {
		if err := controlSrv.Serve(); err != nil {
			log.Printf("gatewayd: control listener stopped: %v", err)
		}
	}()
	go func() {
		if err := attachSrv.Serve(); err != nil {
			log.Printf("gatewayd: attach listener stopped: %v", err)
		}
	}()

	log.Printf("gatewayd: board=%q control=%s attach=%s", board, controlSock, attachSock)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Print("gatewayd: shutting down")
	return nil
}
