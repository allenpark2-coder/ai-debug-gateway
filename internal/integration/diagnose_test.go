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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/cli"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport/serial"
)

type diagnoseHarness struct {
	home, dataDir, gateway string
	srv                    *sshTestServer
	daemon                 *exec.Cmd
	log                    lockedBuffer
	session                string
	canary                 string
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func newDiagnoseHarness(t *testing.T, automatic bool) *diagnoseHarness {
	t.Helper()
	h := &diagnoseHarness{home: shortTempDir(t), gateway: buildBinary(t, "gateway"), srv: newSSHTestServer(t), canary: "TASK6-CANARY-secret-9f31"}
	h.srv.SetPassword(h.canary)
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
	out, err := attach.SessionStartWithOptions("board-1", cli.SessionStartOptions{SSHPassword: h.canary, SSHAcceptHost: true})
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
	// Exercise the explicit secret window with the same known canary used for
	// authentication. The test SSH shell echoes unknown input, so this proves
	// target echo is filtered rather than merely relying on SSH not echoing its
	// authentication exchange.
	if err := attach.SecretBegin(); err != nil {
		t.Fatal(err)
	}
	if err := attach.TransportWrite([]byte(h.canary + "\n")); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, targetLine := range h.srv.Commands() {
			if targetLine == h.canary {
				goto canaryObserved
			}
		}
	}
	t.Fatal("secret canary did not traverse the target echo path before deadline")
canaryObserved:
	if err := attach.SecretDone(); err != nil {
		t.Fatal(err)
	}
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
	commands := h.srv.Commands()
	if len(commands) != before+2 {
		t.Fatalf("approved mutation did not reach target before deadline: got commands %v", commands)
	}
	if !strings.HasPrefix(commands[len(commands)-1], "echo mutation; printf") {
		t.Fatalf("last target command = %q, want approved mutation with marker", commands[len(commands)-1])
	}
	exported, err := control.RecordsExport(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exported), confirmation) || !strings.Contains(string(exported), "confirmation=[redacted] bytes=26") {
		t.Fatalf("audit did not record redacted confirmation metadata: %s", exported)
	}
	surfaces := map[string]string{
		"diagnose stdout":                 stdout,
		"diagnose stderr":                 stderr,
		"daemon stdout/stderr":            h.log.String(),
		"transaction output":              string(got.Result.Output),
		"records export transcript+audit": string(exported),
	}
	for name, surface := range surfaces {
		if strings.Contains(surface, h.canary) {
			t.Fatalf("authentication canary leaked through %s: %q", name, surface)
		}
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
	// SSH has no target prompt detector, so timeout deliberately disconnects
	// the shell; another transaction cannot be attributed until reconnect.
	time.Sleep(150 * time.Millisecond)
	if _, _, err := h.runGateway(t, "diagnose", "--session", h.session, "--text", "ps", "--purpose", "post-timeout check", "--timeout-ms", "5000"); err == nil {
		t.Fatal("post-timeout diagnose unexpectedly reused an unresynchronized SSH shell")
	}
}

func TestDiagnoseRealUARTBinariesReportTargetRebooted(t *testing.T) {
	home := shortTempDir(t)
	dataDir := filepath.Join(home, ".local", "share", "ai-debug-gateway")
	profileDir := filepath.Join(home, ".config", "ai-debug-gateway", "profiles")
	policyDir := filepath.Join(home, ".config", "ai-debug-gateway", "policies")
	byIDDir := filepath.Join(home, "serial-by-id")
	for _, dir := range []string{profileDir, policyDir, byIDDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fc := newFakeConsole(t)
	devicePath := fc.slave.Name()
	// The compiled daemon opens the slave device; the scripted target owns the
	// master. Swap the helper's endpoints because its in-process tests hand the
	// master directly to Coordinator instead.
	fc.master, fc.slave = fc.slave, fc.master
	fc.masterFd, fc.slaveFd = fc.slaveFd, fc.masterFd
	identityPath := filepath.Join(byIDDir, "usb-test-uart")
	if err := os.Symlink(devicePath, identityPath); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save(profileDir, profile.Profile{Name: "board-1", UART: &profile.UARTConfig{
		Identity: transport.Identity{Kind: "usb-serial-by-id", Key: identityPath},
		Line:     serial.LineSettings{BaudRate: 115200, DataBits: 8, Parity: serial.ParityNone, StopBits: 1, Flow: serial.FlowNone},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "board-1.json"), []byte(`{"allow":[],"deny":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var daemonLog lockedBuffer
	daemon := exec.Command(buildBinaryWithTags(t, "gatewayd", "integrationtest"), "--auto-readonly")
	daemon.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=", "GATEWAYD_BOARD=board-1", "GATEWAYD_INTEGRATION_SERIAL_BY_ID_DIR="+byIDDir)
	daemon.Stdout, daemon.Stderr = &daemonLog, &daemonLog
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Signal(syscall.SIGTERM)
		_ = daemon.Wait()
		if t.Failed() {
			t.Log(daemonLog.String())
		}
	})
	waitPath(t, filepath.Join(dataDir, "gatewayd.diagnose.sock"))
	fc.run()
	attach, err := cli.Dial(filepath.Join(dataDir, "gatewayd.attach.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer attach.Close()
	startedJSON, err := attach.SessionStart("board-1")
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(startedJSON, &started); err != nil {
		t.Fatal(err)
	}

	fc.rebootOn.Store(true)
	gateway := exec.Command(buildBinary(t, "gateway"), "diagnose", "--session", started.SessionID, "--text", "ps", "--purpose", "observe reboot", "--timeout-ms", "3000")
	gateway.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=")
	output, err := gateway.Output()
	if err != nil {
		t.Fatal(err)
	}
	var result cli.DiagnoseResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Result == nil || result.Result.Status != command.StatusTargetRebooted || result.Result.ExitCode != nil {
		t.Fatalf("result = %+v, want target-rebooted without exit code", result.Result)
	}
}
