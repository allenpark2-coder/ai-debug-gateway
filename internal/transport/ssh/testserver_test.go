package ssh

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testServer is a minimal local SSH server for tests: one session
// channel per connection, handling pty-req/shell/window-change, and a
// tiny persistent shell-like interpreter (cd/pwd, echoing anything
// else) so tests can assert that shell state survives across commands
// on the same channel.
type testServer struct {
	Addr    string
	HostKey ssh.Signer

	listener net.Listener

	mu          sync.Mutex
	commands    []string
	resizes     []resizeEvent
	sessionOpen bool
}

type resizeEvent struct{ Width, Height int }

func startTestServer(t *testing.T, configure func(*ssh.ServerConfig)) *testServer {
	t.Helper()
	hostKey := newTestSigner(t)

	config := &ssh.ServerConfig{}
	config.AddHostKey(hostKey)
	if configure != nil {
		configure(config)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &testServer{Addr: l.Addr().String(), HostKey: hostKey, listener: l}

	go srv.acceptLoop(config)
	t.Cleanup(func() { l.Close() })
	return srv
}

func (s *testServer) acceptLoop(config *ssh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn, config)
	}
}

func (s *testServer) handleConn(conn net.Conn, config *ssh.ServerConfig) {
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
		s.mu.Lock()
		s.sessionOpen = true
		s.mu.Unlock()
		go s.serveSession(ch, requests)
	}
}

func (s *testServer) serveSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	cwd := "/root"

	for req := range requests {
		switch req.Type {
		case "pty-req":
			req.Reply(true, nil)
		case "shell":
			req.Reply(true, nil)
			go s.runShell(ch, &cwd)
		case "window-change":
			// SSH_MSG_CHANNEL_REQUEST window-change payload: uint32
			// width, height, pixwidth, pixheight (RFC 4254 6.7).
			if len(req.Payload) >= 8 {
				width := int(req.Payload[3]) | int(req.Payload[2])<<8 | int(req.Payload[1])<<16 | int(req.Payload[0])<<24
				height := int(req.Payload[7]) | int(req.Payload[6])<<8 | int(req.Payload[5])<<16 | int(req.Payload[4])<<24
				s.mu.Lock()
				s.resizes = append(s.resizes, resizeEvent{Width: width, Height: height})
				s.mu.Unlock()
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (s *testServer) runShell(ch ssh.Channel, cwd *string) {
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

func (s *testServer) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *testServer) Resizes() []resizeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]resizeEvent(nil), s.resizes...)
}

func (s *testServer) SessionOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionOpen
}

func newTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	priv, pub := generateKeyPEM(t, nil)
	_ = pub
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
