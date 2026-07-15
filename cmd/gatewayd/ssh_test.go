package main

import (
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport/serial"
	sshtransport "github.com/allenpark2-coder/ai-debug-gateway/internal/transport/ssh"
)

func saveTestSSHProfile(t *testing.T, d *dispatcher) {
	t.Helper()
	if err := profile.Save(d.profileDir, profile.Profile{
		Name: "board-1",
		SSH: &profile.SSHConfig{
			Host: "example.invalid",
			Port: 22,
			User: "root",
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStartAmbiguousTransportRequiresExplicitChoice(t *testing.T) {
	d := newTestDispatcher(t)
	if err := profile.Save(d.profileDir, profile.Profile{
		Name: "board-1",
		UART: &profile.UARTConfig{
			Identity: transport.Identity{Kind: "usb-serial-by-id", Key: "/dev/serial/by-id/usb-x"},
			Line:     serial.LineSettings{BaudRate: 115200, DataBits: 8, Parity: serial.ParityNone, StopBits: 1, Flow: serial.FlowNone},
		},
		SSH: &profile.SSHConfig{Host: "example.invalid", Port: 22, User: "root"},
	}); err != nil {
		t.Fatal(err)
	}

	_, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpSessionStart,
		Payload:   mustJSON(t, sessionStartPayload{Board: "board-1"}),
	})
	if protoErr == nil {
		t.Fatal("expected session.start to require an explicit transport when a profile configures both")
	}
}

func TestSessionStartSSHUsesDialSSHOverride(t *testing.T) {
	d := newTestDispatcher(t)
	saveTestSSHProfile(t, d)

	stream := newFakeCoordStream()
	d.dialSSH = func(prof *profile.SSHConfig, auth sshtransport.HumanAuth) (transport.Stream, error) {
		return stream, nil
	}

	result, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpSessionStart,
		Payload:   mustJSON(t, sessionStartPayload{Board: "board-1"}),
	})
	if protoErr != nil {
		t.Fatal(protoErr)
	}
	_ = result

	// SSH auth already completed before Open returns a stream, so the
	// coordinator reaches Ready immediately -- no login-prompt wait
	// like UART.
	if !d.coord.AIEnabled() {
		t.Fatalf("expected the session to be immediately ready, got state %s", d.coord.State())
	}
}

func TestSessionStartSSHAcceptHostOnlyGrantedOnAttach(t *testing.T) {
	capture := func(t *testing.T, role ipc.Role) sshtransport.HumanAuth {
		t.Helper()
		d := newTestDispatcher(t)
		saveTestSSHProfile(t, d)

		var gotAuth sshtransport.HumanAuth
		d.dialSSH = func(prof *profile.SSHConfig, auth sshtransport.HumanAuth) (transport.Stream, error) {
			gotAuth = auth
			return newFakeCoordStream(), nil
		}

		if _, protoErr := d.Dispatch(role, v1.Request{
			Operation: v1.OpSessionStart,
			Payload:   mustJSON(t, sessionStartPayload{Board: "board-1", SSHAcceptHost: true}),
		}); protoErr != nil {
			t.Fatal(protoErr)
		}
		return gotAuth
	}

	controlAuth := capture(t, ipc.RoleControl)
	if controlAuth.Token != (sshtransport.HumanToken{}) || controlAuth.AcceptHost {
		t.Fatal("a control (AI) connection must never be granted host-key acceptance")
	}

	attachAuth := capture(t, ipc.RoleAttach)
	if attachAuth.Token == (sshtransport.HumanToken{}) || !attachAuth.AcceptHost {
		t.Fatal("an attach (human) connection requesting ssh_accept_host must be granted a human token")
	}
}
