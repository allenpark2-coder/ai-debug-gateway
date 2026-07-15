package id

import (
	"strings"
	"testing"
)

func TestNewHasPrefixAndIsUnique(t *testing.T) {
	a := New("sess")
	b := New("sess")

	if !strings.HasPrefix(a, "sess-") {
		t.Fatalf("New(%q) = %q, want sess- prefix", "sess", a)
	}
	if a == b {
		t.Fatalf("New(%q) returned the same ID twice: %q", "sess", a)
	}
}
