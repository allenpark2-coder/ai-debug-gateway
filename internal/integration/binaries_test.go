package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/cli"
)

// buildBinary builds cmd/<name> once per test process into a temp dir
// and returns the executable path, so this exercises the real
// compiled binary rather than calling Go code directly in-process.
func buildBinary(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", out, "./cmd/"+name)
	cmd.Dir = repoRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", name, err, output)
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/integration -> repo root
	return filepath.Join(dir, "..", "..")
}

// shortTempDir returns a fresh directory with a short, fixed-prefix
// path, cleaned up at test end. Unlike t.TempDir, it does not embed
// the test's own name: AF_UNIX socket paths are capped at ~108 bytes
// (sizeof sockaddr_un.sun_path on Linux), and a long test name
// (confirmed: "TestRealDaemonControlConnectionCannotApprove") plus the
// nested XDG data-dir components below it is enough to blow past that
// on its own.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// startDaemon launches a real gatewayd binary against an isolated
// HOME, so it never touches the operator's real config/data, and
// returns the resolved control/attach socket paths once they exist.
func startDaemon(t *testing.T) (controlSock, attachSock string) {
	t.Helper()
	bin := buildBinary(t, "gatewayd")
	home := shortTempDir(t)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon output: %s", outBuf.String())
		}
	})
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
		}
	})

	dataDir := filepath.Join(home, ".local", "share", "ai-debug-gateway")
	controlSock = filepath.Join(dataDir, "gatewayd.control.sock")
	attachSock = filepath.Join(dataDir, "gatewayd.attach.sock")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(controlSock); err == nil {
			if _, err := os.Stat(attachSock); err == nil {
				return controlSock, attachSock
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("gatewayd did not create its sockets in time")
	return "", ""
}

func TestRealDaemonSocketsAreOwnerOnly(t *testing.T) {
	controlSock, attachSock := startDaemon(t)
	for _, path := range []string{controlSock, attachSock} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: got mode %v, want 0600", path, info.Mode().Perm())
		}
	}
}

func TestRealDaemonPortsStatusAndExport(t *testing.T) {
	controlSock, _ := startDaemon(t)

	c, err := cli.Dial(controlSock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, err := c.PortsList(); err != nil {
		t.Fatal(err)
	}

	statusResult, err := c.SessionStatus()
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(statusResult, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "DISCONNECTED" {
		t.Fatalf("got state %q, want DISCONNECTED before any session starts", status.State)
	}

	exportResult, err := c.RecordsExport(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var export struct {
		Transcript json.RawMessage `json:"transcript"`
		Audit      json.RawMessage `json:"audit"`
	}
	if err := json.Unmarshal(exportResult, &export); err != nil {
		t.Fatal(err)
	}
}

func TestRealDaemonControlConnectionCannotApprove(t *testing.T) {
	controlSock, _ := startDaemon(t)

	c, err := cli.Dial(controlSock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, err := c.CommandApprove("nonexistent"); err == nil {
		t.Fatal("expected a control connection to be refused command.approve")
	}
}

func TestRealDaemonRejectsSecondInstance(t *testing.T) {
	bin := buildBinary(t, "gatewayd")
	home := shortTempDir(t)
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME=", "XDG_DATA_HOME=")

	first := exec.Command(bin)
	first.Env = env
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		first.Process.Signal(syscall.SIGTERM)
		first.Wait()
	}()

	dataDir := filepath.Join(home, ".local", "share", "ai-debug-gateway")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dataDir, "gatewayd.lock")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	second := exec.Command(bin)
	second.Env = env
	out, err := second.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a second instance to fail to start, got clean exit with output: %s", out)
	}
}
