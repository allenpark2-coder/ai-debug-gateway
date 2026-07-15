package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/audit"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/transcript"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/gateway"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport/serial"
)

// fakeCoordStream is a minimal in-memory transport.Stream for
// dispatcher tests, avoiding any real serial hardware.
type fakeCoordStream struct {
	identity transport.Identity
	data     chan []byte
	closeCh  chan struct{}
	closeOne sync.Once

	writeMu sync.Mutex
	written bytes.Buffer
}

func newFakeCoordStream() *fakeCoordStream {
	return &fakeCoordStream{
		identity: transport.Identity{Kind: "usb-serial-by-id", Key: "/dev/serial/by-id/usb-x"},
		data:     make(chan []byte, 16),
		closeCh:  make(chan struct{}),
	}
}

func (f *fakeCoordStream) Read(p []byte) (int, error) {
	select {
	case chunk, ok := <-f.data:
		if !ok {
			return 0, io.EOF
		}
		return copy(p, chunk), nil
	case <-f.closeCh:
		return 0, io.EOF
	}
}

func (f *fakeCoordStream) Write(p []byte) (int, error) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	return f.written.Write(p)
}

func (f *fakeCoordStream) Close() error {
	f.closeOne.Do(func() { close(f.closeCh) })
	return nil
}

func (f *fakeCoordStream) Identity() transport.Identity { return f.identity }
func (f *fakeCoordStream) Kind() string                 { return "fake" }

func (f *fakeCoordStream) feedData(data []byte) { f.data <- append([]byte(nil), data...) }

func (f *fakeCoordStream) writtenSoFar() []byte {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	return append([]byte(nil), f.written.Bytes()...)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newTestDispatcher(t *testing.T) *dispatcher {
	t.Helper()
	dir := t.TempDir()

	coord := gateway.NewCoordinator("board-1")
	t.Cleanup(coord.Stop)

	open, err := loadOpenSet(filepath.Join(dir, "open.json"))
	if err != nil {
		t.Fatal(err)
	}
	aw := audit.NewWriter(filepath.Join(dir, "audit.jsonl"))
	tw := transcript.NewWriter(filepath.Join(dir, "transcript.jsonl"))

	return newDispatcher("board-1", dir, coord, open, aw, tw, gateway.LoginConfig{
		ShellPromptPattern: regexp.MustCompile(`\$\s*$`),
	})
}

func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func saveTestProfile(t *testing.T, d *dispatcher) {
	t.Helper()
	if err := profile.Save(d.profileDir, profile.Profile{
		Name: "board-1",
		UART: &profile.UARTConfig{
			Identity: transport.Identity{Kind: "usb-serial-by-id", Key: "/dev/serial/by-id/usb-x"},
			Line:     serial.LineSettings{BaudRate: 115200, DataBits: 8, Parity: serial.ParityNone, StopBits: 1, Flow: serial.FlowNone},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPortsListReturnsInjectedPorts(t *testing.T) {
	d := newTestDispatcher(t)
	d.listPorts = func() ([]serial.Port, error) {
		return []serial.Port{{Path: "/dev/ttyUSB0", ByIDPath: "/dev/serial/by-id/usb-x"}}, nil
	}

	result, protoErr := d.Dispatch(ipc.RoleControl, v1.Request{Operation: v1.OpPortsList})
	if protoErr != nil {
		t.Fatal(protoErr)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("/dev/ttyUSB0")) {
		t.Fatalf("got %s, want it to contain the fake port", data)
	}
}

func TestSessionStartAmbiguousIdentityFails(t *testing.T) {
	d := newTestDispatcher(t)
	d.listPorts = func() ([]serial.Port, error) { return nil, nil }
	d.openPort = func(serial.Port, serial.LineSettings) (transport.Stream, error) {
		t.Fatal("must not open a port when identity is ambiguous")
		return nil, nil
	}
	saveTestProfile(t, d)

	_, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpSessionStart,
		Payload:   mustJSON(t, sessionStartPayload{Board: "board-1"}),
	})
	if protoErr == nil {
		t.Fatal("expected session.start to fail when the port cannot be matched")
	}
}

func TestProposeApproveAndListRoundTrip(t *testing.T) {
	d := newTestDispatcher(t)
	stream := newFakeCoordStream()
	d.listPorts = func() ([]serial.Port, error) {
		return []serial.Port{{Path: "/dev/ttyUSB0", ByIDPath: "/dev/serial/by-id/usb-x"}}, nil
	}
	d.openPort = func(serial.Port, serial.LineSettings) (transport.Stream, error) { return stream, nil }
	saveTestProfile(t, d)

	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpSessionStart,
		Payload:   mustJSON(t, sessionStartPayload{Board: "board-1"}),
	}); protoErr != nil {
		t.Fatal(protoErr)
	}

	stream.feedData([]byte("board $ "))
	waitForCond(t, time.Second, func() bool { return d.coord.AIEnabled() })

	proposeResult, protoErr := d.Dispatch(ipc.RoleControl, v1.Request{
		Operation: v1.OpCommandPropose,
		Payload:   mustJSON(t, commandProposePayload{SessionID: d.coord.SessionID(), Text: "pwd", Purpose: "cwd", TimeoutMS: 1000}),
	})
	if protoErr != nil {
		t.Fatal(protoErr)
	}
	prop, ok := proposeResult.(*command.Proposal)
	if !ok {
		t.Fatalf("got %T, want *command.Proposal", proposeResult)
	}

	approveResult, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpCommandApprove,
		Payload:   mustJSON(t, commandIDPayload{ProposalID: prop.ID}),
	})
	if protoErr != nil {
		t.Fatal(protoErr)
	}
	tx, ok := approveResult.(*command.Transaction)
	if !ok {
		t.Fatalf("got %T, want *command.Transaction", approveResult)
	}

	if got := d.open.list(); len(got) != 1 || got[0] != tx.ID {
		t.Fatalf("expected the open set to track the new transaction, got %+v", got)
	}
}

