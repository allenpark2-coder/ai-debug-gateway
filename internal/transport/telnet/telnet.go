// Package telnet adapts a TCP connection to a board's telnetd to
// transport.Stream. It speaks the minimum of RFC 854 required to be a
// well-behaved NVT client: every option the server offers or demands
// is refused (DONT/WONT), subnegotiations are skipped, and the IAC
// escape is applied to data in both directions. Nothing more: no
// option is ever requested, so the session stays in plain NVT mode,
// which every BusyBox/inetd telnetd accepts.
//
// A telnet connection carries no server authentication and no
// encryption. It reaches the board's own login process (telnetd spawns
// login on a pty), so the console login state machine applies exactly
// as for UART; the caller is expected to run it.
package telnet

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// RFC 854 command bytes. Only the ones the state machine must
// recognize are named.
const (
	cmdSE   = 240
	cmdSB   = 250
	cmdWILL = 251
	cmdWONT = 252
	cmdDO   = 253
	cmdDONT = 254
	cmdIAC  = 255
)

// readState tracks where the incoming filter is inside an IAC
// sequence across Read calls, since a sequence may be split between
// TCP segments.
type readState int

const (
	stateData   readState = iota // plain data
	stateIAC                     // consumed IAC, awaiting command
	stateOption                  // consumed IAC WILL/WONT/DO/DONT, awaiting option
	stateSB                      // inside a subnegotiation
	stateSBIAC                   // consumed IAC inside a subnegotiation
)

// Stream is a telnet NVT client connection implementing
// transport.Stream.
type Stream struct {
	conn     net.Conn
	identity transport.Identity

	// writeMu serializes application writes with the negotiation
	// refusals the read path emits, so a refusal can never be
	// interleaved into the middle of an escaped data sequence.
	writeMu sync.Mutex

	// Read-side filter state. Reads are single-goroutine (the
	// coordinator owns the read loop), so no mutex is needed.
	state readState
	verb  byte // pending WILL/WONT/DO/DONT while in stateOption

	closeOnce sync.Once
	closeErr  error
}

// Open dials prof over TCP and returns the wrapped stream. A zero
// Port means the well-known telnet port 23.
func Open(ctx context.Context, prof *profile.TelnetConfig) (*Stream, error) {
	port := prof.Port
	if port == 0 {
		port = 23
	}
	addr := fmt.Sprintf("%s:%d", prof.Host, port)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return newStream(conn, addr), nil
}

// newStream wraps an established connection. Split from Open so tests
// can drive the filter over net.Pipe without a listener.
func newStream(conn net.Conn, addr string) *Stream {
	return &Stream{
		conn:     conn,
		identity: transport.Identity{Kind: "telnet-host", Key: addr},
	}
}

// Read fills p with unescaped application data. IAC sequences are
// consumed and answered (DO->WONT, WILL->DONT) rather than surfaced.
// A read that yields only protocol bytes loops instead of returning
// 0, nil.
func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		n, err := s.conn.Read(p)
		kept, refusals := s.filter(p[:n])
		if len(refusals) > 0 {
			// Refusals are best-effort: if the peer is gone the
			// pending read error already reports it.
			s.writeRaw(refusals)
		}
		if kept > 0 || err != nil {
			return kept, err
		}
	}
}

// filter runs the NVT state machine over buf in place, returning how
// many data bytes remain at the front of buf and any negotiation
// refusals to send back.
func (s *Stream) filter(buf []byte) (kept int, refusals []byte) {
	for _, b := range buf {
		switch s.state {
		case stateData:
			if b == cmdIAC {
				s.state = stateIAC
				continue
			}
			buf[kept] = b
			kept++
		case stateIAC:
			switch b {
			case cmdIAC: // escaped 0xFF data byte
				buf[kept] = cmdIAC
				kept++
				s.state = stateData
			case cmdWILL, cmdWONT, cmdDO, cmdDONT:
				s.verb = b
				s.state = stateOption
			case cmdSB:
				s.state = stateSB
			default: // NOP, GA, BRK, ... : two-byte command, drop
				s.state = stateData
			}
		case stateOption:
			switch s.verb {
			case cmdDO:
				refusals = append(refusals, cmdIAC, cmdWONT, b)
			case cmdWILL:
				refusals = append(refusals, cmdIAC, cmdDONT, b)
				// A server's WONT/DONT needs no answer: it announces
				// the option is off, which is the only state this
				// client ever wants.
			}
			s.state = stateData
		case stateSB:
			if b == cmdIAC {
				s.state = stateSBIAC
			}
		case stateSBIAC:
			if b == cmdSE {
				s.state = stateData
			} else {
				// Includes IAC IAC (escaped data inside a
				// subnegotiation we are skipping anyway).
				s.state = stateSB
			}
		}
	}
	return kept, refusals
}

// Write sends p verbatim as application data, escaping 0xFF per RFC
// 854. Line endings are not rewritten: like the UART transport, the
// gateway sends exactly the bytes the session layer chose.
func (s *Stream) Write(p []byte) (int, error) {
	escaped := make([]byte, 0, len(p)+8)
	for _, b := range p {
		if b == cmdIAC {
			escaped = append(escaped, cmdIAC)
		}
		escaped = append(escaped, b)
	}
	if err := s.writeRaw(escaped); err != nil {
		return 0, err
	}
	return len(p), nil
}

// writeRaw sends already-escaped protocol bytes under writeMu.
func (s *Stream) writeRaw(b []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.conn.Write(b)
	return err
}

func (s *Stream) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.conn.Close() })
	return s.closeErr
}

func (s *Stream) Identity() transport.Identity { return s.identity }
func (s *Stream) Kind() string                 { return "telnet" }
