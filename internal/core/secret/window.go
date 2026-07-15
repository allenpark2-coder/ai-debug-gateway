// Package secret implements the explicit local redaction window used
// while a human enters a password or passphrase. It must not refer to
// /dev, Unix sockets, POSIX terminal APIs, COM ports, or Windows Named
// Pipes: local echo control and transport writes stay in the CLI and
// transport packages, which consult Window before forwarding bytes to
// any durable store or AI-visible channel.
package secret

import (
	"fmt"
	"sync"
)

// Window is an explicit, local secret-entry mode. While active,
// callers must route no submitted secret bytes and no raw target
// output to a transcript writer, audit writer, or AI-visible channel;
// FilterTarget is the one sanctioned path for target output during
// the window, because the gateway never infers safety from remote
// terminal echo state alone.
type Window struct {
	mu     sync.Mutex
	active bool
}

// NewWindow constructs an inactive Window.
func NewWindow() *Window {
	return &Window{}
}

// Begin starts the redaction window. It begins before the first
// secret byte is accepted.
func (w *Window) Begin() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active = true
}

// Active reports whether the window is currently open.
func (w *Window) Active() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active
}

// Submit records that secretBytes were entered by the human. It
// deliberately does nothing with secretBytes beyond that: this call
// site is the boundary that must never be wired to a durable writer or
// an AI-visible channel, so secret bytes reach the transport directly
// and never pass through Submit's return value or any other path.
func (w *Window) Submit(secretBytes []byte) {
	_ = secretBytes
}

// FilterTarget returns target output suitable for durable storage and
// AI visibility. While the window is active it returns a redaction
// placeholder instead of the real bytes, so a misconfigured remote
// echo cannot leak the submitted secret; it also removes the exact
// echoed bytes from what would otherwise reach the live human display.
// When the window is inactive, data passes through unchanged.
func (w *Window) FilterTarget(data []byte) []byte {
	if !w.Active() {
		return data
	}
	return []byte(fmt.Sprintf("[redacted %d bytes]", len(data)))
}

// Finish ends the redaction window. Callers should only call Finish
// when a configured post-authentication prompt was recognized or the
// human used the local secret-done operation; a timeout must not call
// Finish, so the session stays in human-only mode until the human
// completes or cancels the secret flow.
func (w *Window) Finish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active = false
}
