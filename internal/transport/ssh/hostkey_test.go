package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// testAddr stands in for a real net.Addr in Check calls: knownhosts
// dereferences it internally, so nil panics.
var testAddr net.Addr = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}

func newTestPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshPub
}

func TestHostKeyVerifierKnownHostSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	key := newTestPublicKey(t)
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"example:22"}, key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	v, err := NewHostKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Check("example:22", testAddr, key); err != nil {
		t.Fatalf("known host must succeed, got %v", err)
	}
}

func TestHostKeyVerifierUnknownHostRequiresHuman(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	v, err := NewHostKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	key := newTestPublicKey(t)
	err = v.Check("newhost:22", testAddr, key)
	if !errors.Is(err, ErrUnknownHostRequiresHuman) {
		t.Fatalf("got %v, want ErrUnknownHostRequiresHuman", err)
	}
}

func TestHostKeyVerifierAcceptNewRequiresHumanToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := NewHostKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	key := newTestPublicKey(t)

	// A zero-value token (what an AI-facing code path would have, since
	// only the interactive attach path can call GrantHumanToken) must
	// be refused.
	if err := v.AcceptNewHuman(HumanToken{}, "newhost:22", key); err == nil {
		t.Fatal("expected AcceptNewHuman to reject a non-human token")
	}

	if err := v.AcceptNewHuman(GrantHumanToken(), "newhost:22", key); err != nil {
		t.Fatalf("expected a human token to succeed, got %v", err)
	}
	if err := v.Check("newhost:22", testAddr, key); err != nil {
		t.Fatalf("host must be known immediately after human acceptance, got %v", err)
	}
}

func TestHostKeyVerifierAcceptNewHumanAppendsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := NewHostKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	key := newTestPublicKey(t)

	if err := v.AcceptNewHuman(GrantHumanToken(), "newhost:22", key); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("got mode %v, want 0600", info.Mode().Perm())
	}
}

func TestHostKeyVerifierChangedKeyFailsWithoutAcceptPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	oldKey := newTestPublicKey(t)
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"example:22"}, oldKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	v, err := NewHostKeyVerifier(path)
	if err != nil {
		t.Fatal(err)
	}
	newKey := newTestPublicKey(t)
	err = v.Check("example:22", testAddr, newKey)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("got %v, want ErrHostKeyChanged", err)
	}

	// There is deliberately no accept path for a changed key: calling
	// AcceptNewHuman here would append a second, conflicting line
	// rather than resolve the mismatch, so callers must never do it.
	// This test only asserts that Check itself never returns anything
	// resembling success.
	if err == nil {
		t.Fatal("changed host key must never verify as success")
	}
}
