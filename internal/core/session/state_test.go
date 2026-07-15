package session

import "testing"

func TestRetryRotatesSession(t *testing.T) {
	m := NewMachine("old")
	for _, e := range []Event{Connect, TransportReady, Authenticated, TransportLost} {
		if err := m.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Apply(HumanRetry); err != nil {
		t.Fatal(err)
	}
	if m.State() != Connecting || m.SessionID() == "old" {
		t.Fatalf("%+v", m)
	}
}

func TestConnectAuthRunResult(t *testing.T) {
	m := NewMachine("s1")
	for _, e := range []Event{Connect, TransportReady, Authenticated} {
		if err := m.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	if m.State() != Ready {
		t.Fatalf("after auth: got %s, want READY", m.State())
	}
	if err := m.Apply(CommandStart); err != nil {
		t.Fatal(err)
	}
	if m.State() != RunningCommand {
		t.Fatalf("after command start: got %s, want RUNNING_COMMAND", m.State())
	}
	if err := m.Apply(CommandResult); err != nil {
		t.Fatal(err)
	}
	if m.State() != Ready {
		t.Fatalf("after command result: got %s, want READY", m.State())
	}
}

func TestTargetRebootPreservesSessionID(t *testing.T) {
	for _, start := range []State{Ready, RunningCommand} {
		m := NewMachine("s1")
		m.state = start
		if err := m.Apply(TargetRebooted); err != nil {
			t.Fatal(err)
		}
		if m.State() != Authenticating {
			t.Fatalf("from %s: got %s, want AUTHENTICATING", start, m.State())
		}
		if m.SessionID() != "s1" {
			t.Fatalf("target reboot must preserve session ID, got %q", m.SessionID())
		}
	}
}

func TestTransportLossEntersReconnecting(t *testing.T) {
	for _, start := range []State{Ready, RunningCommand} {
		m := NewMachine("s1")
		m.state = start
		if err := m.Apply(TransportLost); err != nil {
			t.Fatal(err)
		}
		if m.State() != Reconnecting {
			t.Fatalf("from %s: got %s, want RECONNECTING", start, m.State())
		}
		if m.SessionID() != "s1" {
			t.Fatalf("transport loss must not itself rotate the session ID, got %q", m.SessionID())
		}
	}
}

func TestRetriesExhaustedEntersError(t *testing.T) {
	m := NewMachine("s1")
	m.state = Reconnecting
	if err := m.Apply(RetriesExhausted); err != nil {
		t.Fatal(err)
	}
	if m.State() != Error {
		t.Fatalf("got %s, want ERROR", m.State())
	}
}

func TestShutdownFromAnyActiveState(t *testing.T) {
	for _, start := range []State{Connecting, Authenticating, Ready, RunningCommand, Reconnecting, Error} {
		m := NewMachine("s1")
		m.state = start
		if err := m.Apply(Shutdown); err != nil {
			t.Fatalf("from %s: %v", start, err)
		}
		if m.State() != Disconnected {
			t.Fatalf("from %s: got %s, want DISCONNECTED", start, m.State())
		}
	}
}

func TestInvalidTransitionIsRejected(t *testing.T) {
	m := NewMachine("s1")
	err := m.Apply(Authenticated)
	if err == nil {
		t.Fatal("expected error applying AUTHENTICATED from DISCONNECTED")
	}
	if m.State() != Disconnected {
		t.Fatalf("rejected event must not change state, got %s", m.State())
	}
}
