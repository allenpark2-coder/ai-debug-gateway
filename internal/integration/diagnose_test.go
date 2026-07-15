package integration

import (
	"bytes"
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
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
)

type diagnoseHarness struct {
	home, dataDir, gateway string
	srv                    *sshTestServer
	daemon                 *exec.Cmd
	log                    bytes.Buffer
	session                string
}

func newDiagnoseHarness(t *testing.T, automatic bool) *diagnoseHarness {
	t.Helper()
	h := &diagnoseHarness{home: shortTempDir(t), gateway: buildBinary(t, "gateway"), srv: newSSHTestServer(t)}
	h.srv.SetPassword(recoverySSHPassword)
	h.dataDir = filepath.Join(h.home, ".local", "share", "ai-debug-gateway")
	profileDir := filepath.Join(h.home, ".config", "ai-debug-gateway", "profiles")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(h.srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.Save(profileDir, profile.Profile{Name: "board-1", SSH: &profile.SSHConfig{Host: host, Port: port, User: "root", KnownHostsFile: filepath.Join(h.home, "known_hosts")}}); err != nil {
		t.Fatal(err)
	}
	if automatic {
		policyDir := filepath.Join(h.home, ".config", "ai-debug-gateway", "policies")
		if err := os.MkdirAll(policyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(policyDir, "board-1.json"), []byte(`{"allow":[],"deny":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	args := []string{}
	if automatic {
		args = append(args, "--auto-readonly")
	}
	h.daemon = exec.Command(buildBinary(t, "gatewayd"), args...)
	h.daemon.Env = append(os.Environ(), "HOME="+h.home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=", "GATEWAYD_BOARD=board-1")
	h.daemon.Stdout, h.daemon.Stderr = &h.log, &h.log
	if err := h.daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.daemon.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- h.daemon.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = h.daemon.Process.Kill()
		}
		if t.Failed() {
			t.Logf("gatewayd output: %s", h.log.String())
		}
	})
	waitPath(t, filepath.Join(h.dataDir, "gatewayd.attach.sock"))
	if automatic {
		waitPath(t, filepath.Join(h.dataDir, "gatewayd.diagnose.sock"))
	}

	attach, err := cli.Dial(filepath.Join(h.dataDir, "gatewayd.attach.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer attach.Close()
	out, err := attach.SessionStartWithOptions("board-1", cli.SessionStartOptions{SSHPassword: recoverySSHPassword, SSHAcceptHost: true})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(out, &started); err != nil {
		t.Fatal(err)
	}
	h.session = started.SessionID
	return h
}

func waitPath(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("path was not created: %s", path)
}

func (h *diagnoseHarness) runGateway(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(h.gateway, args...)
	cmd.Env = append(os.Environ(), "HOME="+h.home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestDiagnoseSocketIsOptInInRealDaemon(t *testing.T) {
	h := newDiagnoseHarness(t, false)
	if _, err := os.Stat(filepath.Join(h.dataDir, "gatewayd.diagnose.sock")); !os.IsNotExist(err) {
		t.Fatalf("diagnose socket exists without opt-in: %v", err)
	}
}

func TestDiagnoseRealBinariesEnforcePolicyAndPreserveManualApproval(t *testing.T) {
	h := newDiagnoseHarness(t, true)
	before := len(h.srv.Commands())
	stdout, stderr, err := h.runGateway(t, "diagnose", "--session", h.session, "--text", "ps", "--purpose", "inspect processes", "--timeout-ms", "2000")
	if err != nil {
		t.Fatalf("diagnose ps: %v stderr=%s", err, stderr)
	}
	if stderr != "ps\n" {
		t.Fatalf("displayed command = %q", stderr)
	}
	var got cli.DiagnoseResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Decision.Allowed || got.Result == nil || got.Result.Status != command.StatusCompleted {
		t.Fatalf("diagnose result = %+v", got)
	}
	if len(h.srv.Commands()) != before+1 {
		t.Fatalf("ps did not reach target exactly once: %v", h.srv.Commands())
	}

	rejected := []string{"echo x > /tmp/x", "echo $(id)", "felix_status", "cat /etc/shadow", "reboot"}
	for _, text := range rejected {
		count := len(h.srv.Commands())
		stdout, _, err := h.runGateway(t, "diagnose", "--session", h.session, "--text", text, "--purpose", "investigate", "--timeout-ms", "1000")
		if err != nil {
			t.Fatalf("diagnose rejection %q: %v", text, err)
		}
		var result cli.DiagnoseResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatal(err)
		}
		if result.Decision.Allowed || result.Transaction != nil {
			t.Fatalf("%q unexpectedly executed: %+v", text, result)
		}
		if len(h.srv.Commands()) != count {
			t.Fatalf("rejected %q reached target: %v", text, h.srv.Commands()[count:])
		}
	}

	control, err := cli.Dial(filepath.Join(h.dataDir, "gatewayd.control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	proposalJSON, err := control.CommandPropose(h.session, "echo mutation", "operator-requested change", 2000)
	if err != nil {
		t.Fatal(err)
	}
	var proposal struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(proposalJSON, &proposal); err != nil {
		t.Fatal(err)
	}
	confirmation := "operator confirmed in chat"
	if _, stderr, err := h.runGateway(t, "approve", "--proposal", proposal.ID, "--confirmation", confirmation); err != nil {
		t.Fatalf("approve: %v stderr=%s", err, stderr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(h.srv.Commands()) < before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	exported, err := control.RecordsExport(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exported), confirmation) || !strings.Contains(string(exported), "confirmation=[redacted] bytes=26") {
		t.Fatalf("audit did not record redacted confirmation metadata: %s", exported)
	}
	if strings.Contains(stdout, recoverySSHPassword) || strings.Contains(string(exported), recoverySSHPassword) {
		t.Fatal("secret password escaped into diagnostic or durable output")
	}
}

func TestDiagnoseRealBinaryReportsTimeoutAndDisconnect(t *testing.T) {
	h := newDiagnoseHarness(t, true)
	h.srv.SetManagedDelay(100 * time.Millisecond)
	stdout, _, err := h.runGateway(t, "diagnose", "--session", h.session, "--text", "ps", "--purpose", "timeout check", "--timeout-ms", "1")
	if err != nil {
		t.Fatal(err)
	}
	var timed cli.DiagnoseResult
	if err := json.Unmarshal([]byte(stdout), &timed); err != nil {
		t.Fatal(err)
	}
	if timed.Result == nil || timed.Result.Status != command.StatusTimeout {
		t.Fatalf("timeout result = %+v", timed.Result)
	}
	time.Sleep(150 * time.Millisecond)

	h.srv.SetManagedDelay(time.Second)
	done := make(chan struct {
		out string
		err error
	}, 1)
	go func() {
		out, _, err := h.runGateway(t, "diagnose", "--session", h.session, "--text", "ps", "--purpose", "disconnect check", "--timeout-ms", "5000")
		done <- struct {
			out string
			err error
		}{out, err}
	}()
	time.Sleep(100 * time.Millisecond)
	h.srv.DisconnectAll()
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	var disconnected cli.DiagnoseResult
	if err := json.Unmarshal([]byte(result.out), &disconnected); err != nil {
		t.Fatal(err)
	}
	if disconnected.Result == nil || disconnected.Result.Status != command.StatusDisconnected {
		t.Fatalf("disconnect result = %+v", disconnected.Result)
	}
}
