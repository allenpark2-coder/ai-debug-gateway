package ipc

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
)

// echoDispatcher is a minimal Dispatcher for exercising the server's
// framing and role enforcement without any real gateway wiring.
type echoDispatcher struct{}

func (echoDispatcher) Dispatch(role Role, req v1.Request) (any, *v1.ProtocolError) {
	return map[string]string{"echo": req.Operation, "role": role.String()}, nil
}

func newTestServer(t *testing.T, role Role) (string, *Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gatewayd.sock")
	s, err := Listen(path, role, echoDispatcher{})
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return path, s
}

func TestSocketModeIsOwnerOnly(t *testing.T) {
	path, _ := newTestServer(t, RoleAttach)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("got mode %v, want 0600", info.Mode().Perm())
	}
}

func TestUnknownVersionRejected(t *testing.T) {
	path, _ := newTestServer(t, RoleAttach)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Call(v1.Request{Version: "99", RequestID: "r1", Operation: v1.OpPortsList})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != v1.ErrCodeUnknownVersion {
		t.Fatalf("%+v", resp)
	}
}

func TestOversizedFrameRejected(t *testing.T) {
	path, _ := newTestServer(t, RoleAttach)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	huge := bytes.Repeat([]byte("x"), v1.MaxFrameBytes+1)
	if _, err := c.conn.Write(append(huge, '\n')); err != nil {
		t.Fatal(err)
	}

	// The server's response frame is small; only the oversized request
	// frame was rejected.
	line, err := readFrame(c.reader, v1.MaxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	var resp v1.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != v1.ErrCodeFrameTooLarge {
		t.Fatalf("got %+v, want frame_too_large", resp)
	}
}

func TestControlConnectionCannotApproveOrWriteTransport(t *testing.T) {
	path, _ := newTestServer(t, RoleControl)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, op := range []string{v1.OpCommandApprove, v1.OpTransportWrite, v1.OpSecretBegin, v1.OpRetryUART, v1.OpRetrySSH, v1.OpTakeover, v1.OpHostKeyAccept} {
		resp, err := c.Call(v1.Request{Version: v1.Version, RequestID: op, Operation: op})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != v1.ErrCodePermissionDenied {
			t.Fatalf("operation %q: got %+v, want permission_denied", op, resp)
		}
	}
}

func TestControlConnectionCanProposeAndReadOutput(t *testing.T) {
	path, _ := newTestServer(t, RoleControl)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, op := range []string{v1.OpPortsList, v1.OpSessionStart, v1.OpSessionStatus, v1.OpSessionEnd, v1.OpOutputRead, v1.OpCommandPropose, v1.OpCommandList, v1.OpRecordsExport} {
		resp, err := c.Call(v1.Request{Version: v1.Version, RequestID: op, Operation: op})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error != nil {
			t.Fatalf("operation %q: unexpected error %+v", op, resp.Error)
		}
		var result map[string]string
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result["echo"] != op || !strings.Contains(result["role"], "control") {
			t.Fatalf("operation %q: got %+v", op, result)
		}
	}
}

func TestDiagnoseRoleCapabilityMatrix(t *testing.T) {
	allowed := []string{v1.OpSessionStatus, v1.OpOutputRead, v1.OpDiagnoseExecute}
	denied := []string{v1.OpCommandApprove, v1.OpTransportWrite, v1.OpRetryUART,
		v1.OpSecretBegin, v1.OpSessionStart, v1.OpSessionEnd, v1.OpHostKeyAccept}

	for _, op := range allowed {
		if !permitted(RoleDiagnose, op) {
			t.Errorf("diagnose operation %q: got denied, want allowed", op)
		}
	}
	for _, op := range denied {
		if permitted(RoleDiagnose, op) {
			t.Errorf("diagnose operation %q: got allowed, want denied", op)
		}
	}
	if permitted(RoleControl, v1.OpDiagnoseExecute) {
		t.Error("control must not diagnose")
	}
	if permitted(RoleControl, v1.OpCommandApprove) {
		t.Error("control must not approve")
	}
	for _, op := range append(append([]string{}, allowed...), denied...) {
		if !permitted(RoleAttach, op) {
			t.Errorf("attach operation %q: behavior changed to denied", op)
		}
	}
}

func TestUnsafeShellRoleCapabilityMatrix(t *testing.T) {
	allowed := []string{v1.OpSessionStatus, v1.OpOutputRead, v1.OpUnsafeShellExecute}
	denied := []string{v1.OpCommandApprove, v1.OpTransportWrite, v1.OpRetryUART, v1.OpRetrySSH,
		v1.OpSecretBegin, v1.OpSessionStart, v1.OpSessionEnd, v1.OpHostKeyAccept, v1.OpDiagnoseExecute}

	for _, op := range allowed {
		if !permitted(RoleUnsafeShell, op) {
			t.Errorf("unsafeshell operation %q: got denied, want allowed", op)
		}
	}
	for _, op := range denied {
		if permitted(RoleUnsafeShell, op) {
			t.Errorf("unsafeshell operation %q: got allowed, want denied", op)
		}
	}
	if permitted(RoleControl, v1.OpUnsafeShellExecute) {
		t.Error("control must not reach unsafeshell.execute")
	}
	for _, op := range append(append([]string{}, allowed...), denied...) {
		if !permitted(RoleAttach, op) {
			t.Errorf("attach operation %q: behavior changed to denied", op)
		}
	}
}

func TestDiagnoseAndUnsafeShellRolesAreDisjoint(t *testing.T) {
	if permitted(RoleDiagnose, v1.OpUnsafeShellExecute) {
		t.Error("diagnose must not reach unsafeshell.execute")
	}
	if permitted(RoleUnsafeShell, v1.OpDiagnoseExecute) {
		t.Error("unsafeshell must not reach diagnose.execute")
	}
}

func TestAttachConnectionCanApprove(t *testing.T) {
	path, _ := newTestServer(t, RoleAttach)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Call(v1.Request{Version: v1.Version, RequestID: "r1", Operation: v1.OpCommandApprove})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("attach connection must be able to approve, got %+v", resp.Error)
	}
}

func TestReusedRequestIDsAreIndependentlyAnswered(t *testing.T) {
	path, _ := newTestServer(t, RoleAttach)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for i := 0; i < 3; i++ {
		resp, err := c.Call(v1.Request{Version: v1.Version, RequestID: "same-id", Operation: v1.OpPortsList})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error != nil {
			t.Fatalf("%+v", resp.Error)
		}
	}
}

func TestServerCloseStopsAcceptingConnections(t *testing.T) {
	path, s := newTestServer(t, RoleAttach)
	s.Close()
	time.Sleep(20 * time.Millisecond)
	if _, err := Dial(path); err == nil {
		t.Fatal("expected dialing a closed server's socket to fail")
	}
}

func TestServerCloseTerminatesIdleAcceptedConnection(t *testing.T) {
	path, s := newTestServer(t, RoleAttach)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "closed") {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked on idle accepted connection")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("client socket remained open")
	}
}
