package integration

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// markerLineRE extracts the transaction ID and nonce the coordinator's
// completion marker embeds after "; printf ...GWMARK:<txn>:<nonce>:".
var markerLineRE = regexp.MustCompile(`GWMARK:([^:]+):([0-9a-f]{32}):`)

// fakeConsole drives the slave side of a real PTY pair as a scripted
// fake UART target: boot banner, login/password prompts, a tiny
// command interpreter that understands only what the coordinator's
// marker convention requires, reboot, and closing the slave to
// simulate a hangup (a real USB-serial adapter disappearing produces
// the same I/O error on the master, confirmed against a real PTY
// before writing this test).
//
// fakeConsole reads the master's raw fd once, at construction, and
// uses it directly for select/read/write: calling Fd() again from a
// concurrent goroutine races with a concurrent Close on the same
// *os.File's internal bookkeeping (confirmed by go test -race).
// Closing still goes through the *os.File's own Close (not a raw
// unix.Close on the bare fd number): master's fd is registered with
// the Go runtime's netpoller as an ordinary os.File, and bypassing its
// own Close leaves that registration live, which then collides once
// the kernel reuses the same fd number for a later test's PTY
// (confirmed by a from-first-principles review after the second-test-
// onward failures below turned out not to be a resource leak or a
// timing issue in isolation). Cancellation itself comes from the
// shared atomic closed flag polled every select pass, not from
// Close's side effects on a blocked read.
type fakeConsole struct {
	t        *testing.T
	master   *os.File
	masterFd int
	slave    *os.File
	slaveFd  int

	closed      atomic.Bool // master closed (closeMaster)
	slaveClosed atomic.Bool // slave closed (hangup or closeAll)
	closeOnce   sync.Once
	slaveOnce   sync.Once
	rebootOn    atomic.Bool
}

func newFakeConsole(t *testing.T) *fakeConsole {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	// A PTY defaults to cooked/canonical mode with local echo: bytes
	// written to master (e.g. the coordinator's own username write)
	// would otherwise be echoed straight back to master's own read
	// side, which a real dumb serial line never does. Raw mode makes
	// this pair behave like a real UART: only what the script
	// explicitly writes to slave ever reaches the coordinator.
	if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
		t.Fatal(err)
	}

	fc := &fakeConsole{
		t: t, master: master, masterFd: int(master.Fd()),
		slave: slave, slaveFd: int(slave.Fd()),
	}
	t.Cleanup(fc.closeAll)
	return fc
}

func (f *fakeConsole) stream() transport.Stream {
	return &ptyStream{console: f, identity: transport.Identity{Kind: "usb-serial-by-id", Key: "/dev/serial/by-id/usb-fake"}}
}

func (f *fakeConsole) write(s string) {
	f.slave.Write([]byte(s))
}

func (f *fakeConsole) closeMaster() error {
	var err error
	f.closeOnce.Do(func() {
		f.closed.Store(true)
		err = f.master.Close()
	})
	return err
}

func (f *fakeConsole) closeSlave() error {
	var err error
	f.slaveOnce.Do(func() {
		f.slaveClosed.Store(true)
		err = f.slave.Close()
	})
	return err
}

// hangup simulates the USB-serial adapter disappearing: once every
// slave file descriptor is closed, the kernel returns an I/O error
// from the next master read, exactly like a real unplug.
func (f *fakeConsole) hangup() {
	f.closeSlave()
}

func (f *fakeConsole) closeAll() {
	f.closeMaster()
	f.closeSlave()
}

// run starts the scripted console: prints a boot banner and login
// prompt, then reacts to whatever the coordinator sends. It exits
// silently once the slave is closed (hangup) or the master side goes
// away.
//
// The read loop below uses a cancelable select-based reader (like
// ptyStream.Read on the master), not a blocking os.File.Read through
// f.slave directly: an *os.File.Close from another goroutine (hangup)
// does not complete the underlying kernel close while a Read through
// that same *os.File is blocked waiting for input that will never
// come, which silently prevented the master from ever seeing the
// hangup (confirmed empirically -- select on the master fd never
// reported readable, even seconds after the slave was "closed").
func (f *fakeConsole) run() {
	go func() {
		f.write("Booting Linux on physical CPU 0\r\n")
		f.write("myboard login: ")

		reader := bufio.NewReader(cancelableFDReader{fd: f.slaveFd, closed: &f.slaveClosed})
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			f.handleLine(line)
		}
	}()
}

