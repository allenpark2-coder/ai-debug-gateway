package ssh

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrHostKeyChanged is a hard error: a changed or revoked host key has
// no AI or human bypass. Check never returns success for one, and
// there is deliberately no accept path that could resolve it.
var ErrHostKeyChanged = errors.New("ssh: host key changed or revoked; refusing to connect")

// ErrUnknownHostRequiresHuman means address has no known_hosts entry
// yet; only AcceptNewHuman (with a HumanToken) may add one.
var ErrUnknownHostRequiresHuman = errors.New("ssh: unknown host key requires human confirmation")

// HumanToken proves a human, not an AI client, requested an operation
// that requires an interactive terminal. The zero value is not valid;
// only GrantHumanToken produces one, and only the interactive attach
// path may call it.
type HumanToken struct {
	granted bool
}

// GrantHumanToken constructs a valid HumanToken.
func GrantHumanToken() HumanToken { return HumanToken{granted: true} }

// HostKeyVerifier checks a remote host key against a known_hosts file
// and lets a human accept a new host.
type HostKeyVerifier struct {
	path     string
	callback ssh.HostKeyCallback
}

// NewHostKeyVerifier loads the known_hosts file at path, creating it
// (owner-only) if it does not exist yet.
func NewHostKeyVerifier(path string) (*HostKeyVerifier, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f, cerr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if cerr != nil {
			return nil, cerr
		}
		f.Close()
	} else if err != nil {
		return nil, err
	}

	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return &HostKeyVerifier{path: path, callback: cb}, nil
}

// Check verifies remoteKey for address. An empty KeyError.Want means
// the host is unknown (ErrUnknownHostRequiresHuman); a non-empty Want
// means a mismatch (ErrHostKeyChanged). Check never accepts a new host
// by itself.
func (v *HostKeyVerifier) Check(address string, remote net.Addr, remoteKey ssh.PublicKey) error {
	err := v.callback(address, remote, remoteKey)
	if err == nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			return ErrUnknownHostRequiresHuman
		}
		return fmt.Errorf("%w: %v", ErrHostKeyChanged, keyErr)
	}
	return err
}

// AcceptNewHuman appends address's key to the known_hosts file and
// makes it immediately known to this verifier. It requires a
// HumanToken: an AI-facing code path, which never has one, is always
// refused. There is no equivalent for a changed or revoked key.
func (v *HostKeyVerifier) AcceptNewHuman(token HumanToken, address string, remoteKey ssh.PublicKey) error {
	if !token.granted {
		return errors.New("ssh: accepting a new host key requires an interactive human terminal")
	}

	f, err := os.OpenFile(v.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// A single append-mode write of one line is atomic (POSIX
	// guarantees this for writes under PIPE_BUF), so concurrent
	// acceptances from other processes can never interleave.
	if _, err := f.WriteString(knownhosts.Line([]string{address}, remoteKey) + "\n"); err != nil {
		return err
	}

	cb, err := knownhosts.New(v.path)
	if err != nil {
		return err
	}
	v.callback = cb
	return nil
}
