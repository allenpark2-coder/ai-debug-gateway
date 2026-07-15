package gateway

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

func TestStartSSHIsImmediatelyReadyNoLoginPromptNeeded(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "ssh-host", Key: "example:22"})

	if err := c.StartSSH(stream, nil); err != nil {
		t.Fatal(err)
	}
	// SSH authentication already completed during the handshake, before
	// Open ever returned a stream: no UART-style login prompt to wait
	// for.
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
	if c.State() != session.Ready {
		t.Fatalf("got %s, want READY", c.State())
	}
}

func TestSSHDisconnectFinalizesDisconnectedAndNeverAutoReconnects(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "ssh-host", Key: "example:22"})
	if err := c.StartSSH(stream, nil); err != nil {
		t.Fatal(err)
	}
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

	stray, err := c.Propose("s1", "uname -a", "unrelated pending proposal", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	stream.failRead(io.EOF)
	waitFor(t, time.Second, func() bool { return c.State() == session.Reconnecting })

	res, err := c.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "disconnected" {
		t.Fatalf("got status %q, want disconnected", res.Status)
	}
	if got := c.PendingForSession(c.SessionID()); len(got) != 0 {
		t.Fatalf("expected no pending proposals after disconnect, got %+v (stray=%s)", got, stray.ID)
	}

	// Never reconnects on network recovery alone: only a human-approved
	// RetrySSH may leave RECONNECTING.
	time.Sleep(100 * time.Millisecond)
	if c.State() != session.Reconnecting {
		t.Fatalf("got %s, want still RECONNECTING (no automatic reconnect)", c.State())
	}
}

func TestTransportExclusivityAcrossUARTAndSSH(t *testing.T) {
	c := newTestCoordinator(t)

	uartStream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartUART(uartStream, LoginConfig{ShellPromptPattern: mustCompile(`\$\s*$`)}, nil); err != nil {
		t.Fatal(err)
	}
	uartStream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
	uartID := c.SessionID()

	pending, err := c.Propose(uartID, "uname -a", "will be invalidated", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	sshStream := newFakeStream(transport.Identity{Kind: "ssh-host", Key: "example:22"})
	if err := c.StartSSH(sshStream, nil); !errors.Is(err, ErrTransportActive) {
		t.Fatalf("got %v, want ErrTransportActive while UART is active", err)
	}

	if err := c.EndSession(); err != nil {
		t.Fatal(err)
	}

	if err := c.StartSSH(sshStream, nil); err != nil {
		t.Fatal(err)
	}
	if c.SessionID() == uartID {
		t.Fatal("session ID reused across transport switch")
	}
	if got := c.PendingForSession(uartID); len(got) != 0 {
		t.Fatalf("old session's pending proposals not invalidated, got %+v (pending=%s)", got, pending.ID)
	}
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
}

func TestRetrySSHRotatesSessionAndNeverReplaysWrites(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "ssh-host", Key: "example:22"})

	var reopened *fakeStream
	opener := func() (transport.Stream, error) {
		reopened = newFakeStream(transport.Identity{Kind: "ssh-host", Key: "example:22"})
		return reopened, nil
	}
	if err := c.StartSSH(stream, opener); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
	oldID := c.SessionID()

	stream.failRead(io.EOF)
	waitFor(t, time.Second, func() bool { return c.State() == session.Reconnecting })

	if err := c.RetrySSH(); err != nil {
		t.Fatal(err)
	}
	if c.SessionID() == oldID {
		t.Fatal("a human-approved retry must rotate the session ID")
	}
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	if got := reopened.writtenSoFar(); len(got) != 0 {
		t.Fatalf("got %q, want no replayed writes on the new SSH stream", got)
	}
}

func TestRetrySSHRequiresHumanSelectionWhenOpenerFails(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "ssh-host", Key: "example:22"})
	opener := func() (transport.Stream, error) { return nil, ErrHumanSelectionRequired }
	if err := c.StartSSH(stream, opener); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })
	oldID := c.SessionID()

	stream.failRead(io.EOF)
	waitFor(t, time.Second, func() bool { return c.State() == session.Reconnecting })

	err := c.RetrySSH()
	if !errors.Is(err, ErrHumanSelectionRequired) {
		t.Fatalf("got %v, want ErrHumanSelectionRequired", err)
	}
	if c.SessionID() != oldID {
		t.Fatal("a failed retry must not rotate the session ID")
	}
}
