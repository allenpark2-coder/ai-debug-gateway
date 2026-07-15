// Package ssh implements the SSH transport: authentication ordering,
// host-key verification, and a persistent PTY/shell stream.
package ssh

import (
	"context"
	"errors"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
)

// Method names an authentication mechanism, in the order AuthFactory
// tries them: ssh-agent first, then configured private keys, then an
// interactive password last.
type Method string

const (
	MethodAgent      Method = "agent"
	MethodPrivateKey Method = "private-key"
	MethodPassword   Method = "password"
)

// AuthMethod pairs an ssh.AuthMethod with the Method that produced it.
type AuthMethod struct {
	Kind   Method
	Method ssh.AuthMethod
}

// HumanSecrets carries secrets entered interactively by the human for
// one connection attempt. Never persisted: profiles never store a
// password or private-key passphrase.
type HumanSecrets struct {
	// KeyPassphrases maps an identity file path to its passphrase, for
	// encrypted private keys.
	KeyPassphrases map[string][]byte
	// Password is the SSH account password, if the human supplied one.
	Password []byte
}

// SecretPrompt describes one secret a human may need to supply before
// AuthFactory.Build can try every configured method for a profile.
type SecretPrompt struct {
	// Kind is "key-passphrase" or "password".
	Kind string
	// IdentityFile is set only when Kind is "key-passphrase".
	IdentityFile string
}

// AuthRequest is the result of inspecting a profile's identity files
// without any secret material: which ones are encrypted, so a caller
// knows what to prompt the human for ahead of Build.
type AuthRequest struct {
	Profile        *profile.SSHConfig
	EncryptedFiles []string
}

// Prompts returns one SecretPrompt per encrypted identity file, plus a
// trailing password prompt (always offered as a fallback method).
func (r AuthRequest) Prompts() []SecretPrompt {
	prompts := make([]SecretPrompt, 0, len(r.EncryptedFiles)+1)
	for _, f := range r.EncryptedFiles {
		prompts = append(prompts, SecretPrompt{Kind: "key-passphrase", IdentityFile: f})
	}
	return append(prompts, SecretPrompt{Kind: "password"})
}

// Inspect reads prof's identity files, without any secret, and reports
// which ones are encrypted. A file that is missing or fails to parse
// for a reason other than a missing passphrase is skipped here (Build
// will also skip it later) rather than failing the whole request.
func Inspect(prof *profile.SSHConfig, readFile func(string) ([]byte, error)) (AuthRequest, error) {
	req := AuthRequest{Profile: prof}
	for _, path := range prof.IdentityFiles {
		data, err := readFile(path)
		if err != nil {
			continue
		}
		if _, err := ssh.ParsePrivateKey(data); err != nil {
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				req.EncryptedFiles = append(req.EncryptedFiles, path)
			}
		}
	}
	return req, nil
}

// AuthFactory builds the ordered list of SSH authentication methods
// for one profile.
type AuthFactory struct {
	// DialAgent opens the ssh-agent socket; overridable in tests.
	DialAgent func() (net.Conn, error)
	// ReadFile reads an identity file's bytes; overridable in tests.
	ReadFile func(path string) ([]byte, error)
}

// NewAuthFactory returns a factory using the real ssh-agent socket
// (from SSH_AUTH_SOCK) and the real filesystem.
func NewAuthFactory() *AuthFactory {
	return &AuthFactory{
		DialAgent: dialDefaultAgent,
		ReadFile:  os.ReadFile,
	}
}

func dialDefaultAgent() (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("ssh: SSH_AUTH_SOCK is not set")
	}
	return net.Dial("unix", sock)
}

// Build returns the ordered auth methods for prof: ssh-agent first (if
// requested and reachable; an unreachable agent is not fatal, later
// methods still apply), then each configured identity file that reads
// and parses, then an interactive password last (only if the human
// supplied one).
func (f *AuthFactory) Build(ctx context.Context, prof *profile.SSHConfig, secrets HumanSecrets) ([]AuthMethod, error) {
	var methods []AuthMethod

	if prof.UseAgent {
		if conn, err := f.DialAgent(); err == nil {
			ag := agent.NewClient(conn)
			methods = append(methods, AuthMethod{Kind: MethodAgent, Method: ssh.PublicKeysCallback(ag.Signers)})
		}
	}

	for _, path := range prof.IdentityFiles {
		signer, err := f.loadSigner(path, secrets)
		if err != nil {
			continue
		}
		methods = append(methods, AuthMethod{Kind: MethodPrivateKey, Method: ssh.PublicKeys(signer)})
	}

	if len(secrets.Password) > 0 {
		methods = append(methods, AuthMethod{Kind: MethodPassword, Method: ssh.Password(string(secrets.Password))})
	}

	if len(methods) == 0 {
		return nil, errors.New("ssh: no usable authentication method for this profile")
	}
	return methods, nil
}

func (f *AuthFactory) loadSigner(path string, secrets HumanSecrets) (ssh.Signer, error) {
	data, err := f.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if pass, ok := secrets.KeyPassphrases[path]; ok && len(pass) > 0 {
		return ssh.ParsePrivateKeyWithPassphrase(data, pass)
	}
	return ssh.ParsePrivateKey(data)
}