func (f *fakeConsole) handleLine(line string) {
	switch {
	case line == "root":
		f.write("\r\nmyboard $ ")
	case strings.Contains(line, "GWMARK:"):
		f.handleCommandLine(line)
	}
}

func (f *fakeConsole) handleCommandLine(line string) {
	idx := strings.Index(line, "; printf")
	if idx < 0 {
		f.write("\r\nmyboard $ ")
		return
	}
	cmdText := line[:idx]
	m := markerLineRE.FindStringSubmatch(line[idx:])
	if m == nil {
		f.write("\r\nmyboard $ ")
		return
	}
	txn, nonce := m[1], m[2]
	if f.rebootOn.Load() {
		f.rebootOn.Store(false)
		// A real target follows its boot banner with a login prompt;
		// the banner alone is only a reboot *suspicion* (dmesg replays
		// the same bytes), and the prompt is what confirms it.
		f.write("\r\nBooting Linux on physical CPU 0\r\n")
		f.write("myboard login: ")
		return
	}

	switch cmdText {
	case "sudo do-a-thing":
		// Deliberately never completes: security tests use this exact
		// text to model a sudo prompt blocking a command indefinitely.
		// Auto-completing it here, like any other unrecognized command,
		// would emit "myboard $ " immediately after the test's own
		// manually-injected secret prompt -- matching ShellPromptPattern
		// and closing the secret window microseconds after it opened,
		// racing the test's poll of it.
		return
	case "reboot":
		// A real target takes real time to reboot; without some delay
		// here the whole reboot -> re-authenticate cycle completes
		// faster than a test can ever observe the transient
		// AUTHENTICATING state in between (confirmed: it does, by a
		// diagnostic run that found the session already back at READY
		// within the first 200ms poll).
		go func() {
			time.Sleep(150 * time.Millisecond)
			f.write("\r\n")
			f.write("Booting Linux on physical CPU 0\r\n")
			time.Sleep(50 * time.Millisecond)
			f.write("myboard login: ")
		}()
	case "false":
		f.write(fmt.Sprintf("\r\nGWMARK:%s:%s:1\r\nmyboard $ ", txn, nonce))
	case "pwd":
		f.write(fmt.Sprintf("\r\n/root\r\nGWMARK:%s:%s:0\r\nmyboard $ ", txn, nonce))
	default:
		f.write(fmt.Sprintf("\r\nGWMARK:%s:%s:0\r\nmyboard $ ", txn, nonce))
	}
}

// cancelableRead performs one Read via select-with-a-short-timeout,
// checking closed each pass instead of blocking indefinitely in the
// kernel: the same technique go.bug.st/serial's real Unix backend uses
// internally, needed because closing the *os.File wrapping fd from
// another goroutine does not reliably unblock (or, worse, does not
// complete the underlying kernel close at all while) a concurrent
// blocking Read through that same *os.File -- confirmed empirically
// against a real PTY on both the master and the slave side.
func cancelableRead(fd int, closed *atomic.Bool, p []byte) (int, error) {
	for {
		if closed.Load() {
			return 0, io.EOF
		}
		var rfds unix.FdSet
		rfds.Set(fd)
		tv := unix.Timeval{Sec: 0, Usec: 50000}
		n, err := unix.Select(fd+1, &rfds, nil, nil, &tv)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		rn, rerr := unix.Read(fd, p)
		if rn < 0 {
			// unix.Read follows the C read(2) convention of -1 on
			// error; io.Reader forbids a negative count (bufio panics
			// on one).
			rn = 0
		}
		return rn, rerr
	}
}

// cancelableFDReader adapts cancelableRead to io.Reader.
type cancelableFDReader struct {
	fd     int
	closed *atomic.Bool
}

func (r cancelableFDReader) Read(p []byte) (int, error) {
	return cancelableRead(r.fd, r.closed, p)
}

// ptyStream adapts fakeConsole's master fd to transport.Stream, so the
// coordinator drives it exactly as it would a real UART device.
type ptyStream struct {
	console  *fakeConsole
	identity transport.Identity
}

func (s *ptyStream) Read(p []byte) (int, error) {
	return cancelableRead(s.console.masterFd, &s.console.closed, p)
}

func (s *ptyStream) Write(p []byte) (int, error) {
	return unix.Write(s.console.masterFd, p)
}

func (s *ptyStream) Close() error { return s.console.closeMaster() }

func (s *ptyStream) Identity() transport.Identity { return s.identity }
func (s *ptyStream) Kind() string                 { return "uart" }
