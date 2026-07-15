package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/cli"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/gateway"
)

// TestSecretWindowSurvivesTakeoverUntilExplicitDone covers the
// cancellation stage from the hardening plan's adversarial secret
// list: a human takeover ends the running transaction, but the secret
// window it interrupted (e.g. a sudo prompt unrelated to whether the
// transaction itself gets cancelled) must stay open until the human
// explicitly ends it -- Takeover has no reason to know whether
// whatever opened the window has actually been resolved.
func TestSecretWindowSurvivesTakeoverUntilExplicitDone(t *testing.T) {
	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	fc := newFakeConsole(t)
	startAuthenticated(t, c, fc, nil)

	p, err := c.Propose("s1", "sudo do-a-thing", "needs sudo", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Approve(p.ID); err != nil {
		t.Fatal(err)
	}

	fc.write("[sudo] password: ")
	waitFor(t, waitTimeout, func() bool { return c.SecretActive() })

	if err := c.Takeover(); err != nil {
		t.Fatal(err)
	}
	if !c.SecretActive() {
		t.Fatal("takeover must never silently close an open secret window")
	}

	secret := []byte("still-typing-the-password")
	beforeLen := len(c.ReadAfter(0, 1<<16).Data)
	fc.write(string(secret))
	waitFor(t, waitTimeout, func() bool { return len(c.ReadAfter(0, 1<<16).Data) > beforeLen })
	if bytes.Contains(c.ReadAfter(0, 1<<16).Data, secret) {
		t.Fatal("secret bytes echoed after takeover still leaked into AI-visible output")
	}

	c.EndSecret()
	if c.SecretActive() {
		t.Fatal("EndSecret must close the window")
	}
	waitFor(t, waitTimeout, func() bool { return c.AIEnabled() })
}

// TestSecretWindowNeverAutoExpiresOnGracePeriod covers the "timeout"
// stage from the hardening plan's adversarial secret list: the grace
// period only extends an in-flight transaction's deadline (see
// onData), it must never be mistaken for -- or implemented as -- an
// auto-close timer on the window itself.
func TestSecretWindowNeverAutoExpiresOnGracePeriod(t *testing.T) {
	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	fc := newFakeConsole(t)
	fc.run()

	cfg := fakeLoginConfig()
	cfg.SecretGracePeriod = 100 * time.Millisecond
	if err := c.StartUART(fc.stream(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, waitTimeout, func() bool { return c.AIEnabled() })

	fc.write("[sudo] password: ")
	waitFor(t, waitTimeout, func() bool { return c.SecretActive() })

	time.Sleep(5 * cfg.SecretGracePeriod)
	if !c.SecretActive() {
		t.Fatal("the secret window must never auto-expire on its own, only on a recognized prompt or secret-done")
	}

	c.EndSecret()
	if c.SecretActive() {
		t.Fatal("EndSecret must close the window")
	}
}

// TestSecretNeverPersistsAcrossRestart drives a secret entry over a
// real SSH session against the real gatewayd binary, kills the daemon,
// restarts it, and recursively searches every durable file the daemon
// writes (transcript, audit, open-transaction tracking) for the raw
// secret -- not just the live in-memory ring the other secret tests
// check.
func TestSecretNeverPersistsAcrossRestart(t *testing.T) {
	h := newRecoveryHarness(t)
	h.start(t)
	h.startSSHSession(t)

	attach := h.dialAttach(t)
	secret := "S3cr3t-P@ssphrase-9f3c1a"
	if err := attach.SecretBegin(); err != nil {
		t.Fatal(err)
	}
	// The fake SSH server's default handler echoes back whatever line
	// it receives, standing in for a misconfigured remote echoing a
	// typed secret.
	if err := attach.TransportWrite([]byte(secret + "\n")); err != nil {
		t.Fatal(err)
	}
	// Confirm the redaction placeholder -- not the secret -- is what
	// actually reached AI-visible output before ending the window.
	waitForOutputContains(t, attach, "[redacted", waitTimeout)
	if err := attach.SecretDone(); err != nil {
		t.Fatal(err)
	}

	h.kill(t)
	h.start(t)

	err := filepath.Walk(h.dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			// Skip the control/attach sockets themselves: they are not
			// regular files, and reading one fails with ENXIO.
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("secret leaked into durable file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestControlConnectionDeniedOpsNeverReachTarget confirms, against the
// real running system (not just the protocol-level role gate already
// covered in internal/ipc), that a denied control-connection operation
// produces zero bytes on the wire to the target.
func TestControlConnectionDeniedOpsNeverReachTarget(t *testing.T) {
	h := newRecoveryHarness(t)
	h.start(t)
	h.startSSHSession(t)

	control := h.dialControl(t)
	before := len(h.srv.Commands())

	if err := control.TransportWrite([]byte("should-not-arrive\n")); err == nil {
		t.Fatal("expected transport.write to be denied on a control connection")
	}
	if err := control.SecretBegin(); err == nil {
		t.Fatal("expected secret.begin to be denied on a control connection")
	}
	if _, err := control.Takeover(); err == nil {
		t.Fatal("expected takeover to be denied on a control connection")
	}

	time.Sleep(100 * time.Millisecond) // let anything that would have been sent arrive
	if got := len(h.srv.Commands()); got != before {
		t.Fatalf("target saw %d new lines after denied control-connection operations, want 0", got-before)
	}
}

// TestControlConnectionCannotAcceptUnknownHostKey confirms end to end,
// through the real daemon, that a control (AI) connection's
// ssh_accept_host is never honored: cmd/gatewayd/ssh_test.go already
// proves this at the dispatcher-unit level, this proves the whole
// wire path refuses the same way.
func TestControlConnectionCannotAcceptUnknownHostKey(t *testing.T) {
	h := newRecoveryHarness(t)
	h.start(t)

	control := h.dialControl(t)
	_, err := control.SessionStartWithOptions("board-1", cli.SessionStartOptions{
		SSHPassword:   recoverySSHPassword,
		SSHAcceptHost: true,
	})
	if err == nil {
		t.Fatal("expected session.start to fail: a control (AI) connection must never accept an unknown host key")
	}

	status, serr := control.SessionStatus()
	if serr != nil {
		t.Fatal(serr)
	}
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(status, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != "DISCONNECTED" {
		t.Fatalf("got state %q, want DISCONNECTED after a refused connection attempt", st.State)
	}
}
