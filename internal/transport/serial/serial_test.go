package serial

import (
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

func TestLineSettingsValidate(t *testing.T) {
	tests := []struct {
		name string
		s    LineSettings
		ok   bool
	}{
		{"valid 8N1", LineSettings{BaudRate: 115200, DataBits: 8, Parity: ParityNone, StopBits: 1, Flow: FlowNone}, true},
		{"valid 7E2 hardware flow", LineSettings{BaudRate: 9600, DataBits: 7, Parity: ParityEven, StopBits: 2, Flow: FlowHardware}, true},
		{"zero baud", LineSettings{BaudRate: 0, DataBits: 8, Parity: ParityNone, StopBits: 1}, false},
		{"negative baud", LineSettings{BaudRate: -9600, DataBits: 8, Parity: ParityNone, StopBits: 1}, false},
		{"bad data bits", LineSettings{BaudRate: 9600, DataBits: 4, Parity: ParityNone, StopBits: 1}, false},
		{"bad parity", LineSettings{BaudRate: 9600, DataBits: 8, Parity: "weird", StopBits: 1}, false},
		{"bad stop bits", LineSettings{BaudRate: 9600, DataBits: 8, Parity: ParityNone, StopBits: 3}, false},
		{"bad flow", LineSettings{BaudRate: 9600, DataBits: 8, Parity: ParityNone, StopBits: 1, Flow: "weird"}, false},
	}
	for _, tt := range tests {
		err := tt.s.Validate()
		if (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func byIDIdentity(path string) transport.Identity {
	return transport.Identity{Kind: "usb-serial-by-id", Key: path}
}

func TestMatchByIDIdentity(t *testing.T) {
	want := byIDIdentity("/dev/serial/by-id/usb-FTDI_TTL232-if00")
	ports := []Port{
		{Path: "/dev/ttyUSB0", ByIDPath: "/dev/serial/by-id/usb-FTDI_TTL232-if00"},
		{Path: "/dev/ttyUSB1", ByIDPath: "/dev/serial/by-id/usb-other-if00"},
	}
	got := Match(want, ports)
	if got.NeedsHumanSelection || got.Port == nil || got.Port.Path != "/dev/ttyUSB0" {
		t.Fatalf("%+v", got)
	}
}

func TestMatchSurvivesTTYRenameByIdentity(t *testing.T) {
	want := byIDIdentity("/dev/serial/by-id/usb-FTDI_TTL232-if00")
	// The kernel renumbered the tty node across a reboot; the by-id
	// identity is unchanged.
	ports := []Port{
		{Path: "/dev/ttyUSB3", ByIDPath: "/dev/serial/by-id/usb-FTDI_TTL232-if00"},
	}
	got := Match(want, ports)
	if got.NeedsHumanSelection || got.Port == nil || got.Port.Path != "/dev/ttyUSB3" {
		t.Fatalf("%+v", got)
	}
}

func TestMatchRejectsReusedPathWithDifferentIdentity(t *testing.T) {
	want := byIDIdentity("/dev/serial/by-id/usb-FTDI_TTL232-if00")
	// A different physical device now occupies the same tty path.
	ports := []Port{
		{Path: "/dev/ttyUSB0", ByIDPath: "/dev/serial/by-id/usb-DIFFERENT-if00"},
	}
	got := Match(want, ports)
	if !got.NeedsHumanSelection || got.Port != nil {
		t.Fatalf("must not match on reused path with different identity: %+v", got)
	}
}

func TestMatchNoStoredIdentityRequiresHumanSelection(t *testing.T) {
	ports := []Port{{Path: "/dev/ttyUSB0", ByIDPath: "/dev/serial/by-id/usb-FTDI_TTL232-if00"}}
	got := Match(transport.Identity{}, ports)
	if !got.NeedsHumanSelection || got.Port != nil {
		t.Fatalf("%+v", got)
	}
}

func TestMatchMultipleCandidatesRequiresHumanSelection(t *testing.T) {
	want := byIDIdentity("/dev/serial/by-id/usb-FTDI_TTL232-if00")
	ports := []Port{
		{Path: "/dev/ttyUSB0", ByIDPath: "/dev/serial/by-id/usb-FTDI_TTL232-if00"},
		{Path: "/dev/ttyUSB4", ByIDPath: "/dev/serial/by-id/usb-FTDI_TTL232-if00"},
	}
	got := Match(want, ports)
	if !got.NeedsHumanSelection || got.Port != nil {
		t.Fatalf("ambiguous identity match must require human selection: %+v", got)
	}
}
