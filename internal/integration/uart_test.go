// Package integration exercises the gateway stack end to end against
// a real Linux pseudo-terminal standing in for a UART target, and
// against the real gatewayd/gateway binaries for the parts that need
// no physical hardware.
package integration

import (
	"bytes"
	"regexp"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/gateway"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// waitTimeout is generous on purpose: these tests drive a real PTY
// through select-based polling (both the coordinator's and the fake
// console's), so occasional OS scheduling jitter in a loaded sandbox
// can push an individual condition well past what a tight budget like
// 2s reliably allows even though nothing is actually stuck.
const waitTimeout = 5 * time.Second

func fakeLoginConfig() gateway.LoginConfig {
	return gateway.LoginConfig{
		Username:              "root",
		LoginPromptPattern:    regexp.MustCompile(`login:\s*$`),
		PasswordPromptPattern: regexp.MustCompile(`(?i)password:\s*$`),
		ShellPromptPattern:    regexp.MustCompile(`myboard \$\s*$`),
		BootBannerPattern:     regexp.MustCompile(`Booting Linux`),
		SecretPromptPatterns:  []*regexp.Regexp{regexp.MustCompile(`\[sudo\] password`)},
		SecretGracePeriod:     time.Second,
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func startAuthenticated(t *testing.T, c *gateway.Coordinator, fc *fakeConsole, opener gateway.Opener) {
	t.Helper()
	fc.run()
	if err := c.StartUART(fc.stream(), fakeLoginConfig(), opener); err != nil {
		t.Fatal(err)
	}
	waitFor(t, waitTimeout, func() bool { return c.AIEnabled() })
}

func TestFullUARTWorkflowProposeApprovePwd(t *testing.T) {
	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	fc := newFakeConsole(t)
	startAuthenticated(t, c, fc, nil)

	p, err := c.Propose("s1", "pwd", "check cwd", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, waitTimeout, func() bool {
		res, err := c.Result(tx.ID)
		return err == nil && res.Status == "completed"
	})
	res, err := c.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("got exit code %v, want 0", res.ExitCode)
	}

	chunk := c.ReadAfter(0, 1<<16)
	if !bytes.Contains(chunk.Data, []byte("/root")) {
		t.Fatalf("expected pwd's own output in the scoped transcript, got %q", chunk.Data)
	}
}

func TestFullUARTWorkflowNonZeroExitCode(t *testing.T) {
	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	fc := newFakeConsole(t)
	startAuthenticated(t, c, fc, nil)

	p, err := c.Propose("s1", "false", "check nonzero exit", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, waitTimeout, func() bool {
		res, err := c.Result(tx.ID)
		return err == nil && res.Status == "completed"
	})
	res, _ := c.Result(tx.ID)
	if res.ExitCode == nil || *res.ExitCode != 1 {
		t.Fatalf("got exit code %v, want 1", res.ExitCode)
	}
}

func TestEchoedSecretNeverReachesAIVisibleOutput(t *testing.T) {
	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	fc := newFakeConsole(t)
	// Fully complete the ordinary login round trip first (the fake
	// console auto-responds to the username the coordinator sends as
	// soon as it sees "login:"): injecting the ad hoc prompt below
	// before that round trip settles races the coordinator's own
	// automatic username send and the console's automatic shell-prompt
	// reply against this test's manual writes, with no guaranteed
	// ordering between them (confirmed: it does, intermittently,
	// exactly like an ad hoc "sudo" prompt appearing well after login
	// in real use).
	startAuthenticated(t, c, fc, nil)

	secret := "correct-horse-battery-staple"
	fc.write("Password: ")
	waitFor(t, waitTimeout, func() bool { return c.SecretActive() })

	// A misconfigured remote echoing the human's typed password back,
	// fragmented across several writes.
	fc.write(secret[:10])
	fc.write(secret[10:])
	time.Sleep(50 * time.Millisecond)

	fc.write("\r\nmyboard $ ")
	waitFor(t, waitTimeout, func() bool { return c.AIEnabled() })

	chunk := c.ReadAfter(0, 1<<16)
	if bytes.Contains(chunk.Data, []byte(secret)) {
		t.Fatalf("secret leaked into AI-visible output: %q", chunk.Data)
	}
}

func TestTargetRebootPreservesSessionIDOverRealPTY(t *testing.T) {
	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	fc := newFakeConsole(t)
	startAuthenticated(t, c, fc, nil)

	before := c.SessionID()

	p, err := c.Propose("s1", "reboot", "reboot the board", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, waitTimeout, func() bool { return c.State() == session.Authenticating })
	if c.SessionID() != before {
		t.Fatalf("target reboot must preserve the session ID, got %q want %q", c.SessionID(), before)
	}
	res, err := c.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "target-rebooted" || res.ExitCode != nil {
		t.Fatalf("got %+v, want target-rebooted with no exit code", res)
	}

	// The fake console re-sent its boot banner and login prompt; the
	// coordinator must be able to re-authenticate on the same session.
	waitFor(t, waitTimeout, func() bool { return bytes.Contains(c.ReadAfter(0, 1<<16).Data, []byte("login:")) })
	waitFor(t, waitTimeout, func() bool { return c.AIEnabled() })
	if c.SessionID() != before {
		t.Fatalf("session ID must still be preserved after re-authentication, got %q want %q", c.SessionID(), before)
	}
}

func TestUnplugAndRetryMintsNewSessionID(t *testing.T) {
	c := gateway.NewCoordinator("board-1")
	defer c.Stop()

	fc1 := newFakeConsole(t)
	var fc2 *fakeConsole
	opener := func() (transport.Stream, error) {
		fc2 = newFakeConsole(t)
		fc2.run()
		return fc2.stream(), nil
	}
	startAuthenticated(t, c, fc1, opener)
	oldID := c.SessionID()

	fc1.hangup() // simulates the USB-serial adapter disappearing
	waitFor(t, waitTimeout, func() bool { return c.State() == session.Reconnecting })

	if err := c.RetryUART(); err != nil {
		t.Fatal(err)
	}
	if c.SessionID() == oldID {
		t.Fatal("a human-approved retry must rotate the session ID")
	}
	waitFor(t, waitTimeout, func() bool { return c.AIEnabled() })
}
