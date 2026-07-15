package main

import (
	"bufio"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/audit"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/ipc"
	v1 "github.com/allenpark2-coder/ai-debug-gateway/internal/protocol/v1"
)

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type fakeDaemonServer struct {
	serve  chan error
	closed chan struct{}
}

type shutdownDispatcher struct{ called chan struct{} }

func (d *shutdownDispatcher) Dispatch(ipc.Role, v1.Request) (any, *v1.ProtocolError) {
	<-d.called
	return nil, nil
}
func (d *shutdownDispatcher) Shutdown() {
	select {
	case <-d.called:
	default:
		close(d.called)
	}
}

type blockingHandlerServer struct {
	disp    ipc.Dispatcher
	started chan struct{}
	done    chan struct{}
}

func (s *blockingHandlerServer) Serve() error {
	close(s.started)
	s.disp.Dispatch(ipc.RoleDiagnose, v1.Request{Operation: v1.OpDiagnoseExecute})
	close(s.done)
	return nil
}
func (s *blockingHandlerServer) Close() error { <-s.done; return nil }

func newFakeDaemonServer() *fakeDaemonServer {
	return &fakeDaemonServer{serve: make(chan error, 1), closed: make(chan struct{})}
}
func (s *fakeDaemonServer) Serve() error { return <-s.serve }
func (s *fakeDaemonServer) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func TestServeDaemonSocketsAreOptInAndCloseTogether(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "auto"}[auto], func(t *testing.T) {
			var paths []string
			var servers []*fakeDaemonServer
			created := make(chan struct{}, 3)
			listen := func(path string, _ ipc.Role, _ ipc.Dispatcher) (daemonServer, error) {
				paths = append(paths, path)
				s := newFakeDaemonServer()
				servers = append(servers, s)
				created <- struct{}{}
				return s, nil
			}
			stop := make(chan os.Signal, 1)
			var logs syncBuffer
			done := make(chan error, 1)
			go func() {
				done <- serveDaemonSockets(t.TempDir(), "board-1", auto, false, nil, stop, listen, log.New(&logs, "", 0))
			}()
			want := 2
			if auto {
				want = 3
			}
			for i := 0; i < want; i++ {
				<-created
			}
			if len(paths) != want {
				t.Fatalf("paths = %v", paths)
			}
			joined := strings.Join(paths, " ")
			if strings.Contains(joined, "diagnose") != auto {
				t.Fatalf("paths = %v", paths)
			}
			for deadline := time.Now().Add(time.Second); logs.String() == "" && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			if strings.Contains(logs.String(), "diagnose=") != auto {
				t.Fatalf("logs = %q", logs.String())
			}
			stop <- os.Interrupt
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			for _, s := range servers {
				select {
				case <-s.closed:
				default:
					t.Fatal("server not closed")
				}
			}
		})
	}
}

func TestServeDaemonSocketsLogsBoardName(t *testing.T) {
	created := make(chan struct{}, 2)
	listen := func(string, ipc.Role, ipc.Dispatcher) (daemonServer, error) {
		s := newFakeDaemonServer()
		created <- struct{}{}
		return s, nil
	}
	stop := make(chan os.Signal, 1)
	var logs syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(t.TempDir(), "camera-7", false, false, nil, stop, listen, log.New(&logs, "", 0))
	}()
	<-created
	<-created
	for deadline := time.Now().Add(time.Second); logs.String() == "" && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(logs.String(), "board=camera-7") {
		t.Fatalf("logs = %q, want board name", logs.String())
	}
	stop <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeDaemonSocketsClosesAllOnServerError(t *testing.T) {
	var servers []*fakeDaemonServer
	created := make(chan struct{}, 3)
	listen := func(string, ipc.Role, ipc.Dispatcher) (daemonServer, error) {
		s := newFakeDaemonServer()
		servers = append(servers, s)
		created <- struct{}{}
		return s, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(t.TempDir(), "board-1", true, false, nil, make(chan os.Signal), listen, log.New(io.Discard, "", 0))
	}()
	for i := 0; i < 3; i++ {
		<-created
	}
	servers[1].serve <- errors.New("accept failed")
	if err := <-done; err == nil || !strings.Contains(err.Error(), "accept failed") {
		t.Fatalf("err = %v", err)
	}
	for _, s := range servers {
		select {
		case <-s.closed:
		default:
			t.Fatal("server not closed")
		}
	}
}

