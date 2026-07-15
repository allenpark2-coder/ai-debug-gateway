package cli

import "testing"

func TestOrdinaryByteForwards(t *testing.T) {
	f := NewEscapeFilter(0x1d) // Ctrl-]
	r := f.Feed('a')
	if !r.ShouldForward || r.Forward != 'a' || r.EnterCommandMode {
		t.Fatalf("%+v", r)
	}
}

func TestSinglePrefixIsConsumedLocally(t *testing.T) {
	f := NewEscapeFilter(0x1d)
	r := f.Feed(0x1d)
	if r.ShouldForward || r.EnterCommandMode {
		t.Fatalf("a lone prefix byte must neither forward nor enter command mode yet: %+v", r)
	}
}

func TestDoubledPrefixSendsOneLiteralByte(t *testing.T) {
	f := NewEscapeFilter(0x1d)
	_ = f.Feed(0x1d)
	r := f.Feed(0x1d)
	if !r.ShouldForward || r.Forward != 0x1d || r.EnterCommandMode {
		t.Fatalf("doubled prefix must forward exactly one literal escape byte: %+v", r)
	}

	// The filter must return to normal passthrough afterward.
	r2 := f.Feed('x')
	if !r2.ShouldForward || r2.Forward != 'x' || r2.EnterCommandMode {
		t.Fatalf("%+v", r2)
	}
}

func TestPrefixThenOtherByteEntersCommandModeWithoutForwarding(t *testing.T) {
	f := NewEscapeFilter(0x1d)
	_ = f.Feed(0x1d)
	r := f.Feed('a')
	if r.ShouldForward {
		t.Fatalf("the byte that enters command mode must never be forwarded to the target: %+v", r)
	}
	if !r.EnterCommandMode || r.CommandModeFirstByte != 'a' {
		t.Fatalf("%+v", r)
	}
}

func TestFilterReturnsToPassthroughAfterCommandModeEntry(t *testing.T) {
	f := NewEscapeFilter(0x1d)
	_ = f.Feed(0x1d)
	_ = f.Feed('a') // enters command mode
	r := f.Feed('b')
	if !r.ShouldForward || r.Forward != 'b' {
		t.Fatalf("a filter is stateless once command mode is entered; byte-forwarding is the caller's job during that mode: %+v", r)
	}
}
