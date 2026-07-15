package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
)

func methodKinds(methods []AuthMethod) []Method {
	out := make([]Method, len(methods))
	for i, m := range methods {
		out[i] = m.Kind
	}
	return out
}

// generateKeyPEM returns a fresh ed25519 key pair marshaled as an
// OpenSSH-format PEM block, encrypted with passphrase when non-nil.
func generateKeyPEM(t *testing.T, passphrase []byte) (ed25519.PrivateKey, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if len(passphrase) > 0 {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", passphrase)
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	return priv, pem.EncodeToMemory(block)
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFileHelper(path string) ([]byte, error) { return os.ReadFile(path) }

// startFakeAgent runs a real ssh-agent protocol server over a Unix
// socket, backed by priv, and returns a DialAgent-compatible dialer.
func startFakeAgent(t *testing.T, priv ed25519.PrivateKey) func() (net.Conn, error) {
	t.Helper()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}

	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(keyring, conn)
		}
	}()

	return func() (net.Conn, error) { return net.Dial("unix", l.Addr().String()) }
}

func TestAuthFactoryOrdersAgentThenKeyThenPassword(t *testing.T) {
	agentPriv, _ := generateKeyPEM(t, nil)
	dialAgent := startFakeAgent(t, agentPriv)

	_, keyPEM := generateKeyPEM(t, nil)
	dir := t.TempDir()
	keyPath := writeFile(t, dir, "id_ed25519", keyPEM)

	prof := &profile.SSHConfig{
		Host:          "example",
		User:          "root",
		IdentityFiles: []string{keyPath},
		UseAgent:      true,
	}
	factory := NewAuthFactory()
	factory.DialAgent = dialAgent

	want := []Method{MethodAgent, MethodPrivateKey, MethodPassword}
	got, err := factory.Build(context.Background(), prof, HumanSecrets{Password: []byte("hunter2")})
	if err != nil || !slices.Equal(methodKinds(got), want) {
		t.Fatalf("got %+v, err=%v", got, err)
	}
}

func TestAuthFactorySkipsAgentWhenUnavailable(t *testing.T) {
	_, keyPEM := generateKeyPEM(t, nil)
	dir := t.TempDir()
	keyPath := writeFile(t, dir, "id_ed25519", keyPEM)

	prof := &profile.SSHConfig{
		Host:          "example",
		User:          "root",
		IdentityFiles: []string{keyPath},
		UseAgent:      true,
	}
	factory := NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }

	got, err := factory.Build(context.Background(), prof, HumanSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Method{MethodPrivateKey}
	if !slices.Equal(methodKinds(got), want) {
		t.Fatalf("got %+v", got)
	}
}

func TestAuthFactoryUsesPassphraseForEncryptedKey(t *testing.T) {
	passphrase := []byte("correct horse battery staple")
	_, keyPEM := generateKeyPEM(t, passphrase)
	dir := t.TempDir()
	keyPath := writeFile(t, dir, "id_ed25519", keyPEM)

	prof := &profile.SSHConfig{
		Host:          "example",
		User:          "root",
		IdentityFiles: []string{keyPath},
	}
	factory := NewAuthFactory()

	got, err := factory.Build(context.Background(), prof, HumanSecrets{
		KeyPassphrases: map[string][]byte{keyPath: passphrase},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(methodKinds(got), []Method{MethodPrivateKey}) {
		t.Fatalf("got %+v", got)
	}
}

func TestAuthFactoryFailsWithNoUsableMethod(t *testing.T) {
	prof := &profile.SSHConfig{Host: "example", User: "root"}
	factory := NewAuthFactory()
	factory.DialAgent = func() (net.Conn, error) { return nil, errors.New("no agent") }

	if _, err := factory.Build(context.Background(), prof, HumanSecrets{}); err == nil {
		t.Fatal("expected an error when no method is usable")
	}
}

func TestInspectReportsEncryptedIdentityFiles(t *testing.T) {
	passphrase := []byte("s3cr3t")
	_, encPEM := generateKeyPEM(t, passphrase)
	_, plainPEM := generateKeyPEM(t, nil)
	dir := t.TempDir()
	encPath := writeFile(t, dir, "encrypted", encPEM)
	plainPath := writeFile(t, dir, "plain", plainPEM)

	prof := &profile.SSHConfig{IdentityFiles: []string{encPath, plainPath}}
	req, err := Inspect(prof, readFileHelper)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(req.EncryptedFiles, []string{encPath}) {
		t.Fatalf("got %+v", req.EncryptedFiles)
	}

	prompts := req.Prompts()
	if len(prompts) != 2 {
		t.Fatalf("got %+v, want a key-passphrase prompt plus a password fallback", prompts)
	}
	if prompts[0].Kind != "key-passphrase" || prompts[0].IdentityFile != encPath {
		t.Fatalf("got %+v", prompts[0])
	}
	if prompts[1].Kind != "password" {
		t.Fatalf("got %+v", prompts[1])
	}
}
