package gateway

import (
	"bytes"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// TestEchoedSecretNeverReachesRingEvenOnFirstChunk is a regression
// test: the reader loop must check the secret window before the very
// first chunk that arrives while it is open ever reaches the ring (and
// therefore AI polling and the durable transcript drain), not only
// from the second chunk onward.
func TestEchoedSecretNeverReachesRingEvenOnFirstChunk(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}

	stream.feed([]byte("login: "))
	waitFor(t, time.Second, func() bool { return bytes.Contains(stream.writtenSoFar(), []byte("root\n")) })

	// The prompt chunk that OPENS the window is safe to keep (it is
	// not itself secret) and must still reach the ring so a human can
	// see the prompt was shown.
	stream.feed([]byte("Password: "))
	waitFor(t, time.Second, func() bool { return c.SecretActive() })
	if got := c.ReadAfter(0, 1<<16); !bytes.Contains(got.Data, []byte("Password:")) {
		t.Fatalf("the prompt itself must still be visible, got %q", got.Data)
	}

	secret := []byte("hunter2secretvalue")
	before := c.ring.Len()
	stream.feed(secret) // simulates a misconfigured remote echoing the typed password back
	waitFor(t, time.Second, func() bool { return c.ring.Len() > before })

	got := c.ReadAfter(0, 1<<16)
	if bytes.Contains(got.Data, secret) {
		t.Fatalf("echoed secret leaked into the ring on the first chunk after the window opened: %q", got.Data)
	}
}

func TestTakeoverInterruptsRunningTransactionAndRestoresControl(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	p, err := c.Propose("s1", "sleep 5", "long running", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return c.active() != nil })

	if err := c.Takeover(); err != nil {
		t.Fatal(err)
	}

	res, err := c.commands.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "interrupted-by-user" {
		t.Fatalf("got status %q, want interrupted-by-user", res.Status)
	}
	waitFor(t, time.Second, func() bool { return c.State() == session.Ready })

	// Takeover on an idle coordinator must succeed as a no-op.
	if err := c.Takeover(); err != nil {
		t.Fatal(err)
	}
}

func TestManualSecretBeginAndEndForUnrecognizedPrompt(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}

	stream.feed([]byte("login: "))
	waitFor(t, time.Second, func() bool { return bytes.Contains(stream.writtenSoFar(), []byte("root\n")) })

	// An unrecognized, custom prompt: no configured pattern matches it.
	stream.feed([]byte("Enter the vault passphrase >> "))
	time.Sleep(30 * time.Millisecond)
	if c.SecretActive() {
		t.Fatal("an unrecognized prompt must not auto-open the secret window")
	}

	c.BeginSecret()
	if !c.SecretActive() {
		t.Fatal("BeginSecret must open the window")
	}

	c.EndSecret()
	if c.SecretActive() {
		t.Fatal("EndSecret must close the window")
	}
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}
