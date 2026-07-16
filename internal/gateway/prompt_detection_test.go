package gateway

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// defaultShellPromptConfig mirrors the daemon's shipped login config:
// its loose `[#$]\s*$` shell prompt is what makes chunk-boundary false
// positives possible, so these regressions must be tested against it.
func defaultShellPromptConfig() LoginConfig {
	cfg := testLoginConfig()
	cfg.ShellPromptPattern = mustCompile(`[#$]\s*$`)
	return cfg
}

// A kernel version line ("... GNU ld (GNU Binutils) 2.44) #2 SMP ...")
// split by an arbitrary read boundary right after the '#' must not be
// mistaken for a shell prompt: on real hardware this authenticated the
// session while the target was still printing its boot log.
func TestBootLogChunkEndingInPromptCharDoesNotAuthenticate(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, defaultShellPromptConfig(), nil); err != nil {
		t.Fatal(err)
	}

	stream.feed([]byte("Linux version 6.12.61 (gcc 14.3.0, GNU ld 2.44) #"))
	stream.feed([]byte("2 SMP Tue Jul  7 12:37:38 CST 2026\n"))

	time.Sleep(400 * time.Millisecond)
	if c.AIEnabled() {
		t.Fatal("a boot-log line split at '#' must not authenticate the session")
	}

	// The real prompt, followed by line silence, must still authenticate.
	stream.feed([]byte("# "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

// A prompt split across two reads ("boa" + "rd $ ") must still be
// recognized: per-chunk matching sees neither half match.
func TestShellPromptSplitAcrossChunksStillAuthenticates(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}

	stream.feed([]byte("boa"))
	stream.feed([]byte("rd $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

// The design says the session is human-only while a secret window is
// open: an automatic diagnostic must be refused outright.
func TestDiagnoseStartRefusedWhileSecretWindowOpen(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	stream.feed([]byte("[sudo] password for root: "))
	waitFor(t, time.Second, func() bool { return c.SecretActive() })

	if _, err := c.DiagnoseStart(c.SessionID(), "uname", "probe", time.Second); !errors.Is(err, ErrNotReady) {
		t.Fatalf("diagnose must be refused while the secret window is open, got err=%v", err)
	}
	if bytes.Contains(stream.writtenSoFar(), []byte("uname")) {
		t.Fatal("no diagnostic bytes may reach the target while the secret window is open")
	}
}

// Replayed boot text inside command output (`dmesg`, `cat` of a boot
// log) must not finalize the running transaction as target-rebooted:
// on real hardware this killed a dmesg diagnostic while the board's
// uptime proved it never rebooted. The marker arriving afterwards is
// what proves it was output, not a reboot.
func TestBootBannerInsideCommandOutputCompletesWithMarker(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	p, err := c.Propose("s1", "dmesg", "inspect kernel log", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return bytes.Contains(stream.writtenSoFar(), []byte("dmesg")) })

	stream.feed([]byte("Booting Linux on physical CPU 0x0000000000 [0x410fd083]\n"))
	stream.feed([]byte("Machine model: some devboard\n"))

	time.Sleep(400 * time.Millisecond)
	if _, err := c.commands.Result(tx.ID); !errors.Is(err, command.ErrNotFound) {
		res, _ := c.commands.Result(tx.ID)
		t.Fatalf("banner inside output finalized the transaction early: %+v", res)
	}
	if c.State() != session.RunningCommand {
		t.Fatalf("session left RunningCommand on replayed banner, state=%s", c.State())
	}

	m := c.activeMarker()
	stream.feed([]byte("GWMARK:" + tx.ID + ":" + m.nonce + ":0\n"))
	waitFor(t, time.Second, func() bool {
		res, err := c.commands.Result(tx.ID)
		return err == nil && res.Status == command.StatusCompleted
	})
}

// A real reboot during a command still finalizes it as
// target-rebooted: the banner followed by a login prompt (and no
// marker) is the confirmation, and re-authentication proceeds.
func TestRealRebootBannerThenLoginPromptFinalizesAsTargetRebooted(t *testing.T) {
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
	stream.feed([]byte("board login: "))

	waitFor(t, time.Second, func() bool { return c.State() == session.Authenticating })
	res, err := c.commands.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != command.StatusTargetRebooted {
		t.Fatalf("got status %q, want %q", res.Status, command.StatusTargetRebooted)
	}
	// The login prompt that confirmed the reboot must also be answered.
	waitFor(t, time.Second, func() bool {
		return bytes.Count(stream.writtenSoFar(), []byte("root\n")) >= 1
	})
}
