// Package session implements the gateway's platform-independent
// session state machine. It must not refer to /dev, Unix sockets,
// POSIX terminal APIs, COM ports, or Windows Named Pipes; transport-
// specific behavior stays in the transport packages and is reported
// here only as events.
package session

import (
	"fmt"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/id"
)

// State is one of the gateway's explicit session states.
type State string

// Event drives a state transition.
type Event string

const (
	Disconnected   State = "DISCONNECTED"
	Connecting     State = "CONNECTING"
	Authenticating State = "AUTHENTICATING"
	Ready          State = "READY"
	RunningCommand State = "RUNNING_COMMAND"
	Reconnecting   State = "RECONNECTING"
	Error          State = "ERROR"
)

const (
	// Connect is a human request to start a session from DISCONNECTED.
	// It rotates the session ID: every genuinely new session, whether
	// the first one ever or one started after an explicit end (for
	// example to switch transport), gets a fresh identifier.
	Connect Event = "CONNECT"
	// TransportReady reports that the transport is open and readable.
	TransportReady Event = "TRANSPORT_READY"
	// Authenticated reports that login/handshake completed.
	Authenticated Event = "AUTHENTICATED"
	// CommandStart reports that an approved transaction began executing.
	CommandStart Event = "COMMAND_START"
	// CommandResult reports an ordinary transaction completion
	// (completed, timeout, interrupted-by-user, or protocol-error).
	CommandResult Event = "COMMAND_RESULT"
	// TargetRebooted reports a recognized target reboot that leaves the
	// host transport itself usable; the session ID is preserved.
	TargetRebooted Event = "TARGET_REBOOTED"
	// TransportLost reports loss of the host transport itself (USB
	// removal, hangup, ENODEV/EIO, network loss).
	TransportLost Event = "TRANSPORT_LOST"
	// HumanRetry is a human-approved reconnect attempt; it rotates the
	// session ID.
	HumanRetry Event = "HUMAN_RETRY"
	// RetriesExhausted reports that bounded reconnect retries failed.
	RetriesExhausted Event = "RETRIES_EXHAUSTED"
	// Shutdown is a human-requested shutdown, valid from any active state.
	Shutdown Event = "SHUTDOWN"
)

// ErrInvalidTransition reports an event the current state does not accept.
type ErrInvalidTransition struct {
	From  State
	Event Event
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("session: event %s invalid in state %s", e.Event, e.From)
}

type transition struct {
	to        State
	rotatesID bool
}

var table = map[State]map[Event]transition{
	Disconnected: {
		Connect: {to: Connecting, rotatesID: true},
	},
	Connecting: {
		TransportReady: {to: Authenticating},
		TransportLost:  {to: Reconnecting},
		Shutdown:       {to: Disconnected},
	},
	Authenticating: {
		Authenticated: {to: Ready},
		TransportLost: {to: Reconnecting},
		Shutdown:      {to: Disconnected},
	},
	Ready: {
		CommandStart:   {to: RunningCommand},
		TargetRebooted: {to: Authenticating},
		TransportLost:  {to: Reconnecting},
		Shutdown:       {to: Disconnected},
	},
	RunningCommand: {
		CommandResult:  {to: Ready},
		TargetRebooted: {to: Authenticating},
		TransportLost:  {to: Reconnecting},
		Shutdown:       {to: Disconnected},
	},
	Reconnecting: {
		HumanRetry:       {to: Connecting, rotatesID: true},
		RetriesExhausted: {to: Error},
		Shutdown:         {to: Disconnected},
	},
	Error: {
		Shutdown: {to: Disconnected},
	},
}

// Machine is a single board session's state machine. It is not safe
// for concurrent use; callers serialize Apply through one coordinator
// goroutine per session.
type Machine struct {
	state     State
	sessionID string
}

// NewMachine starts a machine in DISCONNECTED with the given session ID.
func NewMachine(sessionID string) *Machine {
	return &Machine{state: Disconnected, sessionID: sessionID}
}

// Apply advances the machine on e, or returns *ErrInvalidTransition
// without changing state.
func (m *Machine) Apply(e Event) error {
	next, ok := table[m.state][e]
	if !ok {
		return &ErrInvalidTransition{From: m.state, Event: e}
	}
	m.state = next.to
	if next.rotatesID {
		m.sessionID = id.New("sess")
	}
	return nil
}

// State returns the current state.
func (m *Machine) State() State { return m.state }

// SessionID returns the current session identifier. It changes only
// when Apply performs a rotating transition (HumanRetry).
func (m *Machine) SessionID() string { return m.sessionID }