func TestTransportWriteForwardsToStream(t *testing.T) {
	d := newTestDispatcher(t)
	stream := newFakeCoordStream()
	d.listPorts = func() ([]serial.Port, error) {
		return []serial.Port{{Path: "/dev/ttyUSB0", ByIDPath: "/dev/serial/by-id/usb-x"}}, nil
	}
	d.openPort = func(serial.Port, serial.LineSettings) (transport.Stream, error) { return stream, nil }
	saveTestProfile(t, d)

	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpSessionStart,
		Payload:   mustJSON(t, sessionStartPayload{Board: "board-1"}),
	}); protoErr != nil {
		t.Fatal(protoErr)
	}

	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpTransportWrite,
		Payload:   mustJSON(t, transportWritePayload{Data: []byte("ls\n")}),
	}); protoErr != nil {
		t.Fatal(protoErr)
	}

	waitForCond(t, time.Second, func() bool { return bytes.Contains(stream.writtenSoFar(), []byte("ls")) })
}

func TestRecordsExportEnforcesRetentionLimit(t *testing.T) {
	d := newTestDispatcher(t)
	d.retentionLimit = 3
	for i := 0; i < 10; i++ {
		if _, err := d.tw.Append(transcript.Record{Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}

	result, protoErr := d.Dispatch(ipc.RoleControl, v1.Request{Operation: v1.OpRecordsExport, Payload: mustJSON(t, recordsExportPayload{})})
	if protoErr != nil {
		t.Fatal(protoErr)
	}
	out, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("got %T", result)
	}
	got, ok := out["transcript"].([]transcript.Record)
	if !ok || len(got) != 3 {
		t.Fatalf("got %+v, want 3 records after retention", out["transcript"])
	}
}

func startTestSession(t *testing.T, d *dispatcher, stream *fakeCoordStream) {
	t.Helper()
	d.listPorts = func() ([]serial.Port, error) {
		return []serial.Port{{Path: "/dev/ttyUSB0", ByIDPath: "/dev/serial/by-id/usb-x"}}, nil
	}
	d.openPort = func(serial.Port, serial.LineSettings) (transport.Stream, error) { return stream, nil }
	saveTestProfile(t, d)
	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpSessionStart,
		Payload:   mustJSON(t, sessionStartPayload{Board: "board-1"}),
	}); protoErr != nil {
		t.Fatal(protoErr)
	}
	stream.feedData([]byte("board $ "))
	waitForCond(t, time.Second, func() bool { return d.coord.AIEnabled() })
}

