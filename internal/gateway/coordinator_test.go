package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
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

func TestDiagnoseStartWriteFailureRecordsAndWakes(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "ssh", Key: "x"})
	if err := c.StartSSH(stream, nil); err != nil {
		t.Fatal(err)
	}
	stream.writeErr = errors.New("write failed")
	tx, err := c.DiagnoseStart(c.SessionID(), "uname -a", "kernel", time.Second)
	if err == nil {
		t.Fatal("expected write failure")
	}
	if tx == nil {
		t.Fatal("approved transaction must be returned for terminal bookkeeping")
	}
	res, waitErr := c.WaitResult(context.Background(), tx.ID)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	if res.Status != command.StatusDisconnected {
		t.Fatalf("status = %s", res.Status)
	}
	if c.State() != session.Ready {
		t.Fatalf("state = %s", c.State())
	}
	if len(c.PendingForSession(c.SessionID())) != 0 {
		t.Fatal("diagnostic proposal residue")
	}
}

func TestDiagnoseStartRejectsStateWithoutPendingResidue(t *testing.T) {
	c := newTestCoordinator(t)
	if _, err := c.DiagnoseStart(c.SessionID(), "uname -a", "kernel", time.Second); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v", err)
	}
	if len(c.PendingForSession(c.SessionID())) != 0 {
		t.Fatal("diagnostic proposal residue")
	}
}

func TestExactRingCapacityIsNotStartTruncated(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "ssh", Key: "x"})
	if err := c.StartSSH(stream, nil); err != nil {
		t.Fatal(err)
	}
	tx, err := c.DiagnoseStart(c.SessionID(), "uname -a", "kernel", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m := c.activeMarker()
	markerLine := []byte(fmt.Sprintf("GWMARK:%s:%s:0\n", tx.ID, m.nonce))
	body := bytes.Repeat([]byte{'x'}, (1<<20)-len(markerLine))
	for len(body) > 0 {
		n := 4096
		if n > len(body) {
			n = len(body)
		}
		stream.feed(body[:n])
		body = body[n:]
	}
	stream.feed(markerLine)
	res, err := c.WaitResult(context.Background(), tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.OutputTruncatedStart {
		t.Fatal("exact ring capacity falsely marked start-truncated")
	}
}

func TestManualApproveCannotRaceDiagnosticProposal(t *testing.T) {
	c := newTestCoordinator(t)
	stream := newFakeStream(transport.Identity{Kind: "ssh", Key: "x"})
	if err := c.StartSSH(stream, nil); err != nil {
		t.Fatal(err)
	}
	manual, err := c.Propose(c.SessionID(), "pwd", "manual", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() { <-start; _, err := c.Approve(manual.ID); errs <- err }()
	go func() {
		<-start
		_, err := c.DiagnoseStart(c.SessionID(), "uname -a", "diagnostic", time.Second)
		errs <- err
	}()
	close(start)
	e1, e2 := <-errs, <-errs
	if (e1 == nil) == (e2 == nil) {
		t.Fatalf("errors = %v, %v; want exactly one start", e1, e2)
	}
	for _, p := range c.PendingForSession(c.SessionID()) {
		if p.ID != manual.ID {
			t.Fatalf("externally visible diagnostic residue: %+v", p)
		}
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
