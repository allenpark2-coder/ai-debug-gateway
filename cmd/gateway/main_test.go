package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/cli"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("display failed") }

type cliCaptureDispatcher struct{ requests []v1.Request }

func (d *cliCaptureDispatcher) Dispatch(_ ipc.Role, req v1.Request) (any, *v1.ProtocolError) {
	d.requests = append(d.requests, req)
	if req.Operation == v1.OpDiagnoseExecute || req.Operation == v1.OpUnsafeShellExecute {
		return map[string]any{"decision": map[string]any{"allowed": true}}, nil
	}
	return map[string]string{"state": "approved"}, nil
}

func testCLIServer(t *testing.T, role ipc.Role, d *cliCaptureDispatcher) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), role.String()+".sock")
	s, err := ipc.Listen(path, role, d)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return path
}

func TestDiagnoseRoutesValidRequestAndPrintsCommandFirst(t *testing.T) {
	d := &cliCaptureDispatcher{}
	path := testCLIServer(t, ipc.RoleDiagnose, d)
	var dialed []string
	dial := func(got string) (*cli.Client, error) { dialed = append(dialed, got); return cli.Dial(path) }
	var stdout, stderr bytes.Buffer
	err := runCLI([]string{"diagnose", "--session", "s1", "--text", "ps", "--purpose", "list", "--timeout-ms", "15"}, socketPaths{Diagnose: "gatewayd.diagnose.sock", Control: "control", Attach: "attach"}, dial, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(dialed) != 1 || dialed[0] != "gatewayd.diagnose.sock" {
		t.Fatalf("dialed %v", dialed)
	}
	if !strings.HasPrefix(stderr.String(), "ps\n") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !json.Valid(bytes.TrimSpace(stdout.Bytes())) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if len(d.requests) != 1 || d.requests[0].Operation != v1.OpDiagnoseExecute {
		t.Fatalf("requests = %+v", d.requests)
	}
}

func TestDiagnoseValidatesRequiredFlagsBeforeDial(t *testing.T) {
	cases := [][]string{
		{"diagnose", "--text", "ps", "--purpose", "list", "--timeout-ms", "1"},
		{"diagnose", "--session", "s", "--purpose", "list", "--timeout-ms", "1"},
		{"diagnose", "--session", "s", "--text", "ps", "--timeout-ms", "1"},
		{"diagnose", "--session", "s", "--text", "ps", "--purpose", "list", "--timeout-ms", "0"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dials := 0
			err := runCLI(args, socketPaths{}, func(string) (*cli.Client, error) { dials++; return nil, nil }, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || dials != 0 {
				t.Fatalf("err=%v dials=%d", err, dials)
			}
		})
	}
}

func TestDiagnoseRejectsOversizedConfirmationAndDisplayErrorsBeforeDial(t *testing.T) {
	for name, args := range map[string][]string{
		"oversized approval": {"approve", "--proposal", "p1", "--confirmation", strings.Repeat("x", maxConfirmationBytes+1)},
		"display failure":    {"diagnose", "--session", "s", "--text", "ps", "--purpose", "list", "--timeout-ms", "1"},
	} {
		t.Run(name, func(t *testing.T) {
			dials := 0
			stderr := io.Writer(&bytes.Buffer{})
			if name == "display failure" {
				stderr = failingWriter{}
			}
			err := runCLI(args, socketPaths{}, func(string) (*cli.Client, error) { dials++; return nil, nil }, &bytes.Buffer{}, stderr)
			if err == nil || dials != 0 {
				t.Fatalf("err=%v dials=%d", err, dials)
			}
		})
	}
}

func TestUnsafeShellRoutesValidRequestAndPrintsCommandFirst(t *testing.T) {
	d := &cliCaptureDispatcher{}
	path := testCLIServer(t, ipc.RoleUnsafeShell, d)
	var dialed []string
	dial := func(got string) (*cli.Client, error) { dialed = append(dialed, got); return cli.Dial(path) }
	var stdout, stderr bytes.Buffer
	err := runCLI([]string{"unsafe-shell", "--session", "s1", "--text", "mount -o remount,rw /", "--purpose", "need rw", "--timeout-ms", "15"},
		socketPaths{UnsafeShell: "gatewayd.unsafeshell.sock", Control: "control", Attach: "attach", Diagnose: "diagnose"}, dial, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(dialed) != 1 || dialed[0] != "gatewayd.unsafeshell.sock" {
		t.Fatalf("dialed %v", dialed)
	}
	if !strings.HasPrefix(stderr.String(), "mount -o remount,rw /\n") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !json.Valid(bytes.TrimSpace(stdout.Bytes())) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if len(d.requests) != 1 || d.requests[0].Operation != v1.OpUnsafeShellExecute {
		t.Fatalf("requests = %+v", d.requests)
	}
}

func TestUnsafeShellValidatesRequiredFlagsBeforeDial(t *testing.T) {
	cases := [][]string{
		{"unsafe-shell", "--text", "mount", "--purpose", "list", "--timeout-ms", "1"},
		{"unsafe-shell", "--session", "s", "--purpose", "list", "--timeout-ms", "1"},
		{"unsafe-shell", "--session", "s", "--text", "mount", "--timeout-ms", "1"},
		{"unsafe-shell", "--session", "s", "--text", "mount", "--purpose", "list", "--timeout-ms", "0"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dials := 0
			err := runCLI(args, socketPaths{}, func(string) (*cli.Client, error) { dials++; return nil, nil }, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || dials != 0 {
				t.Fatalf("err=%v dials=%d", err, dials)
			}
		})
	}
}

func TestApproveRoutesConfirmationToAttach(t *testing.T) {
	d := &cliCaptureDispatcher{}
	path := testCLIServer(t, ipc.RoleAttach, d)
	var dialed []string
	dial := func(got string) (*cli.Client, error) { dialed = append(dialed, got); return cli.Dial(path) }
	err := runCLI([]string{"approve", "--proposal", "p1", "--confirmation", "operator confirmed"}, socketPaths{Diagnose: "diagnose", Control: "control", Attach: "gatewayd.attach.sock"}, dial, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dialed) != 1 || dialed[0] != "gatewayd.attach.sock" {
		t.Fatalf("dialed %v", dialed)
	}
	var payload map[string]string
	if err := json.Unmarshal(d.requests[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["proposal_id"] != "p1" || payload["confirmation"] != "operator confirmed" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestApproveRequiresNonemptyProposalAndConfirmationBeforeDial(t *testing.T) {
	for _, args := range [][]string{{"approve", "--confirmation", "yes"}, {"approve", "--proposal", "p1"}, {"approve", "--proposal", "p1", "--confirmation", "   "}} {
		dials := 0
		err := runCLI(args, socketPaths{}, func(string) (*cli.Client, error) { dials++; return nil, nil }, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || dials != 0 {
			t.Fatalf("args=%v err=%v dials=%d", args, err, dials)
		}
	}
}

func TestFlagValue(t *testing.T) {
	args := []string{"--board", "b1", "--text", "uname -a"}
	if got := flagValue(args, "--board"); got != "b1" {
		t.Fatalf("got %q, want b1", got)
	}
	if got := flagValue(args, "--text"); got != "uname -a" {
		t.Fatalf("got %q, want %q", got, "uname -a")
	}
	if got := flagValue(args, "--missing"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := flagValue([]string{"--board"}, "--board"); got != "" {
		t.Fatalf("a flag with no following value must not panic or return garbage, got %q", got)
	}
}

func TestParseUintAndParseInt(t *testing.T) {
	if got := parseUint("42"); got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
	if got := parseUint(""); got != 0 {
		t.Fatalf("got %d, want 0 for empty input", got)
	}
	if got := parseInt("-5"); got != -5 {
		t.Fatalf("got %d, want -5", got)
	}
}
