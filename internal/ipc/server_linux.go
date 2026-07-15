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

// Role distinguishes control, human attach, and restricted diagnostic
// connections. It is fixed per listener: cmd/gatewayd runs one
// listener per role on its own socket path, so the capability boundary
// is enforced by which socket a client dialed, not by a self-declared
// claim over a shared connection.
type Role int

const (
	RoleControl Role = iota
	RoleAttach
	RoleDiagnose
)

func (r Role) String() string {
	switch r {
	case RoleAttach:
		return "attach"
	case RoleDiagnose:
		return "diagnose"
	default:
		return "control"
	}
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

var diagnoseAllowed = map[string]bool{
	v1.OpSessionStatus:   true,
	v1.OpOutputRead:      true,
	v1.OpDiagnoseExecute: true,
}

func permitted(role Role, operation string) bool {
	switch role {
	case RoleAttach:
		return true
	case RoleDiagnose:
		return diagnoseAllowed[operation]
	default:
		return controlAllowed[operation]
	}
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
	connMu    sync.Mutex
	conns     map[net.Conn]struct{}
}

// Listen creates (or replaces a stale) owner-only (0600) Unix domain
// socket at path, serving role. The caller must call Serve exactly
// once (typically as `go srv.Serve()`) for every successful Listen;
// Close's Wait would otherwise block forever.
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

	s := &Server{role: role, dispatch: dispatch, listener: l, quit: make(chan struct{}), conns: make(map[net.Conn]struct{})}
	// Added here, synchronously, before any goroutine exists to race a
	// concurrent Close's Wait against; Serve balances this with Done.
	s.wg.Add(1)
	return s, nil
}

// Serve accepts connections until Close is called. Listen already
// called wg.Add(1) for this call synchronously, before any goroutine
// existed to race a concurrent Close's Wait against; Serve balances it
// with Done on every return path.
func (s *Server) Serve() error {
	defer s.wg.Done()

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
		s.connMu.Lock()
		select {
		case <-s.quit:
			s.connMu.Unlock()
			_ = conn.Close()
			return nil
		default:
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.connMu.Unlock()
		go s.handleConn(conn)
	}
}

// Close stops accepting connections and waits for in-flight
// connections to finish.
func (s *Server) Close() error {
	s.closeOnce.Do(func() { close(s.quit) })
	err := s.listener.Close()
	s.connMu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.connMu.Unlock()
	s.wg.Wait()
	return err
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.connMu.Lock()
		delete(s.conns, conn)
		s.connMu.Unlock()
		_ = conn.Close()
	}()

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
