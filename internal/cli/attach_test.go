package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
)

// recordingDispatcher records every operation it was asked to perform,
// so tests can assert exactly which (and only which) IPC calls a
// command line produced, without needing a real Coordinator.
type recordingDispatcher struct {
	ops []string
	err *v1.ProtocolError
}

func (d *recordingDispatcher) Dispatch(role ipc.Role, req v1.Request) (any, *v1.ProtocolError) {
	d.ops = append(d.ops, req.Operation)
	if d.err != nil {
		return nil, d.err
	}
	return map[string]string{"ok": "true"}, nil
}

func newRecordingClient(t *testing.T) (*Client, *recordingDispatcher) {
	t.Helper()
	d := &recordingDispatcher{}
	path := filepath.Join(t.TempDir(), "gatewayd.sock")
	s, err := ipc.Listen(path, ipc.RoleAttach, d)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, d
}

func TestRunCommandLineApprove(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer
	end, err := runCommandLine(c, "approve prop-1", &out)
	if err != nil {
		t.Fatal(err)
	}
	if end {
		t.Fatal("approve must not end the attach loop")
	}
	if len(d.ops) != 1 || d.ops[0] != v1.OpCommandApprove {
		t.Fatalf("got %+v", d.ops)
	}
}

func TestRunCommandLineRejectAndEdit(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer

	if _, err := runCommandLine(c, "reject prop-1", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommandLine(c, "edit prop-1 uname -a", &out); err != nil {
		t.Fatal(err)
	}
	if len(d.ops) != 2 || d.ops[0] != v1.OpCommandReject || d.ops[1] != v1.OpCommandEdit {
		t.Fatalf("got %+v", d.ops)
	}
}

func TestRunCommandLineSecretAndSecretDone(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer

	if _, err := runCommandLine(c, "secret", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommandLine(c, "secret-done", &out); err != nil {
		t.Fatal(err)
	}
	if len(d.ops) != 2 || d.ops[0] != v1.OpSecretBegin || d.ops[1] != v1.OpSecretDone {
		t.Fatalf("got %+v", d.ops)
	}
}

func TestRunCommandLineRetryUARTAndTakeover(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer

	if _, err := runCommandLine(c, "retry uart", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommandLine(c, "takeover", &out); err != nil {
		t.Fatal(err)
	}
	if len(d.ops) != 2 || d.ops[0] != v1.OpRetryUART || d.ops[1] != v1.OpTakeover {
		t.Fatalf("got %+v", d.ops)
	}
}

func TestRunCommandLineRetrySSH(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer

	if _, err := runCommandLine(c, "retry ssh", &out); err != nil {
		t.Fatal(err)
	}
	if len(d.ops) != 1 || d.ops[0] != v1.OpRetrySSH {
		t.Fatalf("got %+v", d.ops)
	}
}

func TestRunCommandLineDetachEndsLoopWithoutEndingSession(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer

	end, err := runCommandLine(c, "detach", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !end {
		t.Fatal("detach must end the attach loop")
	}
	if len(d.ops) != 0 {
		t.Fatalf("detach must not call the daemon at all, got %+v", d.ops)
	}
}

func TestRunCommandLineEndEndsLoopAndEndsSession(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer

	end, err := runCommandLine(c, "end", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !end {
		t.Fatal("end must end the attach loop")
	}
	if len(d.ops) != 1 || d.ops[0] != v1.OpSessionEnd {
		t.Fatalf("got %+v", d.ops)
	}
}

func TestRunCommandLineUnknownCommandDoesNotCallDaemon(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer

	end, err := runCommandLine(c, "bogus", &out)
	if err == nil {
		t.Fatal("expected an error for an unknown command")
	}
	if end {
		t.Fatal("an unknown command must not end the attach loop")
	}
	if len(d.ops) != 0 {
		t.Fatalf("got %+v", d.ops)
	}
}

func TestRunCommandLineEmptyLineIsANoOp(t *testing.T) {
	c, d := newRecordingClient(t)
	var out bytes.Buffer

	end, err := runCommandLine(c, "   ", &out)
	if err != nil {
		t.Fatal(err)
	}
	if end || len(d.ops) != 0 {
		t.Fatalf("got end=%v ops=%+v", end, d.ops)
	}
}

func TestRunCommandLineSurfacesDaemonError(t *testing.T) {
	c, d := newRecordingClient(t)
	d.err = &v1.ProtocolError{Code: v1.ErrCodeInternal, Message: "boom"}
	var out bytes.Buffer

	end, err := runCommandLine(c, "approve prop-1", &out)
	if end {
		t.Fatal("a daemon error must not end the attach loop")
	}
	var protoErr *v1.ProtocolError
	if !errors.As(err, &protoErr) || protoErr.Message != "boom" {
		t.Fatalf("got %v", err)
	}
}
