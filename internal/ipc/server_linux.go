// Package ipc is the daemon-side and client-side owner-only Unix
// domain socket transport for the v1 protocol. A Windows Named Pipe
// implementation is a later, separate platform file; nothing here is
// imported by internal/core.
package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
)

// Role distinguishes an AI-facing control connection from a human
// attach connection. It is fixed per listener: cmd/gatewayd runs one
// listener per role on its own socket path, so the capability boundary
// is enforced by which socket a client dialed, not by a self-declared
// claim over a shared connection.
type Role int

const (
	RoleControl Role = iota
	RoleAttach
)

func (r Role) String() string {
	if r == RoleAttach {
		return "attach"
	}
	return "control"
}

// controlAllowed is the exact set of operations reachable on a control
// connection. Approval, secret entry, transport writes, retry,
// takeover, and host-key acceptance require an interactive terminal
// and are deliberately absent here, per the design spec.
var controlAllowed = map[string]bool{
	v1.OpPortsList:      true,
	v1.OpSessionStart:   true,
	v1.OpSessionStatus:  true,
	v1.OpSessionEnd:     true,
	v1.OpOutputRead:     true,
	v1.OpCommandPropose: true,
	v1.OpCommandList:    true,
	v1.OpRecordsExport:  true,
}

func permitted(role Role, operation string) bool {
	if role == RoleAttach {
		return true
	}
	return controlAllowed[operation]
}

// Dispatcher performs one already-permitted, already-version-checked
// request and returns a JSON-marshalable result or a protocol error.
type Dispatcher interface {
	Dispatch(role Role, req v1.Request) (result any, protoErr *v1.ProtocolError)
}

// Server serves one role's Unix domain socket.
type Server struct {
	role     Role
	dispatch Dispatcher
	listener net.Listener

	closeOnce sync.Once
	quit      chan struct{}
	wg        sync.WaitGroup
}

// Listen creates (or replaces a stale) owner-only (0600) Unix domain
// socket at path, serving role.
func Listen(path string, role Role, dispatch Dispatcher) (*Server, error) {
	_ = os.Remove(path) // a stale socket from a previous clean shutdown

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}

	return &Server{role: role, dispatch: dispatch, listener: l, quit: make(chan struct{})}, nil
}

// Serve accepts connections until Close is called.
func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return nil
			default:
				return err
			}
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

// Close stops accepting connections and waits for in-flight
// connections to finish.
func (s *Server) Close() error {
	s.closeOnce.Do(func() { close(s.quit) })
	err := s.listener.Close()
	s.wg.Wait()
	return err
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	reader := bufio.NewReaderSize(conn, 4096)
	writer := bufio.NewWriter(conn)

	for {
		line, err := readFrame(reader, v1.MaxFrameBytes)
		if err != nil {
			if errors.Is(err, errFrameTooLarge) {
				writeResponse(writer, v1.Response{Error: &v1.ProtocolError{
					Code:    v1.ErrCodeFrameTooLarge,
					Message: err.Error(),
				}})
			}
			return
		}

		var req v1.Request
		resp := v1.Response{Version: v1.Version}
		if err := json.Unmarshal(line, &req); err != nil {
			resp.Error = &v1.ProtocolError{Code: v1.ErrCodeInvalidPayload, Message: err.Error()}
			writeResponse(writer, resp)
			continue
		}
		resp.RequestID = req.RequestID

		switch {
		case req.Version != v1.Version:
			resp.Error = &v1.ProtocolError{
				Code:    v1.ErrCodeUnknownVersion,
				Message: fmt.Sprintf("unsupported protocol version %q", req.Version),
			}
		case !permitted(s.role, req.Operation):
			resp.Error = &v1.ProtocolError{
				Code:    v1.ErrCodePermissionDenied,
				Message: fmt.Sprintf("operation %q is not available on a %s connection", req.Operation, s.role),
			}
		default:
			result, derr := s.dispatch.Dispatch(s.role, req)
			if derr != nil {
				resp.Error = derr
			} else if result != nil {
				data, merr := json.Marshal(result)
				if merr != nil {
					resp.Error = &v1.ProtocolError{Code: v1.ErrCodeInternal, Message: merr.Error()}
				} else {
					resp.Result = data
				}
			}
		}

		writeResponse(writer, resp)
	}
}

var errFrameTooLarge = errors.New("ipc: frame exceeds maximum size")

// readFrame reads one newline-delimited frame, bounded to max bytes.
func readFrame(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, errFrameTooLarge
		}
		if err == nil {
			return bytes.TrimRight(buf, "\n"), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

func writeResponse(w *bufio.Writer, resp v1.Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	if _, err := w.Write(data); err != nil {
		return
	}
	if err := w.WriteByte('\n'); err != nil {
		return
	}
	_ = w.Flush()
}
