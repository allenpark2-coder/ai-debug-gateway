// Package cli implements the attach and control CLI clients: raw
// human keystroke forwarding with a local escape prefix, and thin
// machine-readable wrappers over the control connection.
package cli

import (
	"errors"
	"strings"
)

// EscapeResult is what the caller should do with one fed byte.
type EscapeResult struct {
	// Forward is a byte to send to the target verbatim, valid only
	// when ShouldForward is true.
	Forward byte
	// ShouldForward is true for ordinary passthrough bytes and for a
	// doubled escape prefix (which forwards one literal escape byte).
	ShouldForward bool
	// EnterCommandMode is true once a single (non-doubled) escape
	// prefix has been followed by another byte: that byte begins the
	// locally-consumed command line and is never forwarded to the
	// target.
	EnterCommandMode     bool
	CommandModeFirstByte byte
}

// EscapeFilter splits a stream of raw human keystrokes into bytes to
// forward to the target and a signal to enter local command mode.
// Escape commands are consumed locally and are never forwarded to the
// target; a doubled escape prefix sends one literal escape byte
// instead.
type EscapeFilter struct {
	prefix  byte
	pending bool
}

// NewEscapeFilter constructs a filter for the given escape prefix byte
// (for example 0x1d for Ctrl-]).
func NewEscapeFilter(prefix byte) *EscapeFilter {
	return &EscapeFilter{prefix: prefix}
}

// Feed processes one input byte.
func (f *EscapeFilter) Feed(b byte) EscapeResult {
	if f.pending {
		f.pending = false
		if b == f.prefix {
			return EscapeResult{Forward: f.prefix, ShouldForward: true}
		}
		return EscapeResult{EnterCommandMode: true, CommandModeFirstByte: b}
	}
	if b == f.prefix {
		f.pending = true
		return EscapeResult{}
	}
	return EscapeResult{Forward: b, ShouldForward: true}
}

// ErrEmptyCommand is returned by ParseCommand for a blank line.
var ErrEmptyCommand = errors.New("cli: empty command")

// Command is one parsed local escape command line.
type Command struct {
	Name string
	Args []string
}

// ParseCommand parses one command-mode line, such as "approve prop-1"
// or "retry uart". "retry uart" is treated as a single two-word
// command name, matching the spec's literal syntax.
func ParseCommand(line string) (Command, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return Command{}, ErrEmptyCommand
	}
	if fields[0] == "retry" && len(fields) > 1 {
		return Command{Name: "retry " + fields[1], Args: fields[2:]}, nil
	}
	return Command{Name: fields[0], Args: fields[1:]}, nil
}
