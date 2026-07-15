// Package serial implements the UART transport: port discovery, line
// settings, and identity-safe matching against a saved board profile.
// Pure matching and validation logic lives here so it is testable
// without hardware; actual port enumeration and opening are Linux-
// specific and live in discovery_linux.go.
package serial

import (
	"fmt"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// Parity is a UART parity setting.
type Parity string

const (
	ParityNone Parity = "none"
	ParityOdd  Parity = "odd"
	ParityEven Parity = "even"
)

// FlowControl is a UART flow-control setting.
type FlowControl string

const (
	FlowNone     FlowControl = "none"
	FlowHardware FlowControl = "hardware"
	FlowSoftware FlowControl = "software"
)

// LineSettings is a UART port's line configuration.
type LineSettings struct {
	BaudRate int
	DataBits int
	Parity   Parity
	StopBits int
	Flow     FlowControl
}

// Validate reports whether the line settings are usable.
func (s LineSettings) Validate() error {
	if s.BaudRate <= 0 {
		return fmt.Errorf("serial: baud rate must be positive, got %d", s.BaudRate)
	}
	if s.DataBits < 5 || s.DataBits > 8 {
		return fmt.Errorf("serial: data bits must be 5-8, got %d", s.DataBits)
	}
	switch s.Parity {
	case ParityNone, ParityOdd, ParityEven:
	default:
		return fmt.Errorf("serial: unknown parity %q", s.Parity)
	}
	if s.StopBits != 1 && s.StopBits != 2 {
		return fmt.Errorf("serial: stop bits must be 1 or 2, got %d", s.StopBits)
	}
	switch s.Flow {
	case FlowNone, FlowHardware, FlowSoftware:
	default:
		return fmt.Errorf("serial: unknown flow control %q", s.Flow)
	}
	return nil
}

// Port is one serial port discovered on the host.
type Port struct {
	// Path is the current device node, e.g. "/dev/ttyUSB0". It is not
	// stable across reconnects and must never be used as an identity.
	Path string
	// ByIDPath is the stable "/dev/serial/by-id/..." path, when the
	// kernel exposes one.
	ByIDPath string
	// USBSerialNumber is the raw USB serial number, when available and
	// no by-id path is exposed.
	USBSerialNumber string
}

// Identity returns the port's stable identity, preferring the by-id
// path over a raw USB serial number. The zero Identity means the port
// has no stable identity the gateway can trust.
func (p Port) Identity() transport.Identity {
	switch {
	case p.ByIDPath != "":
		return transport.Identity{Kind: "usb-serial-by-id", Key: p.ByIDPath}
	case p.USBSerialNumber != "":
		return transport.Identity{Kind: "usb-serial-number", Key: p.USBSerialNumber}
	default:
		return transport.Identity{}
	}
}

// MatchResult is the outcome of matching a saved profile identity
// against currently discovered ports.
type MatchResult struct {
	// Port is set only when exactly one currently discovered port has
	// the requested identity.
	Port *Port
	// NeedsHumanSelection is true when the gateway must not
	// automatically pick a port: no stored identity, no matching port,
	// or more than one matching port.
	NeedsHumanSelection bool
	Reason              string
}

// Match finds the port among ports whose Identity equals want. It
// never matches on Path alone, so a device with a different identity
// reusing the same tty path is never silently accepted.
func Match(want transport.Identity, ports []Port) MatchResult {
	if !want.Known() {
		return MatchResult{NeedsHumanSelection: true, Reason: "profile has no stored USB identity"}
	}

	var matches []Port
	for _, p := range ports {
		if p.Identity() == want {
			matches = append(matches, p)
		}
	}

	switch len(matches) {
	case 0:
		return MatchResult{NeedsHumanSelection: true, Reason: "no discovered port matches the saved identity"}
	case 1:
		port := matches[0]
		return MatchResult{Port: &port}
	default:
		return MatchResult{NeedsHumanSelection: true, Reason: "multiple discovered ports match the saved identity"}
	}
}
