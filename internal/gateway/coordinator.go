package gateway

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/command"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/id"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/secret"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/session"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/transcript"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

var (
	ErrTransportActive        = errors.New("gateway: a transport is already active for this board")
	ErrNotConnected           = errors.New("gateway: no transport is connected")
	ErrNotReady               = errors.New("gateway: session is not READY")
	ErrNotReconnecting        = errors.New("gateway: retry is only valid while RECONNECTING")
	ErrHumanSelectionRequired = errors.New("gateway: automatic reconnect requires human device selection")
)

const timeoutPollInterval = 25 * time.Millisecond

// Opener attempts to reopen the same physical device a session
// started with. It must verify the device's identity and return
// ErrHumanSelectionRequired if that identity cannot be safely resolved
// (absent, changed, or ambiguous), rather than guessing.
type Opener func() (transport.Stream, error)

// LoginConfig configures UART boot/login/prompt recognition. Every
// pattern is profile-configurable; a nil pattern never matches.
type LoginConfig struct {
	Username string

	LoginPromptPattern    *regexp.Regexp
	PasswordPromptPattern *regexp.Regexp
	ShellPromptPattern    *regexp.Regexp
	BootBannerPattern     *regexp.Regexp
	// SecretPromptPatterns matches ad hoc secret prompts appearing
	// mid-session (sudo, passwd, a nested ssh), in addition to
	// PasswordPromptPattern.
	SecretPromptPatterns []*regexp.Regexp
	// SecretGracePeriod is how much longer an executing transaction is
	// allowed once a secret prompt pauses it.
	SecretGracePeriod time.Duration
}

func (cfg LoginConfig) secretPromptMatches(buf []byte) bool {
	if cfg.PasswordPromptPattern != nil && cfg.PasswordPromptPattern.Match(buf) {
		return true
	}
	for _, p := range cfg.SecretPromptPatterns {
		if p != nil && p.Match(buf) {
			return true
		}
	}
	return false
}

// activeTransaction is the one transaction currently executing, if
// any.
type activeTransaction struct {
	tx       *command.Transaction
	marker   marker
	deadline time.Time
	// startSeq is the ring sequence at which this transaction's own
	// output begins, so marker matching never sees an earlier
	// transaction's output.
	startSeq uint64
}

