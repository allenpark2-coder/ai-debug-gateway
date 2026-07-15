package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	gossh "golang.org/x/crypto/ssh"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/profile"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// HumanAuth bundles what only an interactive human client may supply
// for one connection attempt: secrets for AuthFactory, and (for a new
// host) explicit acceptance with a HumanToken.
type HumanAuth struct {
	Secrets    HumanSecrets
	AcceptHost bool
	Token      HumanToken
}

// Stream adapts a persistent SSH PTY/shell session to transport.Stream.
// The remote shell's stdout and stderr are both connected to the
// requested PTY, so they already arrive merged and in order on the
// same channel; no separate multiplexing is needed on this side.
type Stream struct {
	client  *gossh.Client
	session *gossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader

	identity transport.Identity

	closeOnce sync.Once
	closeErr  error
}

// dialStream opens addr, requests a PTY and shell on one session, and
// adapts it to a Stream. Tests use this directly with a bare
// ClientConfig; Open builds that config from a profile.
func dialStream(addr string, config *gossh.ClientConfig) (*Stream, error) {
	client, err := gossh.Dial("tcp", addr, config)
	if err != nil {
		return nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}

	modes := gossh.TerminalModes{
		gossh.ECHO:          0,
		gossh.TTY_OP_ISPEED: 38400,
		gossh.TTY_OP_OSPEED: 38400,
	}
	if err := session.RequestPty("xterm-256color", 40, 132, modes); err != nil {
		session.Close()
		client.Close()
		return nil, err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, err
	}

	if err := session.Shell(); err != nil {
		session.Close()
		client.Close()
		return nil, err
	}

	return &Stream{
		client:   client,
		session:  session,
		stdin:    stdin,
		stdout:   stdout,
		identity: transport.Identity{Kind: "ssh-host", Key: addr},
	}, nil
}

// Open resolves prof's authentication and host-key verification, then
// opens a persistent SSH PTY/shell stream. auth.AcceptHost combined
// with a valid auth.Token is the only way an unknown host is ever
// accepted; a changed or revoked key always fails regardless of auth.
func Open(ctx context.Context, prof *profile.SSHConfig, verifier *HostKeyVerifier, factory *AuthFactory, auth HumanAuth) (*Stream, error) {
	methods, err := factory.Build(ctx, prof, auth.Secrets)
	if err != nil {
		return nil, err
	}
	sshMethods := make([]gossh.AuthMethod, len(methods))
	for i, m := range methods {
		sshMethods[i] = m.Method
	}

	config := &gossh.ClientConfig{
		User: prof.User,
		Auth: sshMethods,
		HostKeyCallback: func(hostname string, remote net.Addr, key gossh.PublicKey) error {
			err := verifier.Check(hostname, remote, key)
			if errors.Is(err, ErrUnknownHostRequiresHuman) && auth.AcceptHost {
				return verifier.AcceptNewHuman(auth.Token, hostname, key)
			}
			return err
		},
	}

	addr := fmt.Sprintf("%s:%d", prof.Host, prof.Port)
	return dialStream(addr, config)
}

func (s *Stream) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *Stream) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Resize propagates a terminal resize to the remote PTY.
func (s *Stream) Resize(width, height int) error {
	return s.session.WindowChange(height, width)
}

func (s *Stream) Close() error {
	s.closeOnce.Do(func() {
		s.session.Close()
		s.closeErr = s.client.Close()
	})
	return s.closeErr
}

func (s *Stream) Identity() transport.Identity { return s.identity }
func (s *Stream) Kind() string                 { return "ssh" }
