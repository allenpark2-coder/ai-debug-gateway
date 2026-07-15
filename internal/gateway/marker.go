package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
)

// marker is the shell-line fragment appended after an immutable
// command snapshot so completion can be detected without relying only
// on prompt text, and an exit code extracted without ever fabricating
// one for a command that never emits it.
type marker struct {
	transactionID string
	nonce         string // 128 bits, hex-encoded
}

// newMarker constructs a marker scoped to one transaction with a fresh
// random nonce.
func newMarker(transactionID string) marker {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("gateway: crypto/rand unavailable: %v", err))
	}
	return marker{transactionID: transactionID, nonce: hex.EncodeToString(b[:])}
}

// shellSuffix is appended to the transaction's command text on the
// same physical shell line, so the marker reports the command's own
// exit status rather than a following command's.
func (m marker) shellSuffix() string {
	return fmt.Sprintf("; printf '\\nGWMARK:%s:%s:%%d\\n' \"$?\"", m.transactionID, m.nonce)
}

var markerLine = regexp.MustCompile(`GWMARK:([^:\s]+):([0-9a-f]{32}):(-?[0-9]+)`)

// find scans buf for a completion marker matching exactly this
// transaction and nonce, returning the shell exit code it carried. A
// marker for any other transaction or nonce is ignored.
func (m marker) find(buf []byte) (exitCode int, found bool) {
	for _, match := range markerLine.FindAllSubmatch(buf, -1) {
		if string(match[1]) != m.transactionID || string(match[2]) != m.nonce {
			continue
		}
		var code int
		if _, err := fmt.Sscanf(string(match[3]), "%d", &code); err != nil {
			continue
		}
		return code, true
	}
	return 0, false
}