// Coordinator wires one board's session, command, transcript, and
// secret state to a transport.Stream. It serializes every state
// mutation behind its own mutex: the transport read loop appends
// bytes to the bounded ring and fans them out to subscribers without
// ever waiting on that mutex being free for long, since every holder
// only does small, in-memory work.
type Coordinator struct {
	mu sync.Mutex

	board    string
	sess     *session.Machine
	commands *command.Store
	ring     *transcript.Ring
	secretW  *secret.Window

	stream       transport.Stream
	loginCfg     LoginConfig
	opener       Opener
	usernameSent bool
	manualReady  bool

	humanSubs []*subscriber
	aiSubs    []*subscriber

	act *activeTransaction

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// NewCoordinator constructs a Coordinator for one board, starting in
// DISCONNECTED with a fresh session ID.
func NewCoordinator(board string) *Coordinator {
	c := &Coordinator{
		board:    board,
		sess:     session.NewMachine(id.New("sess")),
		commands: command.NewStore(),
		ring:     transcript.NewRing(1 << 20),
		secretW:  secret.NewWindow(),
		stopCh:   make(chan struct{}),
	}
	c.wg.Add(1)
	go c.timeoutLoop()
	return c
}

// Stop closes any active transport and stops background goroutines.
func (c *Coordinator) Stop() {
	c.mu.Lock()
	if c.stream != nil {
		c.stream.Close()
	}
	c.mu.Unlock()
	c.stopOnce.Do(func() { close(c.stopCh) })
	c.wg.Wait()
}

// StartUART begins a session on an already-opened UART stream. opener
// is used by a later RetryUART to safely reopen the same physical
// device; it may be nil if the caller does not support retry.
func (c *Coordinator) StartUART(stream transport.Stream, cfg LoginConfig, opener Opener) error {
	c.mu.Lock()
	if c.stream != nil {
		c.mu.Unlock()
		return ErrTransportActive
	}
	c.stream = stream
	c.loginCfg = cfg
	c.opener = opener
	c.usernameSent = false
	c.manualReady = false
	_ = c.sess.Apply(session.Connect)
	_ = c.sess.Apply(session.TransportReady)
	c.mu.Unlock()

	c.wg.Add(1)
	go c.readLoop(stream)
	return nil
}

// RetryUART is the human-approved retry for a RECONNECTING UART
// session. It requires the Opener supplied to StartUART to resolve the
// device's identity; a nil Opener or one that cannot safely resolve
// the identity yields ErrHumanSelectionRequired, and the session ID is
// left unchanged.
func (c *Coordinator) RetryUART() error {
	c.mu.Lock()
	if c.sess.State() != session.Reconnecting {
		c.mu.Unlock()
		return ErrNotReconnecting
	}
	opener := c.opener
	c.mu.Unlock()

	if opener == nil {
		return ErrHumanSelectionRequired
	}

	newStream, err := opener()
	if err != nil {
		return err
	}

	c.mu.Lock()
	if err := c.sess.Apply(session.HumanRetry); err != nil {
		c.mu.Unlock()
		newStream.Close()
		return err
	}
	c.stream = newStream
	c.usernameSent = false
	c.manualReady = false
	_ = c.sess.Apply(session.TransportReady)
	c.mu.Unlock()

	c.wg.Add(1)
	go c.readLoop(newStream)
	return nil
}

// Propose creates a new pending proposal.
func (c *Coordinator) Propose(sessionID, text, purpose string, timeout time.Duration) (*command.Proposal, error) {
	return c.commands.Propose(command.Input{
		SessionID: sessionID,
		Transport: "uart",
		Board:     c.board,
		Text:      text,
		Purpose:   purpose,
		Timeout:   timeout,
	})
}

// Approve snapshots proposalID into a transaction, appends a
// completion marker to its command text on one shell line, and writes
// it to the transport. The session must be READY.
func (c *Coordinator) Approve(proposalID string) (*command.Transaction, error) {
	tx, err := c.commands.Approve(proposalID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stream == nil {
		return nil, ErrNotConnected
	}
	if c.sess.State() != session.Ready {
		return nil, ErrNotReady
	}

	m := newMarker(tx.ID)
	c.act = &activeTransaction{
		tx:       tx,
		marker:   m,
		deadline: time.Now().Add(tx.Timeout),
		startSeq: c.ring.Len(),
	}
	_ = c.sess.Apply(session.CommandStart)

	line := tx.Text + m.shellSuffix() + "\n"
	if _, werr := c.stream.Write([]byte(line)); werr != nil {
		return tx, werr
	}
	return tx, nil
}

// ConfirmSessionReady is the human's local override when an
// AUTHENTICATING prompt was not recognized by any configured pattern:
// it manually confirms the session is at a working shell.
func (c *Coordinator) ConfirmSessionReady() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.State() != session.Authenticating {
		return fmt.Errorf("gateway: cannot confirm ready from state %s", c.sess.State())
	}
	c.manualReady = true
	return c.sess.Apply(session.Authenticated)
}

// WriteHuman forwards raw human keystrokes straight to the transport.
// It never touches the subscriber fan-out, so a stalled AI subscriber
// can never delay it.
func (c *Coordinator) WriteHuman(data []byte) (int, error) {
	c.mu.Lock()
	stream := c.stream
	c.mu.Unlock()
	if stream == nil {
		return 0, ErrNotConnected
	}
	return stream.Write(data)
}

// SubscribeHuman registers a new live subscriber for the attached
// human terminal.
func (c *Coordinator) SubscribeHuman() *subscriber {
	s := newSubscriber()
	c.mu.Lock()
	c.humanSubs = append(c.humanSubs, s)
	c.mu.Unlock()
	return s
}

// subscribeAI registers a new live subscriber backing an AI client's
// output.read long-poll. A stalled AI subscriber only drops its own
// events; it never affects human output or input.
func (c *Coordinator) subscribeAI() *subscriber {
	s := newSubscriber()
	c.mu.Lock()
	c.aiSubs = append(c.aiSubs, s)
	c.mu.Unlock()
	return s
}

// ReadAfter returns bounded transcript context after a sequence
// number, for AI polling reads.
func (c *Coordinator) ReadAfter(after uint64, max int) transcript.Chunk {
	return c.ring.ReadAfter(after, max)
}

// AIEnabled reports whether an approved AI transaction could execute
// right now: the session must be READY or RUNNING_COMMAND and no
// secret window may be open.
func (c *Coordinator) AIEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secretW.Active() {
		return false
	}
	switch c.sess.State() {
	case session.Ready, session.RunningCommand:
		return true
	default:
		return false
	}
}

// SecretActive reports whether the secret redaction window is open.
func (c *Coordinator) SecretActive() bool { return c.secretW.Active() }

// State returns the current session state.
func (c *Coordinator) State() session.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess.State()
}

// SessionID returns the current session identifier.
func (c *Coordinator) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess.SessionID()
}

func (c *Coordinator) active() *activeTransaction {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.act
}

func (c *Coordinator) activeMarker() marker {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.act == nil {
		return marker{}
	}
	return c.act.marker
}

// readLoop is the transport's dedicated reader goroutine. It only
// appends to the bounded ring, fans bytes out to subscribers, and
// hands the chunk to onData for prompt/marker matching: all bounded,
// in-memory work, so it never waits on AI processing, approval, a slow
// client, or durable log I/O.
func (c *Coordinator) readLoop(stream transport.Stream) {
	defer c.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			c.ring.Append(chunk)
			c.publish(chunk)
			c.onData(chunk)
		}
		if err != nil {
			c.onReadError(stream, err)
			return
		}
	}
}

