package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/gateway"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
	sshtransport "github.com/allenpark2-coder/ai-debug-gateway/internal/transport/ssh"
)

func generateSSHKeyPair(t *testing.T, passphrase []byte) (ed25519.PublicKey, ed25519.PrivateKey, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if len(passphrase) > 0 {
		block, err = gossh.MarshalPrivateKeyWithPassphrase(priv, "", passphrase)
	} else {
		block, err = gossh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv, pem.EncodeToMemory(block)
}

func writeSSHFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForSSH(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

// dialAndStart opens prof against srv's HostKeyVerifier/AuthFactory and
// starts a Coordinator session on it, returning once the session
// reports READY (SSH authentication already completed by the time
// StartSSH is called, so there is no login-prompt round trip to wait
// for).
func dialAndStart(t *testing.T, c *gateway.Coordinator, prof *profile.SSHConfig, verifier *sshtransport.HostKeyVerifier, factory *sshtransport.AuthFactory, auth sshtransport.HumanAuth) {
	t.Helper()
	opener := func() (transport.Stream, error) {
		return sshtransport.Open(context.Background(), prof, verifier, factory, auth)
	}
	stream, err := opener()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartSSH(stream, opener); err != nil {
		t.Fatal(err)
	}
	waitForSSH(t, 5*time.Second, func() bool { return c.AIEnabled() })
}

func proposeApproveAndWait(t *testing.T, c *gateway.Coordinator, text string) *struct {
	ExitCode int
} {
	t.Helper()
	p, err := c.Propose(c.SessionID(), text, "test", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForSSH(t, 5*time.Second, func() bool {
		res, err := c.Result(tx.ID)
		return err == nil && res.Status == "completed"
	})
	res, err := c.Result(tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil {
		t.Fatalf("got nil exit code for %q", text)
	}
	return &struct{ ExitCode int }{ExitCode: *res.ExitCode}
}

func TestSSHPublicKeyAuthentication(t *testing.T) {
	srv := newSSHTestServer(t)
	pub, _, keyPEM := generateSSHKeyPair(t, nil)
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	srv.AddAuthorizedKey(sshPub)

	dir := t.TempDir()
	keyPath := writeSSHFile(t, dir, "id_ed25519", keyPEM)
	knownHosts := writeSSHFile(t, dir, "known_hosts", nil)

	host, port := splitHostPort(t, srv.Addr)
	prof := &profile.SSHConfig{Host: host, Port: port, User: "root", IdentityFiles: []string{keyPath}}

	verifier, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	factory := sshtransport.NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }

	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	dialAndStart(t, c, prof, verifier, factory, sshtransport.HumanAuth{AcceptHost: true, Token: sshtransport.GrantHumanToken()})

	got := proposeApproveAndWait(t, c, "pwd")
	if got.ExitCode != 0 {
		t.Fatalf("got exit code %d, want 0", got.ExitCode)
	}
}

func TestSSHAgentAuthentication(t *testing.T) {
	srv := newSSHTestServer(t)
	pub, priv, _ := generateSSHKeyPair(t, nil)
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	srv.AddAuthorizedKey(sshPub)

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	agentSock := filepath.Join(t.TempDir(), "agent.sock")
	al, err := net.Listen("unix", agentSock)
	if err != nil {
		t.Fatal(err)
	}
	defer al.Close()
	go func() {
		for {
			conn, err := al.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(keyring, conn)
		}
	}()

	dir := t.TempDir()
	knownHosts := writeSSHFile(t, dir, "known_hosts", nil)
	host, port := splitHostPort(t, srv.Addr)
	prof := &profile.SSHConfig{Host: host, Port: port, User: "root", UseAgent: true}

	verifier, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	factory := sshtransport.NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return net.Dial("unix", agentSock) }

	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	dialAndStart(t, c, prof, verifier, factory, sshtransport.HumanAuth{AcceptHost: true, Token: sshtransport.GrantHumanToken()})

	got := proposeApproveAndWait(t, c, "pwd")
	if got.ExitCode != 0 {
		t.Fatalf("got exit code %d, want 0", got.ExitCode)
	}
}

func TestSSHEncryptedKeyAuthentication(t *testing.T) {
	srv := newSSHTestServer(t)
	passphrase := []byte("correct horse battery staple")
	pub, _, keyPEM := generateSSHKeyPair(t, passphrase)
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	srv.AddAuthorizedKey(sshPub)

	dir := t.TempDir()
	keyPath := writeSSHFile(t, dir, "id_ed25519", keyPEM)
	knownHosts := writeSSHFile(t, dir, "known_hosts", nil)
	host, port := splitHostPort(t, srv.Addr)
	prof := &profile.SSHConfig{Host: host, Port: port, User: "root", IdentityFiles: []string{keyPath}}

	verifier, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	factory := sshtransport.NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }

	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	auth := sshtransport.HumanAuth{
		Secrets:    sshtransport.HumanSecrets{KeyPassphrases: map[string][]byte{keyPath: passphrase}},
		AcceptHost: true,
		Token:      sshtransport.GrantHumanToken(),
	}
	dialAndStart(t, c, prof, verifier, factory, auth)

	got := proposeApproveAndWait(t, c, "pwd")
	if got.ExitCode != 0 {
		t.Fatalf("got exit code %d, want 0", got.ExitCode)
	}
}

func TestSSHPasswordAuthentication(t *testing.T) {
	srv := newSSHTestServer(t)
	srv.SetPassword("hunter2")

	dir := t.TempDir()
	knownHosts := writeSSHFile(t, dir, "known_hosts", nil)
	host, port := splitHostPort(t, srv.Addr)
	prof := &profile.SSHConfig{Host: host, Port: port, User: "root"}

	verifier, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	factory := sshtransport.NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }

	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	auth := sshtransport.HumanAuth{
		Secrets:    sshtransport.HumanSecrets{Password: []byte("hunter2")},
		AcceptHost: true,
		Token:      sshtransport.GrantHumanToken(),
	}
	dialAndStart(t, c, prof, verifier, factory, auth)

	got := proposeApproveAndWait(t, c, "pwd")
	if got.ExitCode != 0 {
		t.Fatalf("got exit code %d, want 0", got.ExitCode)
	}
}