func TestTransportWritePausedDuringRunningCommandExceptCtrlC(t *testing.T) {
	d := newTestDispatcher(t)
	stream := newFakeCoordStream()
	startTestSession(t, d, stream)

	proposeResult, protoErr := d.Dispatch(ipc.RoleControl, v1.Request{
		Operation: v1.OpCommandPropose,
		Payload:   mustJSON(t, commandProposePayload{SessionID: d.coord.SessionID(), Text: "sleep 5", Purpose: "long", TimeoutMS: 5000}),
	})
	if protoErr != nil {
		t.Fatal(protoErr)
	}
	prop := proposeResult.(*command.Proposal)

	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpCommandApprove,
		Payload:   mustJSON(t, commandIDPayload{ProposalID: prop.ID}),
	}); protoErr != nil {
		t.Fatal(protoErr)
	}

	_, protoErr = d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpTransportWrite,
		Payload:   mustJSON(t, transportWritePayload{Data: []byte("ordinary keystrokes\n")}),
	})
	if protoErr == nil || protoErr.Code != v1.ErrCodePermissionDenied {
		t.Fatalf("got %+v, want permission_denied while a transaction runs", protoErr)
	}

	_, protoErr = d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpTransportWrite,
		Payload:   mustJSON(t, transportWritePayload{Data: []byte{0x03}}),
	})
	if protoErr != nil {
		t.Fatalf("Ctrl-C must still be forwarded while a transaction runs, got %+v", protoErr)
	}
}

func TestTakeoverEndsTransactionAndReenablesInput(t *testing.T) {
	d := newTestDispatcher(t)
	stream := newFakeCoordStream()
	startTestSession(t, d, stream)

	proposeResult, protoErr := d.Dispatch(ipc.RoleControl, v1.Request{
		Operation: v1.OpCommandPropose,
		Payload:   mustJSON(t, commandProposePayload{SessionID: d.coord.SessionID(), Text: "sleep 5", Purpose: "long", TimeoutMS: 5000}),
	})
	if protoErr != nil {
		t.Fatal(protoErr)
	}
	prop := proposeResult.(*command.Proposal)
	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpCommandApprove,
		Payload:   mustJSON(t, commandIDPayload{ProposalID: prop.ID}),
	}); protoErr != nil {
		t.Fatal(protoErr)
	}

	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{Operation: v1.OpTakeover}); protoErr != nil {
		t.Fatal(protoErr)
	}

	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{
		Operation: v1.OpTransportWrite,
		Payload:   mustJSON(t, transportWritePayload{Data: []byte("back in control\n")}),
	}); protoErr != nil {
		t.Fatalf("input must be re-enabled after takeover, got %+v", protoErr)
	}
}

func TestSecretBeginAndDoneOnlyAllowedOnAttach(t *testing.T) {
	d := newTestDispatcher(t)
	stream := newFakeCoordStream()
	startTestSession(t, d, stream)

	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{Operation: v1.OpSecretBegin}); protoErr != nil {
		t.Fatal(protoErr)
	}
	if !d.coord.SecretActive() {
		t.Fatal("secret.begin must open the redaction window")
	}
	if _, protoErr := d.Dispatch(ipc.RoleAttach, v1.Request{Operation: v1.OpSecretDone}); protoErr != nil {
		t.Fatal(protoErr)
	}
	if d.coord.SecretActive() {
		t.Fatal("secret.done must close the redaction window")
	}
}
