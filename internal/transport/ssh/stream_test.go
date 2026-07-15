package ssh

import (
	"bufio"
	"errors"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

var errUnauthorized = errors.New("unauthorized")

func testServerAuth(password string) func(*gossh.ServerConfig) {
	return func(c *gossh.ServerConfig) {
		c.PasswordCallback = func(conn gossh.ConnMetadata, pass []byte) (*gossh.Permissions, error) {
			if string(pass) == password {
				return nil, nil
			}
			return nil, errUnauthorized
		}
	}
}

func openTestStream(t *testing.T, srv *testServer, password string) *Stream {
	t.Helper()
	config := &gossh.ClientConfig{
		User:            "root",
		Auth:            []gossh.AuthMethod{gossh.Password(password)},
		HostKeyCallback: gossh.FixedHostKey(srv.HostKey.PublicKey()),
		Timeout:         5 * time.Second,
	}
	stream, err := dialStream(srv.Addr, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stream.Close() })
	return stream
}

func TestPersistentPTYSessionReceivesOneShellRequest(t *testing.T) {
	srv := startTestServer(t, testServerAuth("secret"))
	_ = openTestStream(t, srv, "secret")

	waitForTest(t, time.Second, func() bool { return srv.SessionOpen() })
}

func TestPersistentPTYShellStateSurvivesAcrossCommands(t *testing.T) {
	srv := startTestServer(t, testServerAuth("secret"))
	stream := openTestStream(t, srv, "secret")

	if _, err := stream.Write([]byte("cd /tmp\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("pwd\n")); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(stream)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(line, "\r\n") != "/tmp" {
		t.Fatalf("got %q, want /tmp (shell state must persist across commands on one channel)", line)
	}
}

func TestPersistentPTYResizePropagates(t *testing.T) {
	srv := startTestServer(t, testServerAuth("secret"))
	stream := openTestStream(t, srv, "secret")

	if err := stream.Resize(132, 43); err != nil {
		t.Fatal(err)
	}

	waitForTest(t, time.Second, func() bool { return len(srv.Resizes()) == 1 })
	got := srv.Resizes()[0]
	if got.Width != 132 || got.Height != 43 {
		t.Fatalf("got %+v, want {132 43}", got)
	}
}

func TestPersistentPTYCancellationCloses(t *testing.T) {
	srv := startTestServer(t, testServerAuth("secret"))
	stream := openTestStream(t, srv, "secret")

	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 16)
	if _, err := stream.Read(buf); err == nil {
		t.Fatal("expected reading a closed stream to fail")
	}
}

func waitForTest(t *testing.T, timeout time.Duration, cond func() bool) {
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
