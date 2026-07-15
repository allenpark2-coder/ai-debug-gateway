package integration

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/cli"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
)

type unsafeShellHarness struct {
	home, dataDir, gateway string
	srv                    *sshTestServer
	daemon                 *exec.Cmd
	log                    lockedBuffer
	session                string
}

// newUnsafeShellHarness starts the real gatewayd and gateway binaries
// against a local SSH server. When enabled is true, unsafeFileBody is
// written as the board's owner-only unsafe-shell file before the daemon
// starts; an empty unsafeFileBody with enabled true is used by tests that
// want the flag on but the file absent (startup must degrade, not crash).
func newUnsafeShellHarness(t *testing.T, enabled bool, unsafeFileBody string) *unsafeShellHarness {
	t.Helper()
	h := &unsafeShellHarness{home: shortTempDir(t), gateway: buildBinary(t, "gateway"), srv: newSSHTestServer(t)}
	h.srv.SetPassword("unsafe-shell-test-pw")
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

	args := []string{}
	if enabled {
		shellDir := filepath.Join(h.home, ".config", "ai-debug-gateway", "unsafe-shell")
		if err := os.MkdirAll(shellDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if unsafeFileBody != "" {
			if err := os.WriteFile(filepath.Join(shellDir, "board-1.json"), []byte(unsafeFileBody), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		args = append(args, "--unsafe-auto-shell=board-1")
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

	attach, err := cli.Dial(filepath.Join(h.dataDir, "gatewayd.attach.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer attach.Close()
	out, err := attach.SessionStartWithOptions("board-1", cli.SessionStartOptions{SSHPassword: "unsafe-shell-test-pw", SSHAcceptHost: true})
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

func (h *unsafeShellHarness) runGateway(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(h.gateway, args...)
	cmd.Env = append(os.Environ(), "HOME="+h.home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestUnsafeShellSocketIsOptInInRealDaemon(t *testing.T) {
	h := newUnsafeShellHarness(t, false, "")
	if _, err := os.Stat(filepath.Join(h.dataDir, "gatewayd.unsafeshell.sock")); !os.IsNotExist(err) {
		t.Fatalf("unsafe-shell socket exists without opt-in: %v", err)
	}
}

func TestUnsafeShellRequiresRiskAcceptedFileWithoutCrashingTheDaemon(t *testing.T) {
	// The flag is passed but no file is written: risk_accepted cannot be
	// true, so only this socket must be disabled -- attach must still work.
	h := newUnsafeShellHarness(t, true, "")
	if _, err := os.Stat(filepath.Join(h.dataDir, "gatewayd.unsafeshell.sock")); !os.IsNotExist(err) {
		t.Fatalf("unsafe-shell socket exists despite a missing risk_accepted file: %v", err)
	}
	if _, err := cli.Dial(filepath.Join(h.dataDir, "gatewayd.attach.sock")); err != nil {
		t.Fatalf("attach socket unavailable after unsafe-shell misconfiguration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "gatewayd.control.sock")); err != nil {
		t.Fatalf("control socket unavailable after unsafe-shell misconfiguration: %v", err)
	}
}

func TestUnsafeShellRealBinariesExecuteMutationsAndEnforceHardDenials(t *testing.T) {
	h := newUnsafeShellHarness(t, true, `{"risk_accepted":true,"deny_executables":["reboot"]}`)
	waitPath(t, filepath.Join(h.dataDir, "gatewayd.unsafeshell.sock"))

	before := len(h.srv.Commands())
	stdout, stderr, err := h.runGateway(t, "unsafe-shell", "--session", h.session, "--text", "mount -o remount,rw /", "--purpose", "need rw", "--timeout-ms", "2000")
	if err != nil {
		t.Fatalf("unsafe-shell mount: %v stderr=%s", err, stderr)
	}
	if stderr != "mount -o remount,rw /\n" {
		t.Fatalf("displayed command = %q", stderr)
	}
	var got cli.UnsafeShellResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Decision.Allowed || got.Result == nil || got.Result.Status != command.StatusCompleted {
		t.Fatalf("a previously-forbidden mutating command must complete automatically: %+v", got)
	}
	if len(h.srv.Commands()) != before+1 {
		t.Fatalf("mount did not reach target exactly once: %v", h.srv.Commands())
	}

	rejected := map[string]string{
		"hard-denied interpreter":           "sh -c 'id'",
		"hard-denied eval":                  "eval 'id'",
		"hard-denied command substitution":  "cat $(id)",
		"hard-denied backtick substitution": "`id`",
		"hard-denied indirect exec":         "env id",
		"hard-denied find -exec":            "find / -exec id {} \\;",
		"board-denied executable":           "reboot",
		"sensitive path read":               "cat /etc/shadow",
	}
	for name, text := range rejected {
		t.Run(name, func(t *testing.T) {
			count := len(h.srv.Commands())
			stdout, _, err := h.runGateway(t, "unsafe-shell", "--session", h.session, "--text", text, "--purpose", "attempt", "--timeout-ms", "1000")
			if err != nil {
				t.Fatalf("unsafe-shell rejection %q: %v", text, err)
			}
			var result cli.UnsafeShellResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatal(err)
			}
			if result.Decision.Allowed || result.Transaction != nil {
				t.Fatalf("%q unexpectedly executed: %+v", text, result)
			}
			if len(h.srv.Commands()) != count {
				t.Fatalf("rejected %q reached target: %v", text, h.srv.Commands()[count:])
			}
		})
	}
}

func TestUnsafeShellAndDiagnoseCoexistWithoutInterference(t *testing.T) {
	h := newUnsafeShellHarness(t, true, `{"risk_accepted":true}`)
	// Layer --auto-readonly onto the same running daemon setup by starting
	// a second harness process is unnecessary: enabling both flags on one
	// daemon is exactly what production does, so restart with both.
	_ = h.daemon.Process.Signal(syscall.SIGTERM)
	_ = h.daemon.Wait()

	policyDir := filepath.Join(h.home, ".config", "ai-debug-gateway", "policies")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "board-1.json"), []byte(`{"allow":[],"deny":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var log2 lockedBuffer
	h.daemon = exec.Command(buildBinary(t, "gatewayd"), "--auto-readonly", "--unsafe-auto-shell=board-1")
	h.daemon.Env = append(os.Environ(), "HOME="+h.home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=", "GATEWAYD_BOARD=board-1")
	h.daemon.Stdout, h.daemon.Stderr = &log2, &log2
	if err := h.daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.daemon.Process.Signal(syscall.SIGTERM)
		_ = h.daemon.Wait()
		if t.Failed() {
			t.Log(log2.String())
		}
	})
	waitPath(t, filepath.Join(h.dataDir, "gatewayd.diagnose.sock"))
	waitPath(t, filepath.Join(h.dataDir, "gatewayd.unsafeshell.sock"))

	attach, err := cli.Dial(filepath.Join(h.dataDir, "gatewayd.attach.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer attach.Close()
	out, err := attach.SessionStartWithOptions("board-1", cli.SessionStartOptions{SSHPassword: "unsafe-shell-test-pw", SSHAcceptHost: true})
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

	// diagnose (read-only allowlist) still runs its own command.
	stdout, _, err := h.runGateway(t, "diagnose", "--session", h.session, "--text", "ps", "--purpose", "inspect", "--timeout-ms", "2000")
	if err != nil {
		t.Fatal(err)
	}
	var diag cli.DiagnoseResult
	if err := json.Unmarshal([]byte(stdout), &diag); err != nil {
		t.Fatal(err)
	}
	if !diag.Decision.Allowed || diag.Result == nil || diag.Result.Status != command.StatusCompleted {
		t.Fatalf("diagnose result = %+v", diag)
	}

	// unsafe-shell still runs a mutating command the diagnose policy would
	// have refused.
	stdout, _, err = h.runGateway(t, "unsafe-shell", "--session", h.session, "--text", "mount -o remount,rw /", "--purpose", "need rw", "--timeout-ms", "2000")
	if err != nil {
		t.Fatal(err)
	}
	var unsafe cli.UnsafeShellResult
	if err := json.Unmarshal([]byte(stdout), &unsafe); err != nil {
		t.Fatal(err)
	}
	if !unsafe.Decision.Allowed || unsafe.Result == nil || unsafe.Result.Status != command.StatusCompleted {
		t.Fatalf("unsafe-shell result = %+v", unsafe)
	}

	// diagnose must still refuse what its own allowlist refuses, unaffected
	// by unsafe-shell mode being enabled on the same daemon.
	stdout, _, err = h.runGateway(t, "diagnose", "--session", h.session, "--text", "mount -o remount,rw /", "--purpose", "attempt", "--timeout-ms", "1000")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stdout), &diag); err != nil {
		t.Fatal(err)
	}
	if diag.Decision.Allowed {
		t.Fatalf("diagnose policy was weakened by unsafe-shell mode: %+v", diag)
	}
}
