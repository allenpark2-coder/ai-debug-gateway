package gateway

import (
	"bytes"
	"regexp"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

func testLoginConfig() LoginConfig {
	return LoginConfig{
		Username:              "root",
		LoginPromptPattern:    mustCompile(`login:\s*$`),
		PasswordPromptPattern: mustCompile(`[Pp]assword:\s*$`),
		ShellPromptPattern:    mustCompile(`board\s*\$\s*$`),
		BootBannerPattern:     mustCompile(`Booting Linux`),
		SecretPromptPatterns:  []*regexp.Regexp{mustCompile(`\[sudo\] password`), mustCompile(`\(current\) UNIX password`)},
		SecretGracePeriod:     200 * time.Millisecond,
	}
}

func TestLoginSendsOnlyUsernameAtLoginPrompt(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}

	stream.feed([]byte("Welcome\nlogin: "))
	waitFor(t, time.Second, func() bool {
		return bytes.Contains(stream.writtenSoFar(), []byte("root\n"))
	})
	if bytes.Contains(stream.writtenSoFar(), []byte("password")) {
		t.Fatalf("must never send a password at the login prompt, wrote %q", stream.writtenSoFar())
	}

	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

func TestPasswordAndAdHocPromptsPauseForSecretWindow(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}

	stream.feed([]byte("login: "))
	waitFor(t, time.Second, func() bool { return bytes.Contains(stream.writtenSoFar(), []byte("root\n")) })

	stream.feed([]byte("Password: "))
	waitFor(t, time.Second, func() bool { return c.SecretActive() })
	if c.AIEnabled() {
		t.Fatal("AI must stay disabled while a secret prompt is open")
	}

	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return !c.SecretActive() })
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

func TestAdHocSudoPromptDuringSessionPausesSecretWindow(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	cfg := testLoginConfig()
	if err := c.StartUART(stream, cfg, nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	stream.feed([]byte("[sudo] password for root: "))
	waitFor(t, time.Second, func() bool { return c.SecretActive() })

	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return !c.SecretActive() })
}

func TestUnknownPromptLeavesAIDisabledUntilManualConfirm(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}

	stream.feed([]byte("some totally unrecognized banner\n"))
	time.Sleep(50 * time.Millisecond)
	if c.AIEnabled() {
		t.Fatal("AI must stay disabled for an unrecognized prompt")
	}
	// Raw human access must remain available regardless.
	if _, err := c.WriteHuman([]byte("\n")); err != nil {
		t.Fatal(err)
	}

	if err := c.ConfirmSessionReady(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

func TestTargetRebootPreservesSessionIDAndReturnsToAuthenticating(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	sessionBefore := c.SessionID()

	stream.feed([]byte("Booting Linux on physical CPU 0\n"))
	waitFor(t, time.Second, func() bool { return c.State() == session.Authenticating })

	if c.SessionID() != sessionBefore {
		t.Fatalf("target reboot must preserve the session ID, got %q want %q", c.SessionID(), sessionBefore)
	}
	if c.AIEnabled() {
		t.Fatal("AI must be disabled again after a target reboot until re-authentication completes")
	}

	// Re-authenticate after the simulated reboot.
	stream.feed([]byte("login: "))
	waitFor(t, time.Second, func() bool { return bytes.Contains(stream.writtenSoFar(), []byte("root\n")) })
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

func TestTargetRebootFinalizesActiveTransactionWithoutFabricatingExitCode(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	p, err := c.Propose("s1", "reboot", "reboot the board", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return bytes.Contains(stream.writtenSoFar(), []byte("reboot")) })

	stream.feed([]byte("Booting Linux on physical CPU 0\n"))
	waitFor(t, time.Second, func() bool { return c.State() == session.Authenticating })

	res, err := c.commands.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "target-rebooted" {
		t.Fatalf("got status %q, want target-rebooted", res.Status)
	}
	if res.ExitCode != nil {
		t.Fatalf("must not fabricate an exit code for a rebooting command, got %v", *res.ExitCode)
	}
}

func TestMarkerCompletesRunningCommandWithExitCode(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	p, err := c.Propose("s1", "false", "check exit code", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return c.active() != nil })

	m := c.activeMarker()
	stream.feed([]byte("GWMARK:" + tx.ID + ":" + m.nonce + ":1\n"))

	waitFor(t, time.Second, func() bool {
		res, err := c.commands.Result(tx.ID)
		return err == nil && res.Status == "completed"
	})
	res, _ := c.commands.Result(tx.ID)
	if res.ExitCode == nil || *res.ExitCode != 1 {
		t.Fatalf("got exit code %v, want 1", res.ExitCode)
	}
	waitFor(t, time.Second, func() bool { return c.State() == session.Ready })
}
