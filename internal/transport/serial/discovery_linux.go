package serial

import (
	"fmt"
	"os"
	"path/filepath"

	upstream "go.bug.st/serial"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// List enumerates serial ports on the host, resolving each to its
// stable "/dev/serial/by-id/..." identity when the kernel exposes one.
func List() ([]Port, error) {
	names, err := upstream.GetPortsList()
	if err != nil {
		return nil, err
	}

	byID := resolveByID()
	ports := make([]Port, 0, len(names))
	for _, name := range names {
		ports = append(ports, Port{
			Path:     name,
			ByIDPath: byID[name],
		})
	}
	return appendIntegrationPorts(ports, byID), nil
}

// resolveByID maps a resolved device node path (e.g. "/dev/ttyUSB0")
// to its stable by-id symlink path, for every by-id entry the kernel
// currently exposes.
func resolveByID() map[string]string {
	dir := serialByIDDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make(map[string]string)
	for _, e := range entries {
		linkPath := filepath.Join(dir, e.Name())
		target, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			continue
		}
		out[target] = linkPath
	}
	return out
}

func toUpstreamMode(s LineSettings) *upstream.Mode {
	mode := &upstream.Mode{BaudRate: s.BaudRate, DataBits: s.DataBits}
	switch s.Parity {
	case ParityOdd:
		mode.Parity = upstream.OddParity
	case ParityEven:
		mode.Parity = upstream.EvenParity
	default:
		mode.Parity = upstream.NoParity
	}
	if s.StopBits == 2 {
		mode.StopBits = upstream.TwoStopBits
	} else {
		mode.StopBits = upstream.OneStopBit
	}
	return mode
}

// stream adapts an upstream serial.Port to transport.Stream.
type stream struct {
	upstream.Port
	identity transport.Identity
}

func (s *stream) Identity() transport.Identity { return s.identity }
func (s *stream) Kind() string                 { return "uart" }

// Open opens port with the given line settings.
//
// The pinned go.bug.st/serial v1.7.1 backend always disables both
// hardware (RTS/CTS) and software (XON/XOFF) flow control at the
// termios level on Linux and exposes no public option to re-enable
// either, so a request for FlowHardware or FlowSoftware is rejected
// rather than silently opened as FlowNone.
func Open(port Port, settings LineSettings) (transport.Stream, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if settings.Flow != FlowNone {
		return nil, fmt.Errorf("serial: flow control %q is not supported by the pinned go.bug.st/serial v1.7.1 backend; use FlowNone", settings.Flow)
	}

	p, err := upstream.Open(port.Path, toUpstreamMode(settings))
	if err != nil {
		return nil, err
	}
	return &stream{Port: p, identity: port.Identity()}, nil
}
