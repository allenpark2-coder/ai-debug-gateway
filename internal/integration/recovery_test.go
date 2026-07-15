package integration

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/cli"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
)

// recoveryHarness runs the real gatewayd binary against a local fake
// SSH server (no hardware needed) so tests can SIGKILL the daemon
// process itself -- an uncatchable signal, so no cleanup code runs,
// exactly like a real crash -- and restart a fresh process against the
// same on-disk data directory to observe what the recovery mechanism
// actually persisted.
type recoveryHarness struct {
	bin        string
	home       string
	dataDir    string
	profileDir string
	srv        *sshTestServer

	cmd         *exec.Cmd
	controlSock string
	attachSock  string
}

const recoverySSHPassword = "hunter2"

func newRecoveryHarness(t *testing.T) *recoveryHarness {
	t.Helper()
	bin := buildBinary(t, "gatewayd")
	home := shortTempDir(t)
	srv := newSSHTestServer(t)
	srv.SetPassword(recoverySSHPassword)

	dataDir := filepath.Join(home, ".local", "share", "ai-debug-gateway")
	profileDir := filepath.Join(home, ".config", "ai-debug-gateway", "profiles")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}

	host, portStr, err := net.SplitHostPort(srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	if err := profile.Save(profileDir, profile.Profile{
		Name: "board-1",
		SSH: &profile.SSHConfig{
			Host:           host,
			Port:           port,
			User:           "root",
			KnownHostsFile: filepath.Join(home, "known_hosts"),
		},
	}); err != nil {
		t.Fatal(err)
	}

	return &recoveryHarness{bin: bin, home: home, dataDir: dataDir, profileDir: profileDir, srv: srv}
}

