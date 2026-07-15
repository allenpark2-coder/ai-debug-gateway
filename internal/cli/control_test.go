package cli

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/policy"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
)

type echoDispatcher struct{}

func (echoDispatcher) Dispatch(role ipc.Role, req v1.Request) (any, *v1.ProtocolError) {
	return map[string]string{"operation": req.Operation, "role": role.String()}, nil
}

type diagnoseDispatcher struct {
	request DiagnoseRequest
}

func (d *diagnoseDispatcher) Dispatch(role ipc.Role, req v1.Request) (any, *v1.ProtocolError) {
	if err := json.Unmarshal(req.Payload, &d.request); err != nil {
		return nil, &v1.ProtocolError{Code: v1.ErrCodeInvalidPayload, Message: err.Error()}
	}
	exitCode := 0
	return DiagnoseResult{
		Decision:       policy.Decision{Allowed: true, Rule: "command.uname", Reason: "safe"},
		Transaction:    &command.Transaction{ID: "txn-1", SessionID: d.request.SessionID, Text: d.request.Text},
		Result:         &command.Result{TransactionID: "txn-1", Status: command.StatusCompleted, ExitCode: &exitCode},
		TruncatedStart: true,
	}, nil
}

func TestDiagnoseExecuteUsesTypedRequestAndResult(t *testing.T) {
	d := &diagnoseDispatcher{}
	path := filepath.Join(t.TempDir(), "gatewayd.sock")
	s, err := ipc.Listen(path, ipc.RoleDiagnose, d)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	want := DiagnoseRequest{SessionID: "session-1", Text: "uname -a", Purpose: "inspect kernel", TimeoutMS: 2500}
	got, err := c.DiagnoseExecute(want)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.request, want) {
		t.Fatalf("request got %+v, want %+v", d.request, want)
	}
	if !got.Decision.Allowed || got.Transaction == nil || got.Transaction.ID != "txn-1" || got.Result == nil || got.Result.Status != command.StatusCompleted || !got.TruncatedStart || got.TruncatedEnd {
		t.Fatalf("unexpected result: %+v", got)
	}
}

type unsafeShellDispatcher struct {
	request UnsafeShellRequest
}

func (d *unsafeShellDispatcher) Dispatch(role ipc.Role, req v1.Request) (any, *v1.ProtocolError) {
	if err := json.Unmarshal(req.Payload, &d.request); err != nil {
		return nil, &v1.ProtocolError{Code: v1.ErrCodeInvalidPayload, Message: err.Error()}
	}
	exitCode := 0
	return UnsafeShellResult{
		Decision:    policy.Decision{Allowed: true, Rule: "denylist.allow", Reason: "not denied"},
		Transaction: &command.Transaction{ID: "txn-2", SessionID: d.request.SessionID, Text: d.request.Text},
		Result:      &command.Result{TransactionID: "txn-2", Status: command.StatusCompleted, ExitCode: &exitCode},
	}, nil
}

func TestUnsafeShellExecuteUsesTypedRequestAndResult(t *testing.T) {
	d := &unsafeShellDispatcher{}
	path := filepath.Join(t.TempDir(), "gatewayd.sock")
	s, err := ipc.Listen(path, ipc.RoleUnsafeShell, d)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	want := UnsafeShellRequest{SessionID: "session-1", Text: "mount -o remount,rw /", Purpose: "need rw", TimeoutMS: 5000}
	got, err := c.UnsafeShellExecute(want)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.request, want) {
		t.Fatalf("request got %+v, want %+v", d.request, want)
	}
	if !got.Decision.Allowed || got.Transaction == nil || got.Transaction.ID != "txn-2" || got.Result == nil || got.Result.Status != command.StatusCompleted {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func newTestServer(t *testing.T, role ipc.Role) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gatewayd.sock")
	s, err := ipc.Listen(path, role, echoDispatcher{})
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return path
}

func TestClientCallRoundTrip(t *testing.T) {
	path := newTestServer(t, ipc.RoleControl)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out struct {
		Operation string `json:"operation"`
	}
	if err := c.Call(v1.OpPortsList, nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.Operation != v1.OpPortsList {
		t.Fatalf("got %+v", out)
	}
}

func TestClientSurfacesProtocolErrorAsGoError(t *testing.T) {
	path := newTestServer(t, ipc.RoleControl)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	err = c.Call(v1.OpCommandApprove, nil, nil)
	var protoErr *v1.ProtocolError
	if !errors.As(err, &protoErr) || protoErr.Code != v1.ErrCodePermissionDenied {
		t.Fatalf("got %v, want a permission_denied ProtocolError", err)
	}
}

func TestPortsListWrapperReturnsRawResult(t *testing.T) {
	path := newTestServer(t, ipc.RoleControl)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	out, err := c.PortsList()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["operation"] != v1.OpPortsList {
		t.Fatalf("%+v", decoded)
	}
}
