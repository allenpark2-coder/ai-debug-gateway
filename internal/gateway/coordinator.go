package gateway

import (
	"bytes"
	"context"
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
	ErrCommandActive          = errors.New("gateway: a command is already active")
	ErrResultWaiterLimit      = errors.New("gateway: too many result waiters")
)

const timeoutPollInterval = 25 * time.Millisecond
const maxResultWaiterTransactions = 1024

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
	// PromptQuietPeriod is how long the line ending in
	// ShellPromptPattern must stay silent before it is believed. Read
	// chunks split lines at arbitrary byte boundaries, so a boot-log
	// line like "... 2.44) #2 SMP ..." split right after its '#' is
	// indistinguishable from a prompt until the next bytes arrive; a
	// real prompt is followed by line silence. Zero means
	// defaultPromptQuietPeriod.
	PromptQuietPeriod time.Duration
	// AuthRetryPeriod is how long authentication may stall before the
	// coordinator nudges the console with a bare newline. Prompt
	// recognition is edge-triggered on the unterminated line tail, and
	// advanceLineTailLocked discards that tail whenever a chunk carries
	// a line break -- so a kernel message printed right after "login: "
	// (a link-up notice is the common one) erases the prompt before it
	// can be matched. getty does not reprint on its own, so without a
	// nudge the session stays in AUTHENTICATING forever. Zero means
	// defaultAuthRetryPeriod.
	AuthRetryPeriod time.Duration
	// AuthRetryLimit bounds those nudges. Once spent, the session stays
	// in AUTHENTICATING: a board that is simply off must look stuck
	// rather than have the gateway type at it indefinitely. Zero means
	// defaultAuthRetryLimit.
	AuthRetryLimit int
}

// defaultPromptQuietPeriod bounds how long a shell prompt candidate
// must stay unextended before it is acted on. Within one UART burst
// the continuation of a split line arrives in well under a
// millisecond; a real prompt sits silent until someone types.
const defaultPromptQuietPeriod = 150 * time.Millisecond

func (cfg LoginConfig) promptQuietPeriod() time.Duration {
	if cfg.PromptQuietPeriod > 0 {
		return cfg.PromptQuietPeriod
	}
	return defaultPromptQuietPeriod
}

// defaultAuthRetryPeriod is long enough that a board still printing its
// boot log is left alone, short enough that a missed prompt costs one
// wait rather than the whole session.
const defaultAuthRetryPeriod = 5 * time.Second

// defaultAuthRetryLimit caps the nudges at roughly half a minute of
// trying, after which staying stuck is the honest report.
const defaultAuthRetryLimit = 5

func (cfg LoginConfig) authRetryPeriod() time.Duration {
	if cfg.AuthRetryPeriod > 0 {
		return cfg.AuthRetryPeriod
	}
	return defaultAuthRetryPeriod
}

