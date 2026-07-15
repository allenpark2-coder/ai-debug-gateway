package gateway

import (
	"errors"
	"io"
	"syscall"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

func TestReadEOFEntersReconnecting(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	stream.failRead(io.EOF)
	waitFor(t, time.Second, func() bool { return c.State() == session.Reconnecting })
}

func TestENODEVAndEIOEnterReconnecting(t *testing.T) {
	for _, injected := range []error{syscall.ENODEV, syscall.EIO} {
		c := newTestCoordinator(t)
		stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
		if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
			t.Fatal(err)
		}
		stream.feed([]byte("board $ "))
		waitFor(t, time.Second, func() bool { return c.AIEnabled() })

		stream.failRead(injected)
		waitFor(t, time.Second, func() bool { return c.State() == session.Reconnecting })
	}
}

func TestTransportLossInvalidatesPendingProposalsAndFinalizesTransaction(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(stream, testLoginConfig(), nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	sessionBefore := c.SessionID()

	p, err := c.Propose("s1", "sleep 5", "long running", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return c.active() != nil })

	stray, err := c.Propose("s1", "uname -a", "unrelated pending proposal", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	stream.failRead(io.EOF)
	waitFor(t, time.Second, func() bool { return c.State() == session.Reconnecting })

	res, err := c.commands.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "disconnected" {
		t.Fatalf("got status %q, want disconnected", res.Status)
	}

	if got := c.commands.PendingForSession(sessionBefore); len(got) != 0 {
		t.Fatalf("expected no pending proposals after transport loss, got %+v (stray=%s)", got, stray.ID)
	}
}

func TestRetryUARTRotatesSessionOnTrustedIdentity(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	reopened := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})

	opener := func() (transport.Stream, error) { return reopened, nil }
	if err := c.StartUART(stream, testLoginConfig(), opener); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	oldID := c.SessionID()
	stream.failRead(io.EOF)
	waitFor(t, time.Second, func() bool { return c.State() == session.Reconnecting })

	if err := c.RetryUART(); err != nil {
		t.Fatal(err)
	}
	if c.SessionID() == oldID {
		t.Fatal("a human-approved retry must rotate the session ID")
	}
	waitFor(t, time.Second, func() bool { return c.State() == session.Connecting || c.State() == session.Authenticating })
}

func TestRetryUARTRequiresHumanSelectionWhenIdentityIsAmbiguous(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})

	opener := func() (transport.Stream, error) { return nil, ErrHumanSelectionRequired }
	if err := c.StartUART(stream, testLoginConfig(), opener); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	oldID := c.SessionID()
	stream.failRead(io.EOF)
	waitFor(t, time.Second, func() bool { return c.State() == session.Reconnecting })

	err := c.RetryUART()
	if !errors.Is(err, ErrHumanSelectionRequired) {
		t.Fatalf("got %v, want ErrHumanSelectionRequired", err)
	}
	if c.SessionID() != oldID {
		t.Fatal("a failed retry must not rotate the session ID")
	}
	if c.State() != session.Reconnecting {
		t.Fatalf("got state %s, want still RECONNECTING", c.State())
	}
}
