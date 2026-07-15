package gateway

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

func TestWaitResultImmediateAndMarkerWakeup(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})
	if err := c.StartSSH(stream, nil); err != nil {
		t.Fatal(err)
	}
	p, err := c.Propose(c.SessionID(), "uname -a", "kernel", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *command.Result, 1)
	go func() { r, _ := c.WaitResult(context.Background(), tx.ID); done <- r }()
	stream.feed([]byte(tx.ID)) // unrelated output must not wake the waiter
	select {
	case <-done:
		t.Fatal("waiter woke before completion")
	case <-time.After(20 * time.Millisecond):
	}
	m := c.activeMarker()
	stream.feed([]byte("GWMARK:" + tx.ID + ":" + m.nonce + ":0\n"))
	var got *command.Result
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("waiter was not awakened")
	}
	if got.Status != command.StatusCompleted {
		t.Fatalf("status = %s", got.Status)
	}
	immediate, err := c.WaitResult(context.Background(), tx.ID)
	if err != nil || immediate.TransactionID != tx.ID {
		t.Fatalf("immediate = %+v, %v", immediate, err)
	}
}

func TestWaitResultCancellationRemovesWaiter(t *testing.T) {
	c := newTestCoordinator(t)
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := c.WaitResult(ctx, "missing"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	}
	c.mu.Lock()
	n := len(c.resultWaiters)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("leaked %d waiter entries", n)
	}
}

func newTestCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	c := NewCoordinator("board-1")
	t.Cleanup(c.Stop)
	return c
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestStalledAIQueueDoesNotBlockHumanOutputOrKeystrokes(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})

	if err := c.StartUART(stream, LoginConfig{}, nil); err != nil {
		t.Fatal(err)
	}

	human := c.SubscribeHuman()
	ai := c.subscribeAI()

	// Stall the AI queue by filling it without ever draining it.
	for i := 0; i < subscriberQueueSize+8; i++ {
		stream.feed([]byte("burst"))
	}

	// Human subscriber must keep receiving despite the stalled AI queue.
	received := 0
	timeout := time.After(2 * time.Second)
loop:
	for received < subscriberQueueSize {
		select {
		case <-human.Events():
			received++
		case <-timeout:
			break loop
		}
	}
	if received < subscriberQueueSize {
		t.Fatalf("human subscriber only received %d events, want at least %d", received, subscriberQueueSize)
	}

	if ai.Dropped() == 0 {
		t.Fatal("expected the stalled AI queue to have dropped events")
	}

	// Keystroke forwarding must still complete promptly.
	done := make(chan error, 1)
	go func() {
		_, err := c.WriteHuman([]byte("ls\n"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("WriteHuman blocked while an AI subscriber queue was stalled")
	}
}

func TestProposeAndApproveStartsTransaction(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "usb-serial-by-id", Key: "x"})

	cfg := LoginConfig{ShellPromptPattern: mustCompile(`\$\s*$`)}
	if err := c.StartUART(stream, cfg, nil); err != nil {
		t.Fatal(err)
	}
	stream.feed([]byte("board $ "))
	waitFor(t, time.Second, func() bool { return c.AIEnabled() })

	p, err := c.Propose("s1", "uname -a", "check kernel", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Approve(p.ID); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		return bytes.Contains(stream.writtenSoFar(), []byte("uname -a"))
	})
}