func (cfg LoginConfig) authRetryLimit() int {
	if cfg.AuthRetryLimit > 0 {
		return cfg.AuthRetryLimit
	}
	return defaultAuthRetryLimit
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

type resultWaiter struct {
	wake chan struct{}
	refs int
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

	stream        transport.Stream
	transportKind string // "uart" or "ssh"
	loginCfg      LoginConfig
	opener        Opener
	usernameSent  bool
	manualReady   bool
	// resyncPending keeps AI disabled after a timeout until the configured
	// prompt proves that Ctrl-C returned the target to its shell.
	resyncPending bool
	// lineTail accumulates the current, still-unterminated console
	// line across read-chunk boundaries; every prompt pattern matches
	// against it, never against a raw chunk.
	lineTail []byte
	// rebootSuspect is set when a boot banner is seen: replayed boot
	// text (dmesg, a printed log) emits the same bytes as a real
	// reboot, so only a following login/password prompt confirms the
	// reboot, and a completion marker refutes it.
	rebootSuspect bool
	// promptGen invalidates a pending shell-prompt confirmation
	// whenever new bytes arrive; promptTimer holds the pending one.
	promptGen   uint64
	promptTimer *time.Timer
	// authRetryTimer nudges a stalled authentication with a bare
	// newline; authRetries counts the nudges spent, authGen
	// invalidates a pending one after the state has moved on.
	authRetryTimer *time.Timer
	authRetries    int
	authGen        uint64
	// readerDone is closed when the current transport's reader
	// goroutine fully exits, so EndSession can wait on exactly that
	// goroutine (not the coordinator's whole lifetime, which
	// timeoutLoop holds open) before returning.
	readerDone chan struct{}

	humanSubs []*subscriber
	aiSubs    []*subscriber

	act *activeTransaction
	// resultWaiters is protected by mu. Each transaction has one shared
	// notification channel, closed only after its result is stored.
	resultWaiters map[string]*resultWaiter

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// NewCoordinator constructs a Coordinator for one board, starting in
// DISCONNECTED with a fresh session ID.
func NewCoordinator(board string) *Coordinator {
	c := &Coordinator{
		board:         board,
		sess:          session.NewMachine(id.New("sess")),
		commands:      command.NewStore(),
		ring:          transcript.NewRing(1 << 20),
		secretW:       secret.NewWindow(),
		resultWaiters: make(map[string]*resultWaiter),
		stopCh:        make(chan struct{}),
	}
	c.wg.Add(1)
	go c.timeoutLoop()
	return c
}

// Stop closes any active transport and stops background goroutines.
func (c *Coordinator) Stop() {
	c.mu.Lock()
	if c.act != nil {
		c.finishActiveLocked(command.StatusDaemonRestarted, nil)
	}
	if c.stream != nil {
		c.stream.Close()
	}
	c.resetPromptStateLocked()
	c.mu.Unlock()
	c.stopOnce.Do(func() { close(c.stopCh) })
	c.wg.Wait()
}

// start begins a session on an already-opened stream, shared by
// StartUART and StartSSH: only the Opener (for a later retry), login
// config (UART-only; SSH has none), transport kind, and whether the
// transport's own handshake already completed authentication before
// the stream existed (true for SSH, false for UART, whose login
// happens over the stream itself) differ between the two transports.
func (c *Coordinator) start(stream transport.Stream, cfg LoginConfig, opener Opener, kind string, alreadyAuthenticated bool) error {
	c.mu.Lock()
	if c.stream != nil {
		c.mu.Unlock()
		return ErrTransportActive
	}
	if c.sess.State() == session.Reconnecting {
		// A prior transport loss left the session dangling in
		// RECONNECTING with no explicit EndSession or Retry* yet:
		// Connect is only valid from DISCONNECTED, so starting fresh
		// here abandons that reconnect lineage instead of silently
		// failing to reach CONNECTING at all.
		_ = c.sess.Apply(session.Shutdown)
	}
	c.stream = stream
	c.transportKind = kind
	c.loginCfg = cfg
	c.opener = opener
	c.usernameSent = false
	c.manualReady = false
	c.resyncPending = false
	c.resetPromptStateLocked()
	_ = c.sess.Apply(session.Connect)
	_ = c.sess.Apply(session.TransportReady)
	if alreadyAuthenticated {
		_ = c.sess.Apply(session.Authenticated)
	} else {
		// A console that says nothing at all -- already past its login,
		// or parked on a stale prompt -- would otherwise never reach
		// handleAuthenticatingLocked, so arm the first nudge here.
		c.armAuthRetryLocked()
	}
	done := make(chan struct{})
	c.readerDone = done
	c.mu.Unlock()

	c.wg.Add(1)
	go c.readLoop(stream, done)
	return nil
}

// StartUART begins a session on an already-opened UART stream. opener
// is used by a later RetryUART to safely reopen the same physical
// device; it may be nil if the caller does not support retry.
func (c *Coordinator) StartUART(stream transport.Stream, cfg LoginConfig, opener Opener) error {
	return c.start(stream, cfg, opener, "uart", false)
}

// StartSSH begins a session on an already-opened, already-authenticated
// SSH stream (the SSH handshake completes authentication before Open
// ever returns a stream, so there is no UART-style login prompt to
// wait for). opener is used by a later RetrySSH to reconnect.
func (c *Coordinator) StartSSH(stream transport.Stream, opener Opener) error {
	return c.start(stream, LoginConfig{}, opener, "ssh", true)
}

// StartTelnet begins a session on an already-connected telnet stream.
// telnetd spawns the board's own login on a pty, so the UART console
// login state machine applies unchanged; only the byte carrier
// differs. opener is used by a later RetryTelnet to redial.
func (c *Coordinator) StartTelnet(stream transport.Stream, cfg LoginConfig, opener Opener) error {
	return c.start(stream, cfg, opener, "telnet", false)
}

// retry is the shared human-approved-retry mechanism for the
// per-transport Retry entry points: only whether the transport's
// handshake already completed authentication before the new stream
// existed differs.
func (c *Coordinator) retry(alreadyAuthenticated bool) error {
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
	c.resyncPending = false
	c.resetPromptStateLocked()
	_ = c.sess.Apply(session.TransportReady)
	if alreadyAuthenticated {
		_ = c.sess.Apply(session.Authenticated)
	} else {
		// A console that says nothing at all -- already past its login,
		// or parked on a stale prompt -- would otherwise never reach
		// handleAuthenticatingLocked, so arm the first nudge here.
		c.armAuthRetryLocked()
	}
	done := make(chan struct{})
	c.readerDone = done
	c.mu.Unlock()

	c.wg.Add(1)
	go c.readLoop(newStream, done)
	return nil
}

// RetryUART is the human-approved retry for a RECONNECTING UART
// session. It requires the Opener supplied to StartUART to resolve the
// device's identity; a nil Opener or one that cannot safely resolve
// the identity yields ErrHumanSelectionRequired, and the session ID is
// left unchanged.
func (c *Coordinator) RetryUART() error { return c.retry(false) }

// RetryTelnet is the human-approved retry for a RECONNECTING telnet
// session. Like a UART retry the new stream is unauthenticated: the
// console login state machine runs again after the redial.
func (c *Coordinator) RetryTelnet() error { return c.retry(false) }

// RetrySSH is the human-approved retry for a RECONNECTING SSH session.
// Like RetryUART, it requires the Opener supplied to StartSSH; a
// failed retry leaves the session ID unchanged. A successful one
// rotates the session ID and always loses prior shell state (a new
// SSH connection is a new shell), since a retry is never a resume.
func (c *Coordinator) RetrySSH() error { return c.retry(true) }

// Propose creates a new pending proposal.
func (c *Coordinator) Propose(sessionID, text, purpose string, timeout time.Duration) (*command.Proposal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commands.Propose(command.Input{
		SessionID: sessionID,
		Transport: c.transportKind,
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.canStartLocked(); err != nil {
		return nil, err
	}
	tx, err := c.commands.Approve(proposalID)
	if err != nil {
		return nil, err
	}
	return c.startTransactionLocked(tx)
}

// DiagnoseStart atomically creates, approves, installs, and writes a diagnostic
// transaction. The proposal is never observable while it is pending.
func (c *Coordinator) DiagnoseStart(sessionID, text, purpose string, timeout time.Duration) (*command.Transaction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.canStartLocked(); err != nil {
		return nil, err
	}
	if sessionID != c.sess.SessionID() {
		return nil, ErrNotReady
	}
	prop, err := c.commands.Propose(command.Input{SessionID: sessionID, Transport: c.transportKind, Board: c.board, Text: text, Purpose: purpose, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	tx, err := c.commands.Approve(prop.ID)
	if err != nil {
		_ = c.commands.Reject(prop.ID)
		return nil, err
	}
	return c.startTransactionLocked(tx)
}

func (c *Coordinator) canStartLocked() error {
	if c.stream == nil {
		return ErrNotConnected
	}
	if c.sess.State() != session.Ready || c.secretW.Active() || c.resyncPending {
		return ErrNotReady
	}
	if c.act != nil {
		return ErrCommandActive
	}
	return nil
}

func (c *Coordinator) startTransactionLocked(tx *command.Transaction) (*command.Transaction, error) {

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
		c.finishActiveLocked(command.StatusDisconnected, nil)
		if c.sess.State() == session.RunningCommand {
			_ = c.sess.Apply(session.CommandResult)
		}
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

// Result returns the recorded result for transactionID, if any.
func (c *Coordinator) Result(transactionID string) (*command.Result, error) {
	return c.commands.Result(transactionID)
}

// WaitResult waits without polling until transactionID has a terminal result.
// Registration and the second result check share c.mu with terminal recording,
// preventing completion between the check and registration from being missed.
func (c *Coordinator) WaitResult(ctx context.Context, transactionID string) (*command.Result, error) {
	if result, err := c.commands.Result(transactionID); err == nil {
		return result, nil
	} else if !errors.Is(err, command.ErrNotFound) {
		return nil, err
	}
	c.mu.Lock()
	if result, err := c.commands.Result(transactionID); err == nil {
		c.mu.Unlock()
		return result, nil
	} else if !errors.Is(err, command.ErrNotFound) {
		c.mu.Unlock()
		return nil, err
	}
	waiter := c.resultWaiters[transactionID]
	if waiter == nil {
		if len(c.resultWaiters) >= maxResultWaiterTransactions {
			c.mu.Unlock()
			return nil, ErrResultWaiterLimit
		}
		waiter = &resultWaiter{wake: make(chan struct{})}
		c.resultWaiters[transactionID] = waiter
	}
	waiter.refs++
	c.mu.Unlock()

	select {
	case <-waiter.wake:
		return c.commands.Result(transactionID)
	case <-ctx.Done():
		c.mu.Lock()
		if c.resultWaiters[transactionID] == waiter {
			waiter.refs--
			if waiter.refs == 0 {
				delete(c.resultWaiters, transactionID)
			}
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Takeover immediately ends the active transaction, if any, as
// interrupted-by-user and restores normal human input. It always
// succeeds, even when nothing is executing.
func (c *Coordinator) Takeover() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.act == nil {
		return nil
	}
	c.finishActiveLocked(command.StatusInterruptedByUser, nil)
	if c.sess.State() == session.RunningCommand {
		_ = c.sess.Apply(session.CommandResult)
	}
	return nil
}

// BeginSecret manually opens the secret redaction window: the local
// `secret` command-mode operation, for a prompt no configured pattern
// recognizes.
func (c *Coordinator) BeginSecret() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.secretW.Begin()
	if c.act != nil {
		c.act.deadline = time.Now().Add(c.loginCfg.SecretGracePeriod)
	}
}

// EndSecret manually ends the secret redaction window: the local
// `secret-done` operation, used after the human inspects the live
// console and confirms authentication completed.
func (c *Coordinator) EndSecret() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.secretW.Finish()
	if c.sess.State() == session.Authenticating {
		c.stopAuthRetryLocked()
		c.authRetries = 0
		_ = c.sess.Apply(session.Authenticated)
	}
}

// Reject marks a pending proposal as rejected.
func (c *Coordinator) Reject(proposalID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commands.Reject(proposalID)
}

// Edit replaces a pending proposal with a new one carrying text and
// purpose; it does not approve or execute anything.
func (c *Coordinator) Edit(proposalID, text, purpose string) (*command.Proposal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commands.Edit(proposalID, text, purpose)
}

// PendingForSession returns every currently pending proposal for
// sessionID.
func (c *Coordinator) PendingForSession(sessionID string) []*command.Proposal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commands.PendingForSession(sessionID)
}

// EndSession closes the active transport, if any, without stopping the
// coordinator itself, and waits for that transport's reader goroutine
// to fully exit before returning -- so by the time EndSession returns,
// c.stream is guaranteed nil and a subsequent StartUART/StartSSH for
// the same board can proceed immediately, including switching
// transport. The reader goroutine's own error handling already moves
// the session to RECONNECTING and invalidates pending proposals
// exactly as a real transport loss would; EndSession additionally
// finishes that lineage off to DISCONNECTED, so the next Start*'s
// Connect (only valid from DISCONNECTED) mints a genuinely new session
// ID rather than attempting to resume this one, per "changing
// transport explicitly ends the old session ... and creates a new
// session identifier."
func (c *Coordinator) EndSession() error {
	c.mu.Lock()
	stream := c.stream
	done := c.readerDone
	c.mu.Unlock()
	if stream == nil {
		return ErrNotConnected
	}
	if err := stream.Close(); err != nil {
		return err
	}
	if done != nil {
		<-done
	}

	c.mu.Lock()
	_ = c.sess.Apply(session.Shutdown)
	c.mu.Unlock()
	return nil
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
	if c.resyncPending {
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
func (c *Coordinator) readLoop(stream transport.Stream, done chan struct{}) {
	defer c.wg.Done()
	defer close(done)
	buf := make([]byte, 4096)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			raw := append([]byte(nil), buf[:n]...)
			// Redact before this chunk ever reaches the ring or a
			// subscriber: onData below is what recognizes and opens
			// the secret window, so checking Active() here, before
			// onData runs, is what keeps an already-open window's
			// echoed bytes out of durable stores and AI-visible
			// output even for the very first chunk that arrives while
			// it is active.
			stored := raw
			c.mu.Lock()
			active := c.secretW.Active()
			c.mu.Unlock()
			if active {
				stored = c.secretW.FilterTarget(raw)
			}
			c.ring.Append(stored)
			c.publish(stored)
			c.onData(raw)
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

	// New bytes invalidate any shell prompt candidate awaiting its
	// quiet period: what looked like a prompt was a line still being
	// extended.
	c.promptGen++
	// A banner line can arrive and terminate within one chunk, so
	// content patterns scan the previous tail plus this chunk;
	// end-anchored prompt patterns then match the advanced tail.
	scan := append(append(make([]byte, 0, len(c.lineTail)+len(chunk)), c.lineTail...), chunk...)
	c.advanceLineTailLocked(chunk)

	cfg := c.loginCfg
	state := c.sess.State()

	// A boot banner alone is only a suspicion: dmesg or a printed
	// boot log replays the same bytes without any reboot. The
	// login/password prompt that follows a real reboot (with no
	// completion marker in between) is what confirms it; a marker
	// refutes it (handleMarkerLocked).
	if cfg.BootBannerPattern != nil && cfg.BootBannerPattern.Match(scan) &&
		(state == session.Ready || state == session.RunningCommand) {
		c.rebootSuspect = true
	}
	if c.rebootSuspect && state != session.Authenticating && c.authPromptAtLineEndLocked() {
		c.rebootSuspect = false
		c.handleTargetRebootLocked()
		// The prompt that confirmed the reboot still needs answering.
		c.handleAuthenticatingLocked()
		return
	}

	if c.secretW.Active() {
		// Only a recognized post-authentication prompt (believed
		// after its quiet period), or the human's local secret-done
		// operation, ends the window; a timeout must never silently
		// end it.
		c.armPromptConfirmLocked(cfg)
		return
	}

	if cfg.secretPromptMatches(c.lineTail) {
		c.secretW.Begin()
		if c.act != nil {
			c.act.deadline = time.Now().Add(cfg.SecretGracePeriod)
		}
		c.lineTail = c.lineTail[:0]
		return
	}

	switch state {
	case session.Authenticating:
		c.handleAuthenticatingLocked()
	case session.RunningCommand:
		c.handleMarkerLocked(chunk)
	}
	c.armPromptConfirmLocked(cfg)
}

// advanceLineTailLocked folds chunk into the current unterminated
// console line: bytes after the last line break start a new tail,
// otherwise the tail extends, bounded so a pathological line cannot
// grow it without limit.
func (c *Coordinator) advanceLineTailLocked(chunk []byte) {
	const maxLineTail = 1024
	if i := bytes.LastIndexAny(chunk, "\r\n"); i >= 0 {
		c.lineTail = append(c.lineTail[:0], chunk[i+1:]...)
	} else {
		c.lineTail = append(c.lineTail, chunk...)
	}
	if n := len(c.lineTail); n > maxLineTail {
		copy(c.lineTail, c.lineTail[n-maxLineTail:])
		c.lineTail = c.lineTail[:maxLineTail]
	}
}

func (c *Coordinator) authPromptAtLineEndLocked() bool {
	cfg := c.loginCfg
	if cfg.LoginPromptPattern != nil && cfg.LoginPromptPattern.Match(c.lineTail) {
		return true
	}
	return cfg.PasswordPromptPattern != nil && cfg.PasswordPromptPattern.Match(c.lineTail)
}

// armPromptConfirmLocked schedules the shell prompt currently at the
// line end to be believed after the quiet period, unless further
// bytes arrive first.
func (c *Coordinator) armPromptConfirmLocked(cfg LoginConfig) {
	if cfg.ShellPromptPattern == nil || !cfg.ShellPromptPattern.Match(c.lineTail) {
		return
	}
	gen := c.promptGen
	if c.promptTimer != nil {
		c.promptTimer.Stop()
	}
	c.promptTimer = time.AfterFunc(cfg.promptQuietPeriod(), func() { c.confirmShellPrompt(gen) })
}

// confirmShellPrompt runs once a shell prompt candidate has survived
// its quiet period with no further bytes: the line really ends at the
// prompt, so every prompt-gated state advances.
func (c *Coordinator) confirmShellPrompt(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.promptGen {
		return
	}
	cfg := c.loginCfg
	if cfg.ShellPromptPattern == nil || !cfg.ShellPromptPattern.Match(c.lineTail) {
		return
	}
	c.lineTail = c.lineTail[:0]
	c.rebootSuspect = false
	c.resyncPending = false
	if c.secretW.Active() {
		c.secretW.Finish()
	}
	if c.sess.State() == session.Authenticating {
		_ = c.sess.Apply(session.Authenticated)
	}
}

// resetPromptStateLocked clears per-transport prompt tracking and
// invalidates any pending prompt confirmation.
func (c *Coordinator) resetPromptStateLocked() {
	c.promptGen++
	if c.promptTimer != nil {
		c.promptTimer.Stop()
		c.promptTimer = nil
	}
	c.stopAuthRetryLocked()
	c.authRetries = 0
	c.lineTail = nil
	c.rebootSuspect = false
}

// stopAuthRetryLocked cancels a pending nudge and invalidates any that
// is already on its way to firing.
func (c *Coordinator) stopAuthRetryLocked() {
	c.authGen++
	if c.authRetryTimer != nil {
		c.authRetryTimer.Stop()
		c.authRetryTimer = nil
	}
}

// armAuthRetryLocked schedules a newline nudge for an authentication
// that has gone quiet. It is re-armed on every read chunk while the
// session authenticates, so the timer only fires once the console has
// actually stalled -- a board mid-boot-log keeps pushing it back.
func (c *Coordinator) armAuthRetryLocked() {
	cfg := c.loginCfg
	if c.stream == nil || c.secretW.Active() {
		return
	}
	if c.authRetries >= cfg.authRetryLimit() {
		return
	}
	c.authGen++
	gen := c.authGen
	if c.authRetryTimer != nil {
		c.authRetryTimer.Stop()
	}
	c.authRetryTimer = time.AfterFunc(cfg.authRetryPeriod(), func() { c.retryAuth(gen) })
}

// retryAuth writes a bare newline so getty reprints its login prompt,
// then clears usernameSent so the next prompt is answered again. A
// newline is the whole nudge: it cannot leak a secret, and at a stale
// "Password:" it submits an empty answer, which fails the attempt and
// returns the console to "login:" -- exactly where we want it.
func (c *Coordinator) retryAuth(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.authGen || c.sess.State() != session.Authenticating {
		return
	}
	if c.stream == nil || c.secretW.Active() {
		return
	}
	// A shell prompt sitting out its quiet period is authentication
	// about to succeed; typing into it would both wipe the tail the
	// confirmation matches against and add a stray line.
	if cfg := c.loginCfg; cfg.ShellPromptPattern != nil &&
		cfg.ShellPromptPattern.Match(c.lineTail) {
		c.armAuthRetryLocked()
		return
	}
	c.authRetries++
	_, _ = c.stream.Write([]byte("\n"))
	// The tail is deliberately left alone: it may already hold a prompt
	// that the next chunk completes, and clearing usernameSent is what
	// re-arms the answer.
	c.usernameSent = false
	c.armAuthRetryLocked()
}

func (c *Coordinator) handleAuthenticatingLocked() {
	cfg := c.loginCfg
	if !c.usernameSent && cfg.LoginPromptPattern != nil && cfg.LoginPromptPattern.Match(c.lineTail) {
		if c.stream != nil {
			_, _ = c.stream.Write([]byte(cfg.Username + "\n"))
		}
		c.usernameSent = true
		c.lineTail = c.lineTail[:0]
	}
	c.armAuthRetryLocked()
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
	// The marker proves the shell survived whatever the output
	// contained -- including a replayed boot banner.
	c.rebootSuspect = false
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
	c.resyncPending = false
	c.rebootSuspect = false
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
		TransactionID:    c.act.tx.ID,
		SourceProposalID: c.act.tx.SourceProposalID,
		Status:           status,
		ExitCode:         exitCode,
		Duration:         time.Since(c.act.tx.ApprovedAt),
		CompletedAt:      time.Now(),
	}
	chunk := c.ring.ReadAfter(c.act.startSeq, 1<<20)
	res.Output = chunk.Data
	res.OutputTruncatedStart = chunk.Gap
	_ = c.commands.CompleteTransaction(res)
	if waiter := c.resultWaiters[res.TransactionID]; waiter != nil {
		delete(c.resultWaiters, res.TransactionID)
		close(waiter.wake)
	}
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
	if c.stream != nil {
		_, _ = c.stream.Write([]byte{3})
	}
	c.finishActiveLocked(command.StatusTimeout, nil)
	if c.sess.State() == session.RunningCommand {
		_ = c.sess.Apply(session.CommandResult)
	}
	c.resyncPending = true
	// SSH and transports without prompt recognition cannot prove shell
	// resynchronization in-place. Disconnect so an operator-approved reconnect
	// creates a fresh shell instead of leaving attribution ambiguous.
	if c.loginCfg.ShellPromptPattern == nil && c.stream != nil {
		_ = c.stream.Close()
	}
}
