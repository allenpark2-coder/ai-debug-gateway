// Package id generates opaque, unique identifiers for sessions,
// proposals, and transactions.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New returns a unique identifier of the form "prefix-<16 hex chars>".
func New(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing indicates a broken host RNG; there is no
		// safe way to keep issuing identifiers.
		panic(fmt.Sprintf("id: crypto/rand unavailable: %v", err))
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
