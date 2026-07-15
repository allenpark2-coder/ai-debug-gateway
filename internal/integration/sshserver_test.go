package integration

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshTestServer is a controlled local SSH server for end-to-end tests:
// ephemeral host key (swappable, for a changed-host-key scenario),
// password and public-key auth, a persistent PTY/shell session per
// connection with a tiny command interpreter, and a disconnect hook.
type sshTestServer struct {
	Addr    string
	HostKey ssh.Signer

	mu             sync.Mutex
	password       string
	authorizedKeys []ssh.PublicKey
	commands       []string
	conns          []net.Conn
	listener       net.Listener
	managedDelay   time.Duration
}

func (s *sshTestServer) SetManagedDelay(delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.managedDelay = delay
}

func newSSHTestServer(t *testing.T) *sshTestServer {
	t.Helper()
	s := &sshTestServer{HostKey: generateSSHHostKey(t)}
	s.listen(t)
	return s
}

func (s *sshTestServer) listen(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Addr = l.Addr().String()
	s.listener = l
	go s.acceptLoop()
	t.Cleanup(func() { l.Close() })
}

func generateSSHHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func (s *sshTestServer) SetPassword(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.password = p
}

func (s *sshTestServer) AddAuthorizedKey(k ssh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorizedKeys = append(s.authorizedKeys, k)
}

func (s *sshTestServer) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// DisconnectAll forcibly closes every accepted connection, simulating
// a network loss.
func (s *sshTestServer) DisconnectAll() {
	s.mu.Lock()
	conns := append([]net.Conn(nil), s.conns...)
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (s *sshTestServer) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			s.mu.Lock()
			want := s.password
			s.mu.Unlock()
			if want != "" && string(pass) == want {
				return nil, nil
			}
			return nil, fmt.Errorf("password rejected")
		},
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, k := range s.authorizedKeys {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return nil, nil
				}
			}
			return nil, fmt.Errorf("public key rejected")
		},
	}
	cfg.AddHostKey(s.HostKey)
	return cfg
}

func (s *sshTestServer) acceptLoop() {
	config := s.config()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		go s.handleConn(conn, config)
	}
}

func (s *sshTestServer) handleConn(conn net.Conn, config *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			return
		}
		go s.serveSession(ch, requests)
	}
}

func (s *sshTestServer) serveSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	cwd := "/root"
	for req := range requests {
		switch req.Type {
		case "pty-req":
			req.Reply(true, nil)
		case "shell":
			req.Reply(true, nil)
			go s.runShell(ch, &cwd)
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (s *sshTestServer) runShell(ch ssh.Channel, cwd *string) {
	reader := bufio.NewReader(ch)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.mu.Lock()
		s.commands = append(s.commands, line)
		s.mu.Unlock()

		// A Coordinator-approved transaction appends
		// "; printf '\nGWMARK:<txn>:<nonce>:%d\n' \"$?\"" on the same
		// line as the command text; respond with the command's own
		// output plus the marker, like a real shell would.
		if idx := strings.Index(line, "; printf"); idx >= 0 {
			cmdText := line[:idx]
			m := markerLineRE.FindStringSubmatch(line[idx:])
			if m != nil {
				s.respondToManagedCommand(ch, cwd, cmdText, m[1], m[2])
				continue
			}
		}

		switch {
		case line == "pwd":
			fmt.Fprintf(ch, "%s\n", *cwd)
		case strings.HasPrefix(line, "cd "):
			*cwd = strings.TrimPrefix(line, "cd ")
		default:
			fmt.Fprintf(ch, "echo:%s\n", line)
		}
	}
}

func (s *sshTestServer) respondToManagedCommand(ch ssh.Channel, cwd *string, cmdText, txn, nonce string) {
	s.mu.Lock()
	delay := s.managedDelay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	switch {
	case cmdText == "pwd":
		fmt.Fprintf(ch, "%s\r\nGWMARK:%s:%s:0\r\n", *cwd, txn, nonce)
	case cmdText == "false":
		fmt.Fprintf(ch, "GWMARK:%s:%s:1\r\n", txn, nonce)
	case strings.HasPrefix(cmdText, "sleep "):
		// Actually sleeps, unlike every other fake response here: tests
		// that need a transaction to still be genuinely RUNNING (not
		// yet completed) when they act need real wall-clock delay, not
		// an instantly-answered stand-in.
		if secs, err := strconv.ParseFloat(strings.TrimPrefix(cmdText, "sleep "), 64); err == nil {
			time.Sleep(time.Duration(secs * float64(time.Second)))
		}
		fmt.Fprintf(ch, "GWMARK:%s:%s:0\r\n", txn, nonce)
	default:
		if strings.HasPrefix(cmdText, "cd ") {
			*cwd = strings.TrimPrefix(cmdText, "cd ")
		}
		fmt.Fprintf(ch, "GWMARK:%s:%s:0\r\n", txn, nonce)
	}
}