func TestServeDaemonSocketsShutsDispatcherBeforeWaitingHandlersOnListenerError(t *testing.T) {
	disp := &shutdownDispatcher{called: make(chan struct{})}
	created := make(chan struct{}, 2)
	blocking := &blockingHandlerServer{disp: disp, started: make(chan struct{}), done: make(chan struct{})}
	failing := newFakeDaemonServer()
	count := 0
	listen := func(string, ipc.Role, ipc.Dispatcher) (daemonServer, error) {
		count++
		created <- struct{}{}
		if count == 1 {
			return blocking, nil
		}
		return failing, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(t.TempDir(), "board", false, false, disp, make(chan os.Signal), listen, log.New(io.Discard, "", 0))
	}()
	<-created
	<-created
	<-blocking.started
	failing.serve <- errors.New("listener failed")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "listener failed") {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener-error teardown did not shut dispatcher before waiting")
	}
	select {
	case <-disp.called:
	default:
		t.Fatal("dispatcher was not shut down")
	}
}

func TestServeDaemonSocketsTreatsNilServeReturnAsFailure(t *testing.T) {
	var servers []*fakeDaemonServer
	created := make(chan struct{}, 2)
	listen := func(string, ipc.Role, ipc.Dispatcher) (daemonServer, error) {
		s := newFakeDaemonServer()
		servers = append(servers, s)
		created <- struct{}{}
		return s, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(t.TempDir(), "board-1", false, false, nil, make(chan os.Signal), listen, log.New(io.Discard, "", 0))
	}()
	for i := 0; i < 2; i++ {
		<-created
	}
	servers[0].serve <- nil
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unexpectedly") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("nil Serve return did not notify supervisor")
	}
}

func TestShutdownClosesEveryListenerBeforeWaitingForHeldClient(t *testing.T) {
	dir := t.TempDir()
	stop := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(dir, "board-1", true, false, nil, stop,
			func(path string, role ipc.Role, dispatch ipc.Dispatcher) (daemonServer, error) {
				return ipc.Listen(path, role, dispatch)
			},
			log.New(io.Discard, "", 0))
	}()
	paths := []string{filepath.Join(dir, "gatewayd.control.sock"), filepath.Join(dir, "gatewayd.attach.sock"), filepath.Join(dir, "gatewayd.diagnose.sock")}
	for _, path := range paths {
		for deadline := time.Now().Add(time.Second); ; {
			if _, err := os.Stat(path); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("socket did not start: %s", path)
			}
			time.Sleep(time.Millisecond)
		}
	}
	held, err := net.Dial("unix", paths[0])
	if err != nil {
		t.Fatal(err)
	}
	// Complete one frame so the server has definitely accepted the connection
	// and its per-connection goroutine is waiting for the next frame.
	if _, err := held.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(held).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	stop <- os.Interrupt
	for _, path := range paths {
		closed := false
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
			conn, err := net.Dial("unix", path)
			if err != nil {
				closed = true
				break
			}
			conn.Close()
			time.Sleep(time.Millisecond)
		}
		if !closed {
			t.Fatalf("listener remained open: %s", path)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		_ = held.Close()
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for idle client cooperation")
	}
}

func TestDiagnoseSocketHasOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	stop := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(dir, "board-1", true, false, nil, stop,
			func(path string, role ipc.Role, dispatch ipc.Dispatcher) (daemonServer, error) {
				return ipc.Listen(path, role, dispatch)
			},
			log.New(io.Discard, "", 0))
	}()
	path := filepath.Join(dir, "gatewayd.diagnose.sock")
	var info os.FileInfo
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		var err error
		info, err = os.Stat(path)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if info == nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket info = %v", info)
	}
	stop <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeShellSocketIsOptInAndClosesTogether(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "enabled"}[enabled], func(t *testing.T) {
			var paths []string
			var servers []*fakeDaemonServer
			created := make(chan struct{}, 3)
			listen := func(path string, _ ipc.Role, _ ipc.Dispatcher) (daemonServer, error) {
				paths = append(paths, path)
				s := newFakeDaemonServer()
				servers = append(servers, s)
				created <- struct{}{}
				return s, nil
			}
			stop := make(chan os.Signal, 1)
			var logs syncBuffer
			done := make(chan error, 1)
			go func() {
				done <- serveDaemonSockets(t.TempDir(), "board-1", false, enabled, nil, stop, listen, log.New(&logs, "", 0))
			}()
			want := 2
			if enabled {
				want = 3
			}
			for i := 0; i < want; i++ {
				<-created
			}
			if len(paths) != want {
				t.Fatalf("paths = %v", paths)
			}
			joined := strings.Join(paths, " ")
			if strings.Contains(joined, "unsafeshell") != enabled {
				t.Fatalf("paths = %v", paths)
			}
			for deadline := time.Now().Add(time.Second); logs.String() == "" && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			if strings.Contains(logs.String(), "unsafeshell=") != enabled {
				t.Fatalf("logs = %q", logs.String())
			}
			stop <- os.Interrupt
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			for _, s := range servers {
				select {
				case <-s.closed:
				default:
					t.Fatal("server not closed")
				}
			}
		})
	}
}

func TestUnsafeShellSocketHasOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	stop := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(dir, "board-1", false, true, nil, stop,
			func(path string, role ipc.Role, dispatch ipc.Dispatcher) (daemonServer, error) {
				return ipc.Listen(path, role, dispatch)
			},
			log.New(io.Discard, "", 0))
	}()
	path := filepath.Join(dir, "gatewayd.unsafeshell.sock")
	var info os.FileInfo
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		var err error
		info, err = os.Stat(path)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if info == nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket info = %v", info)
	}
	stop <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryStartupRemovesStaleUnsafeShellSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatewayd.unsafeshell.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := make(chan struct{}, 2)
	listen := func(string, ipc.Role, ipc.Dispatcher) (daemonServer, error) {
		created <- struct{}{}
		return newFakeDaemonServer(), nil
	}
	stop := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(dir, "board-1", false, false, nil, stop, listen, log.New(io.Discard, "", 0))
	}()
	for i := 0; i < 2; i++ {
		<-created
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale unsafe-shell socket remains: %v", err)
	}
	stop <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryStartupRemovesStaleDiagnoseSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatewayd.diagnose.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := make(chan struct{}, 2)
	listen := func(string, ipc.Role, ipc.Dispatcher) (daemonServer, error) {
		created <- struct{}{}
		return newFakeDaemonServer(), nil
	}
	stop := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveDaemonSockets(dir, "board-1", false, false, nil, stop, listen, log.New(io.Discard, "", 0))
	}()
	for i := 0; i < 2; i++ {
		<-created
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale diagnose socket remains: %v", err)
	}
	stop <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestParseDaemonOptions(t *testing.T) {
	got, err := parseDaemonOptions([]string{"--auto-readonly"})
	if err != nil || !got.AutoReadonly {
		t.Fatalf("parseDaemonOptions() = %+v, %v", got, err)
	}
	if _, err := parseDaemonOptions([]string{"--unknown"}); err == nil {
		t.Fatal("expected unknown argument to fail")
	}
	got, err = parseDaemonOptions([]string{"--unsafe-auto-shell=board-1"})
	if err != nil || got.UnsafeAutoShell != "board-1" {
		t.Fatalf("parseDaemonOptions() = %+v, %v", got, err)
	}
	got, err = parseDaemonOptions([]string{"--auto-readonly", "--unsafe-auto-shell=board-1"})
	if err != nil || !got.AutoReadonly || got.UnsafeAutoShell != "board-1" {
		t.Fatalf("combined flags: parseDaemonOptions() = %+v, %v", got, err)
	}
}

