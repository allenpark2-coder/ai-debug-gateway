// Package transport defines the common byte-stream and lifecycle
// interface every transport (serial, ssh) implements. It must not
// refer to /dev, Unix sockets, POSIX terminal APIs, COM ports, or
// Windows Named Pipes: those stay behind transport-specific packages
// so a later Windows implementation does not require rewriting session
// or approval logic.
package transport

import "io"

// Identity is a transport-independent way to recognize "the same
// physical device or host" across a reconnect. The zero value means
// no stable identity is known.
type Identity struct {
	// Kind identifies the identity scheme, e.g. "usb-serial-by-id",
	// "usb-serial-number", or "ssh-host-key".
	Kind string
	// Key is the stable identifier within Kind.
	Key string
}

// Known reports whether the identity carries a usable value.
func (id Identity) Known() bool {
	return id.Kind != "" && id.Key != ""
}

// Stream is the common byte-stream and lifecycle interface every
// transport implements.
type Stream interface {
	io.Reader
	io.Writer
	io.Closer
	Identity() Identity
	Kind() string
}
