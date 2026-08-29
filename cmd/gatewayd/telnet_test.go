package main

import (
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

func saveTestTelnetProfile(t *testing.T, d *dispatcher) {
	t.Helper()
	if err := profile.Save(d.profileDir, profile.Profile{
		Name:   "board-1",
		Telnet: &profile.TelnetConfig{Host: "example.invalid", Port: 23},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStartDefaultsToTelnetWhenOnlyTelnetConfigured(t *testing.T) {
	d := newTestDispatcher(t)
	saveTestTelnetProfile(t, d)

	stream := newFakeCoordStream()
	dialed := false
	d.dialTelnet = func(prof *profile.TelnetConfig) (transport.Stream, error) {
		dialed = true
		if prof.Host != "example.invalid" || prof.Port != 23 {
			t.Fatalf("dialed wrong target %+v", prof)
		}
		return stream, nil
	}

	_, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpSessionStart,
		Payload:   mustJSON(t, sessionStartPayload{Board: "board-1"}),
	})
	if protoErr != nil {
		t.Fatal(protoErr)
	}
	if !dialed {
		t.Fatal("expected the telnet dial override to be used")
	}

	// Unlike SSH, a telnet stream lands on the board's login prompt:
	// the session must NOT be AI-ready before the console login state
	// machine has run.
	if d.coord.AIEnabled() {
		t.Fatalf("expected the session to still be authenticating, got state %s", d.coord.State())
	}
}

func TestSessionStartTelnetWithoutTelnetConfigFails(t *testing.T) {
	d := newTestDispatcher(t)
	saveTestSSHProfile(t, d)

	_, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpSessionStart,
		Payload:   mustJSON(t, sessionStartPayload{Board: "board-1", Transport: "telnet"}),
	})
	if protoErr == nil {
		t.Fatal("expected session.start transport=telnet to fail on a profile without telnet configuration")
	}
}