func TestLoadDaemonPolicyOnlyWhenAutoReadonlyIsEnabled(t *testing.T) {
	config := t.TempDir()
	policyDir := filepath.Join(config, "policies")
	if err := os.Mkdir(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(policyDir, "board-1.json")
	if err := os.WriteFile(name, []byte(`{"allow":[{"executable":"opsis-inspect","args":["status"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := loadDaemonPolicy(daemonOptions{}, config, "board-1")
	if err != nil {
		t.Fatalf("ordinary startup unexpectedly read policy: %v", err)
	}
	if p.Evaluate("opsis-inspect status").Allowed {
		t.Fatal("ordinary startup unexpectedly applied board policy")
	}

	p, err = loadDaemonPolicy(daemonOptions{AutoReadonly: true}, config, "board-1")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Evaluate("opsis-inspect status").Allowed {
		t.Fatal("auto-readonly did not apply board policy")
	}

	// Loading returns a snapshot; changing the file does not mutate the policy.
	if err := os.WriteFile(name, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !p.Evaluate("opsis-inspect status").Allowed {
		t.Fatal("policy was live reloaded")
	}
	if _, err := loadDaemonPolicy(daemonOptions{AutoReadonly: true}, config, "board-1"); err == nil || !strings.Contains(err.Error(), "board policy") {
		t.Fatalf("invalid diagnose policy error = %v, want clear board policy error", err)
	}

	// Board additions are optional: a board without a policy file runs
	// auto-readonly under the common policy alone.
	p, err = loadDaemonPolicy(daemonOptions{AutoReadonly: true}, config, "board-without-file")
	if err != nil {
		t.Fatalf("missing board file must not be an error, got %v", err)
	}
	if !p.Evaluate("uptime").Allowed {
		t.Fatal("missing board file must fall back to the common policy")
	}
	if p.Evaluate("opsis-inspect status").Allowed {
		t.Fatal("missing board file must not inherit another board's additions")
	}
}

func TestLoadUnsafeShellDaemonPolicyOnlyWhenFlagNamesABoard(t *testing.T) {
	config := t.TempDir()
	shellDir := filepath.Join(config, "unsafe-shell")
	if err := os.Mkdir(shellDir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(shellDir, "board-1.json")
	if err := os.WriteFile(name, []byte(`{"risk_accepted":true,"deny_executables":["reboot"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := loadUnsafeShellDaemonPolicy(daemonOptions{}, config)
	if err != nil || p != nil {
		t.Fatalf("ordinary startup unexpectedly loaded an unsafe-shell policy: %v, %v", p, err)
	}

	p, err = loadUnsafeShellDaemonPolicy(daemonOptions{UnsafeAutoShell: "board-1"}, config)
	if err != nil {
		t.Fatal(err)
	}
	if p.Evaluate("reboot").Allowed {
		t.Fatal("board denylist was not applied")
	}
	if !p.Evaluate("mount -o remount,rw /").Allowed {
		t.Fatal("denylist mode unexpectedly denied an unrelated command")
	}

	if err := os.WriteFile(name, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p.Evaluate("reboot").Allowed {
		t.Fatal("policy was live reloaded; a loaded snapshot must be unaffected by a later file change")
	}
	if _, err := loadUnsafeShellDaemonPolicy(daemonOptions{UnsafeAutoShell: "board-1"}, config); err == nil || !strings.Contains(err.Error(), "unsafe-shell policy") {
		t.Fatalf("invalid unsafe-shell policy error = %v, want clear unsafe-shell policy error", err)
	}

	if _, err := loadUnsafeShellDaemonPolicy(daemonOptions{UnsafeAutoShell: "missing-board"}, config); err == nil {
		t.Fatal("expected a missing board file to fail")
	}
}

func TestAcquireLockRejectsSecondInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gatewayd.lock")

	f1, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock(f1)

	if _, err := acquireLock(path); err == nil {
		t.Fatal("expected a second instance to fail to acquire the lock")
	}

	releaseLock(f1)
	f2, err := acquireLock(path)
	if err != nil {
		t.Fatalf("expected to acquire the lock after release, got %v", err)
	}
	releaseLock(f2)
}

func TestOpenSetPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "open.json")

	s1, err := loadOpenSet(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.add("txn-1", "sess-1"); err != nil {
		t.Fatal(err)
	}
	if err := s1.add("txn-2", "sess-2"); err != nil {
		t.Fatal(err)
	}
	aw := audit.NewWriter(filepath.Join(dir, "audit.jsonl"))
	if ok, err := s1.finalize(aw, "board-1", "txn-1", "transaction=txn-1 status=completed"); !ok || err != nil {
		t.Fatalf("finalize(txn-1) = %v, %v, want true, nil", ok, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("got mode %v, want 0600", info.Mode().Perm())
	}

	s2, err := loadOpenSet(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.list()
	if len(got) != 1 || got[0] != "txn-2" {
		t.Fatalf("got %+v, want [txn-2]", got)
	}
}

func TestRecoverIncompleteTransactionsFinalizesAndClearsOpenSet(t *testing.T) {
	dir := t.TempDir()
	open, err := loadOpenSet(filepath.Join(dir, "open.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := open.add("txn-stale", "sess-stale"); err != nil {
		t.Fatal(err)
	}

	aw := audit.NewWriter(filepath.Join(dir, "audit.jsonl"))
	if err := recoverIncompleteTransactions(open, aw, "board-1"); err != nil {
		t.Fatal(err)
	}

	if got := open.list(); len(got) != 0 {
		t.Fatalf("expected the open set to be cleared, got %+v", got)
	}

	records, err := aw.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range records {
		if r.Kind == "result" && strings.Contains(r.Detail, "txn-stale") && strings.Contains(r.Detail, "daemon-restarted") {
			found = true
			if r.Board != "board-1" || r.Session != "sess-stale" {
				t.Fatalf("recovery record must be attributed, got board=%q session=%q", r.Board, r.Session)
			}
		}
	}
	if !found {
		t.Fatalf("expected a daemon-restarted result record for txn-stale, got %+v", records)
	}
}

// TestOpenSetFinalizeIsExactlyOnce covers the dispatcher/reconciler
// race: both may try to finalize the same completed transaction, and
// only the first attempt may write a result record.
func TestOpenSetFinalizeIsExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	open, err := loadOpenSet(filepath.Join(dir, "open.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := open.add("txn-1", "sess-1"); err != nil {
		t.Fatal(err)
	}

	aw := audit.NewWriter(filepath.Join(dir, "audit.jsonl"))
	if ok, err := open.finalize(aw, "board-1", "txn-1", "transaction=txn-1 status=completed"); !ok || err != nil {
		t.Fatalf("first finalize = %v, %v, want true, nil", ok, err)
	}
	if ok, err := open.finalize(aw, "board-1", "txn-1", "transaction=txn-1 status=completed"); ok || err != nil {
		t.Fatalf("second finalize = %v, %v, want false, nil", ok, err)
	}

	records, err := aw.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	results := 0
	for _, r := range records {
		if r.Kind == "result" {
			results++
			if r.Board != "board-1" || r.Session != "sess-1" {
				t.Fatalf("result record must be attributed, got board=%q session=%q", r.Board, r.Session)
			}
		}
	}
	if results != 1 {
		t.Fatalf("got %d result records for one transaction, want exactly 1", results)
	}
}

// TestLoadOpenSetAcceptsLegacyListFormat keeps a daemon upgraded across
// the open-set format change able to recover transactions persisted by
// the previous version (a bare JSON array of IDs).
func TestLoadOpenSetAcceptsLegacyListFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open.json")
	if err := os.WriteFile(path, []byte(`["txn-legacy"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	open, err := loadOpenSet(path)
	if err != nil {
		t.Fatal(err)
	}
	got := open.list()
	if len(got) != 1 || got[0] != "txn-legacy" {
		t.Fatalf("got %+v, want [txn-legacy]", got)
	}
}