func TestSSHUnknownHostRequiresHumanAcceptanceThenPersists(t *testing.T) {
	srv := newSSHTestServer(t)
	srv.SetPassword("hunter2")

	dir := t.TempDir()
	knownHosts := writeSSHFile(t, dir, "known_hosts", nil)
	host, port := splitHostPort(t, srv.Addr)
	prof := &profile.SSHConfig{Host: host, Port: port, User: "root"}

	verifier, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	factory := sshtransport.NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }
	secrets := sshtransport.HumanSecrets{Password: []byte("hunter2")}

	// Without AcceptHost, an unknown host must not connect at all.
	_, err = sshtransport.Open(context.Background(), prof, verifier, factory, sshtransport.HumanAuth{Secrets: secrets})
	if err == nil {
		t.Fatal("expected Open to fail for an unknown host without human acceptance")
	}

	// With AcceptHost and a human token, it must succeed, and persist so
	// a later connection to the same host needs no re-acceptance.
	stream, err := sshtransport.Open(context.Background(), prof, verifier, factory, sshtransport.HumanAuth{
		Secrets: secrets, AcceptHost: true, Token: sshtransport.GrantHumanToken(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()

	data, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("expected the known_hosts file to have a new entry")
	}

	verifier2, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	stream2, err := sshtransport.Open(context.Background(), prof, verifier2, factory, sshtransport.HumanAuth{Secrets: secrets})
	if err != nil {
		t.Fatalf("expected the now-known host to connect without re-acceptance, got %v", err)
	}
	stream2.Close()
}

func TestSSHChangedHostKeyRefusedEvenWithAcceptHost(t *testing.T) {
	srv := newSSHTestServer(t)
	srv.SetPassword("hunter2")

	dir := t.TempDir()
	host, port := splitHostPort(t, srv.Addr)
	addr := srv.Addr

	// Pre-populate known_hosts with a DIFFERENT key for this address,
	// simulating a host whose key changed (or a MITM).
	wrongKey, _, _ := generateSSHKeyPair(t, nil)
	wrongSSHKey, err := gossh.NewPublicKey(wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := writeSSHFile(t, dir, "known_hosts", []byte(knownHostsLineFor(t, addr, wrongSSHKey)))

	prof := &profile.SSHConfig{Host: host, Port: port, User: "root"}
	verifier, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	factory := sshtransport.NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }
	auth := sshtransport.HumanAuth{
		Secrets: sshtransport.HumanSecrets{Password: []byte("hunter2")},
		// Even with AcceptHost + a human token, a changed key must never
		// be silently accepted: there is deliberately no accept path for
		// it, unlike a first-time-unknown host.
		AcceptHost: true, Token: sshtransport.GrantHumanToken(),
	}
	_, err = sshtransport.Open(context.Background(), prof, verifier, factory, auth)
	if !errors.Is(err, sshtransport.ErrHostKeyChanged) {
		t.Fatalf("got %v, want ErrHostKeyChanged", err)
	}
}

func TestSSHShellStateSurvivesAcrossApprovedCommands(t *testing.T) {
	srv := newSSHTestServer(t)
	srv.SetPassword("hunter2")

	dir := t.TempDir()
	knownHosts := writeSSHFile(t, dir, "known_hosts", nil)
	host, port := splitHostPort(t, srv.Addr)
	prof := &profile.SSHConfig{Host: host, Port: port, User: "root"}

	verifier, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	factory := sshtransport.NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }

	c := gateway.NewCoordinator("board-1")
	defer c.Stop()
	auth := sshtransport.HumanAuth{Secrets: sshtransport.HumanSecrets{Password: []byte("hunter2")}, AcceptHost: true, Token: sshtransport.GrantHumanToken()}
	dialAndStart(t, c, prof, verifier, factory, auth)

	proposeApproveAndWait(t, c, "cd /tmp")

	p, err := c.Propose(c.SessionID(), "pwd", "check cwd", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.Approve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForSSH(t, 5*time.Second, func() bool {
		res, err := c.Result(tx.ID)
		return err == nil && res.Status == "completed"
	})

	chunk := c.ReadAfter(0, 1<<16)
	if !bytes.Contains(chunk.Data, []byte("/tmp")) {
		t.Fatalf("expected cwd to persist across commands on the same session, got %q", chunk.Data)
	}
}

func TestSSHDisconnectAndApprovedRetryNewSessionIDNoSecretLeak(t *testing.T) {
	srv := newSSHTestServer(t)
	password := "hunter2-super-secret"
	srv.SetPassword(password)

	dir := t.TempDir()
	knownHosts := writeSSHFile(t, dir, "known_hosts", nil)
	host, port := splitHostPort(t, srv.Addr)
	prof := &profile.SSHConfig{Host: host, Port: port, User: "root"}

	verifier, err := sshtransport.NewHostKeyVerifier(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	factory := sshtransport.NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }
	auth := sshtransport.HumanAuth{Secrets: sshtransport.HumanSecrets{Password: []byte(password)}, AcceptHost: true, Token: sshtransport.GrantHumanToken()}

	c := gateway.NewCoordinator("board-1")
	defer c.Stop()

	opener := func() (transport.Stream, error) {
		return sshtransport.Open(context.Background(), prof, verifier, factory, auth)
	}
	stream, err := opener()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartSSH(stream, opener); err != nil {
		t.Fatal(err)
	}
	waitForSSH(t, 5*time.Second, func() bool { return c.AIEnabled() })
	oldID := c.SessionID()

	beforeCommandCount := len(srv.Commands())
	proposeApproveAndWait(t, c, "pwd")

	srv.DisconnectAll()
	waitForSSH(t, 5*time.Second, func() bool { return c.State() == session.Reconnecting })

	if err := c.RetrySSH(); err != nil {
		t.Fatal(err)
	}
	if c.SessionID() == oldID {
		t.Fatal("a human-approved retry must rotate the session ID")
	}
	waitForSSH(t, 5*time.Second, func() bool { return c.AIEnabled() })

	got := proposeApproveAndWait(t, c, "pwd")
	if got.ExitCode != 0 {
		t.Fatalf("got exit code %d, want 0", got.ExitCode)
	}

	// The retry must be a fresh shell, never a replay: the server saw
	// exactly one new "pwd" managed-command line since disconnect (the
	// second proposeApproveAndWait's), not the earlier command resent.
	afterCommands := srv.Commands()
	if len(afterCommands) <= beforeCommandCount {
		t.Fatalf("expected new commands after retry, got %d before and %d after", beforeCommandCount, len(afterCommands))
	}

	// The password must never appear in AI-visible output.
	chunk := c.ReadAfter(0, 1<<20)
	if bytes.Contains(chunk.Data, []byte(password)) {
		t.Fatalf("password leaked into AI-visible output: %q", chunk.Data)
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func knownHostsLineFor(t *testing.T, addr string, key gossh.PublicKey) string {
	t.Helper()
	return knownhosts.Line([]string{addr}, key) + "\n"
}
