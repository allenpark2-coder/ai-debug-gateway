package gateway

import (
	"bytes"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// retryLoginConfig shortens the nudge period so the tests do not wait
// out the production default.
func retryLoginConfig() LoginConfig {
	cfg := testLoginConfig()
	cfg.AuthRetryPeriod = 50 * time.Millisecond
	cfg.AuthRetryLimit = 3
	return cfg
}

// A kernel message printed right after the login prompt is the real
// failure this guards: the message carries a line break, so
// advanceLineTailLocked discards the tail holding "login: " before the
// pattern can match, and getty never reprints. Without a nudge the
// session stays in AUTHENTICATING forever.
func TestKernelMessageAfterLoginPromptStillAuthenticates(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, retryLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}

	// The prompt and the message arrive as one chunk, exactly as the
	// UART delivers them.
	stream.feed([]byte("buildroot login: bcmgenet eth0: Link is Up - 1Gbps/Full\r\n"))

	// Nothing can match yet, so the username must not have gone out.
	time.Sleep(20 * time.Millisecond)
	if bytes.Contains(stream.writtenSoFar(), []byte("root\n")) {
		t.Fatalf("username sent with no matchable prompt: %q", stream.writtenSoFar())
	}

	// The nudge fires, getty reprints, and the retry answers it.
	waitFor(t, time.Second, func() bool {
		return bytes.Contains(stream.writtenSoFar(), []byte("\n"))
	})
	stream.feed([]byte("buildroot login: "))
	waitFor(t, time.Second, func() bool {
		return bytes.Contains(stream.writtenSoFar(), []byte("root\n"))
	})

	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

// A console parked on a stale "Password:" says nothing more, so no read
// chunk ever arrives to drive the state machine. The first nudge has to
// be armed when the session enters AUTHENTICATING, not on incoming
// bytes.
func TestSilentConsoleIsNudgedAndRecovers(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, retryLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}

	// No feed at all: the board is silent.
	waitFor(t, time.Second, func() bool {
		return bytes.Contains(stream.writtenSoFar(), []byte("\n"))
	})

	stream.feed([]byte("\r\nbuildroot login: "))
	waitFor(t, time.Second, func() bool {
		return bytes.Contains(stream.writtenSoFar(), []byte("root\n"))
	})
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

// The nudge must never become an endless typist: a board that is simply
// off has to look stuck instead.
func TestAuthRetryStopsAtLimit(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	cfg := retryLoginConfig()
	if err := c.StartUART(stream, cfg, nil); err != nil {
		t.Fatal(err)
	}

	// Let every allowed nudge fire, then some.
	time.Sleep(cfg.AuthRetryPeriod * time.Duration(cfg.AuthRetryLimit+4))

	if got := bytes.Count(stream.writtenSoFar(), []byte("\n")); got > cfg.AuthRetryLimit {
		t.Fatalf("wrote %d newlines, limit is %d: %q",
			got, cfg.AuthRetryLimit, stream.writtenSoFar())
	}
	if c.AIEnabled() {
		t.Fatal("a board that never answered must not be reported as ready")
	}
}

// An open secret window means someone is being asked for a password.
// Typing a newline into that would submit an empty secret, so the nudge
// must hold off until the window closes.
func TestAuthRetryDoesNotInterruptSecretWindow(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	cfg := retryLoginConfig()
	if err := c.StartUART(stream, cfg, nil); err != nil {
		t.Fatal(err)
	}

	stream.feed([]byte("login: "))
	waitFor(t, time.Second, func() bool {
		return bytes.Contains(stream.writtenSoFar(), []byte("root\n"))
	})
	before := len(stream.writtenSoFar())

	// The password prompt opens the secret window.
	stream.feed([]byte("\r\nPassword: "))
	time.Sleep(cfg.AuthRetryPeriod * time.Duration(cfg.AuthRetryLimit+2))

	if got := stream.writtenSoFar(); len(got) != before {
		t.Fatalf("wrote %q into an open secret window", got[before:])
	}
}