// start launches a fresh daemon process against h's data directory. It
// may be called more than once (after kill) to simulate a restart.
func (h *recoveryHarness) start(t *testing.T) {
	t.Helper()
	h.controlSock = filepath.Join(h.dataDir, "gatewayd.control.sock")
	h.attachSock = filepath.Join(h.dataDir, "gatewayd.attach.sock")

	// A prior instance's socket files (created, never unlinked by a
	// SIGKILL) still stat successfully with nothing listening behind
	// them; remove them first so the poll below only succeeds once the
	// new listener has actually replaced them, not on stale leftovers.
	os.Remove(h.controlSock)
	os.Remove(h.attachSock)

	cmd := exec.Command(h.bin)
	cmd.Env = append(os.Environ(), "HOME="+h.home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.cmd = cmd

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(h.controlSock); err == nil {
			if _, err := os.Stat(h.attachSock); err == nil {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("gatewayd did not create its sockets in time")
}

// kill SIGKILLs the daemon process and waits for it to fully exit, so
// a subsequent start can acquire the data directory's flock
// immediately: SIGKILL cannot be caught, so no signal handler, no
// deferred cleanup, nothing but whatever was already durably persisted
// survives -- precisely what a real crash leaves behind.
func (h *recoveryHarness) kill(t *testing.T) {
	t.Helper()
	if err := h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	h.cmd.Wait()
}

func (h *recoveryHarness) dial(t *testing.T, sock string) *cli.Client {
	t.Helper()
	c, err := cli.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (h *recoveryHarness) dialAttach(t *testing.T) *cli.Client  { return h.dial(t, h.attachSock) }
func (h *recoveryHarness) dialControl(t *testing.T) *cli.Client { return h.dial(t, h.controlSock) }

// startSSHSession starts a session against h.srv over the attach
// socket (host-key acceptance requires it) and returns the session ID.
func (h *recoveryHarness) startSSHSession(t *testing.T) string {
	t.Helper()
	c := h.dialAttach(t)
	out, err := c.SessionStartWithOptions("board-1", cli.SessionStartOptions{
		SSHPassword:   recoverySSHPassword,
		SSHAcceptHost: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		SessionID string `json:"session_id"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.State != "READY" {
		t.Fatalf("got state %q after session.start, want READY", resp.State)
	}
	return resp.SessionID
}

type recoveryAuditRecord struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

func (h *recoveryHarness) auditRecords(t *testing.T, c *cli.Client) []recoveryAuditRecord {
	t.Helper()
	out, err := c.RecordsExport(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Audit []recoveryAuditRecord `json:"audit"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Audit
}

func waitForOutputContains(t *testing.T, c *cli.Client, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := c.OutputRead(0, 1<<20)
		if err == nil {
			var resp struct {
				Data []byte `json:"data"`
			}
			if json.Unmarshal(out, &resp) == nil && strings.Contains(string(resp.Data), substr) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("output never contained %q within %s", substr, timeout)
}

// TestRestartAfterProposalOnlyExpiresPending kills the daemon with a
// proposal outstanding but never approved: proposals are pure in-memory
// state (never durably tracked), so restart trivially "expires" it --
// the fresh coordinator has no record of it at all, and nothing about
// the crash's own recovery path re-sends anything to the target.
func TestRestartAfterProposalOnlyExpiresPending(t *testing.T) {
	h := newRecoveryHarness(t)
	h.start(t)

	sessionID := h.startSSHSession(t)
	control := h.dialControl(t)
	if _, err := control.CommandPropose(sessionID, "pwd", "check cwd", 5000); err != nil {
		t.Fatal(err)
	}

	h.kill(t)
	h.start(t)

	fresh := h.dialControl(t)
	status, err := fresh.SessionStatus()
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(status, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != "DISCONNECTED" {
		t.Fatalf("got state %q after restart, want DISCONNECTED: restart must never auto-reconnect or replay", st.State)
	}

	pending, err := fresh.CommandList(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var listResp struct {
		Pending []json.RawMessage `json:"pending"`
	}
	if err := json.Unmarshal(pending, &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Pending) != 0 {
		t.Fatalf("got %d pending proposals for the old session after restart, want 0", len(listResp.Pending))
	}
}

// TestRestartAfterApprovalFinalizesIncompleteAsDaemonRestarted kills
// the daemon right after approving a long-running command (the write
// to the target and the open-set/audit bookkeeping both already
// happened): restart must finalize it as daemon-restarted, never
// silently drop it, and never re-send anything to the target.
func TestRestartAfterApprovalFinalizesIncompleteAsDaemonRestarted(t *testing.T) {
	h := newRecoveryHarness(t)
	h.start(t)

	sessionID := h.startSSHSession(t)
	control := h.dialControl(t)
	attach := h.dialAttach(t)

	proposeOut, err := control.CommandPropose(sessionID, "sleep 5", "long enough to still be running", 30000)
	if err != nil {
		t.Fatal(err)
	}
	var prop struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(proposeOut, &prop); err != nil {
		t.Fatal(err)
	}

	approveOut, err := attach.CommandApprove(prop.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tx struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(approveOut, &tx); err != nil {
		t.Fatal(err)
	}
	if tx.ID == "" {
		t.Fatal("expected a non-empty transaction ID from command.approve")
	}

	before := h.auditRecords(t, control)
	foundApproval := false
	for _, r := range before {
		if r.Kind == "transaction" && r.Detail == tx.ID {
			foundApproval = true
		}
	}
	if !foundApproval {
		t.Fatalf("expected an audit transaction record for %s before the kill, got %+v", tx.ID, before)
	}

	h.kill(t)
	h.start(t)

	fresh := h.dialControl(t)
	after := h.auditRecords(t, fresh)
	found := false
	for _, r := range after {
		if r.Kind == "result" && strings.Contains(r.Detail, tx.ID) {
			found = true
			if !strings.Contains(r.Detail, "daemon-restarted") {
				t.Fatalf("got result detail %q, want it to say daemon-restarted", r.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("expected a daemon-restarted result record for %s after restart, got %+v", tx.ID, after)
	}

	commands := h.srv.Commands()
	sleepCount := 0
	for _, c := range commands {
		if strings.HasPrefix(c, "sleep 5") {
			sleepCount++
		}
	}
	if sleepCount != 1 {
		t.Fatalf("target saw the command %d times, want exactly 1 (restart must never replay)", sleepCount)
	}
}

// TestRestartPreservesCompletedResultInsteadOfMarkingDaemonRestarted
// covers the "full marker" stage: the transaction fully completed
// while the daemon was alive, so its real result must survive a later
// restart untouched -- restart must never relabel already-complete
// work as daemon-restarted.
func TestRestartPreservesCompletedResultInsteadOfMarkingDaemonRestarted(t *testing.T) {
	h := newRecoveryHarness(t)
	h.start(t)

	sessionID := h.startSSHSession(t)
	control := h.dialControl(t)
	attach := h.dialAttach(t)

	proposeOut, err := control.CommandPropose(sessionID, "pwd", "check cwd", 5000)
	if err != nil {
		t.Fatal(err)
	}
	var prop struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(proposeOut, &prop); err != nil {
		t.Fatal(err)
	}

	approveOut, err := attach.CommandApprove(prop.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tx struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(approveOut, &tx); err != nil {
		t.Fatal(err)
	}

	waitForOutputContains(t, control, "GWMARK:"+tx.ID, waitTimeout)
	// Give the daemon's own asynchronous bookkeeping (writing the
	// completed result to the durable audit log and clearing the open
	// set) time to run before the kill, so this observes the fixed
	// steady state rather than racing it.
	time.Sleep(250 * time.Millisecond)

	h.kill(t)
	h.start(t)

	fresh := h.dialControl(t)
	after := h.auditRecords(t, fresh)
	var resultDetail string
	for _, r := range after {
		if r.Kind == "result" && strings.Contains(r.Detail, tx.ID) {
			resultDetail = r.Detail
		}
	}
	if resultDetail == "" {
		t.Fatalf("expected a result audit record for completed transaction %s to survive restart, got %+v", tx.ID, after)
	}
	if strings.Contains(resultDetail, "daemon-restarted") {
		t.Fatalf("got %q, a completed transaction must never be relabeled daemon-restarted by a later restart", resultDetail)
	}
	if !strings.Contains(resultDetail, "status=completed") {
		t.Fatalf("got %q, want it to record the real completed status", resultDetail)
	}
}