func (c *Coordinator) publish(data []byte) {
	c.mu.Lock()
	subs := make([]*subscriber, 0, len(c.humanSubs)+len(c.aiSubs))
	subs = append(subs, c.humanSubs...)
	subs = append(subs, c.aiSubs...)
	c.mu.Unlock()

	for _, s := range subs {
		s.publish(Event{Data: data})
	}
}

func (c *Coordinator) onData(chunk []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg := c.loginCfg
	state := c.sess.State()

	if cfg.BootBannerPattern != nil && cfg.BootBannerPattern.Match(chunk) &&
		(state == session.Ready || state == session.RunningCommand) {
		c.handleTargetRebootLocked()
		return
	}

	if c.secretW.Active() {
		// Only a recognized post-authentication prompt, or the
		// human's local secret-done operation (not modeled as a
		// distinct method here), ends the window; a timeout must
		// never silently end it.
		if cfg.ShellPromptPattern != nil && cfg.ShellPromptPattern.Match(chunk) {
			c.secretW.Finish()
			if state == session.Authenticating {
				_ = c.sess.Apply(session.Authenticated)
			}
		}
		return
	}

	if cfg.secretPromptMatches(chunk) {
		c.secretW.Begin()
		if c.act != nil {
			c.act.deadline = time.Now().Add(cfg.SecretGracePeriod)
		}
		return
	}

	switch state {
	case session.Authenticating:
		c.handleAuthenticatingLocked(chunk)
	case session.RunningCommand:
		c.handleMarkerLocked(chunk)
	}
}

func (c *Coordinator) handleAuthenticatingLocked(chunk []byte) {
	cfg := c.loginCfg
	if cfg.ShellPromptPattern != nil && cfg.ShellPromptPattern.Match(chunk) {
		_ = c.sess.Apply(session.Authenticated)
		return
	}
	if !c.usernameSent && cfg.LoginPromptPattern != nil && cfg.LoginPromptPattern.Match(chunk) {
		if c.stream != nil {
			_, _ = c.stream.Write([]byte(cfg.Username + "\n"))
		}
		c.usernameSent = true
	}
}

func (c *Coordinator) handleMarkerLocked(chunk []byte) {
	if c.act == nil {
		return
	}
	tail := c.ring.ReadAfter(c.act.startSeq, 1<<16)
	code, found := c.act.marker.find(tail.Data)
	if !found {
		return
	}
	c.finishActiveLocked(command.StatusCompleted, &code)
	_ = c.sess.Apply(session.CommandResult)
}

func (c *Coordinator) handleTargetRebootLocked() {
	if c.act != nil {
		c.finishActiveLocked(command.StatusTargetRebooted, nil)
	}
	c.commands.InvalidateSession(c.sess.SessionID())
	_ = c.sess.Apply(session.TargetRebooted)
	c.usernameSent = false
	c.manualReady = false
	if c.secretW.Active() {
		c.secretW.Finish()
	}
}

// finishActiveLocked records the terminal result for the current
// active transaction, if any, and clears it. It never changes session
// state itself; callers apply whichever session event fits their
// caller (CommandResult, TargetRebooted, or TransportLost).
func (c *Coordinator) finishActiveLocked(status command.Status, exitCode *int) {
	if c.act == nil {
		return
	}
	res := command.Result{
		TransactionID: c.act.tx.ID,
		Status:        status,
		ExitCode:      exitCode,
		Duration:      time.Since(c.act.tx.ApprovedAt),
		CompletedAt:   time.Now(),
	}
	_ = c.commands.CompleteTransaction(res)
	c.act = nil
}

// onReadError handles loss of the transport itself (EOF, hangup,
// ENODEV, persistent EIO): it finalizes any active transaction as
// disconnected, invalidates pending proposals, and enters
// RECONNECTING. A recognized target reboot never reaches this path,
// since the transport stays open and readable across one; see
// handleTargetRebootLocked.
func (c *Coordinator) onReadError(stream transport.Stream, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stream != stream {
		return // a stale reader from an already-replaced transport
	}

	if c.act != nil {
		c.finishActiveLocked(command.StatusDisconnected, nil)
	}
	c.commands.InvalidateSession(c.sess.SessionID())
	_ = c.sess.Apply(session.TransportLost)
	stream.Close()
	c.stream = nil
	c.usernameSent = false
	c.manualReady = false
}

func (c *Coordinator) timeoutLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(timeoutPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.checkTimeout()
		}
	}
}

// checkTimeout finalizes the active transaction once its deadline
// passes. A secret prompt mid-transaction shortens the deadline to the
// configured grace period (see onData) rather than suspending it
// indefinitely; either way, expiry never touches the secret window, so
// a timeout can never silently end redaction or restore AI capability.
func (c *Coordinator) checkTimeout() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.act == nil || time.Now().Before(c.act.deadline) {
		return
	}
	c.finishActiveLocked(command.StatusTimeout, nil)
	if c.sess.State() == session.RunningCommand {
		_ = c.sess.Apply(session.CommandResult)
	}
}
