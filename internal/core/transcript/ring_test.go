package transcript

import "testing"

func TestRingReadAfterWraparoundReportsGap(t *testing.T) {
	r := NewRing(8)
	r.Append([]byte("123456"))
	r.Append([]byte("7890"))

	c := r.ReadAfter(0, 8)
	if !c.Gap || string(c.Data) != "34567890" {
		t.Fatalf("%+v", c)
	}
}

func TestRingReadAfterNoGapWhenCaughtUp(t *testing.T) {
	r := NewRing(8)
	r.Append([]byte("123456"))
	r.Append([]byte("7890"))

	c := r.ReadAfter(2, 8)
	if c.Gap {
		t.Fatalf("did not expect a gap: %+v", c)
	}
	if string(c.Data) != "34567890" {
		t.Fatalf("%+v", c)
	}
}

func TestRingReadAfterFutureSequenceReturnsNothingYet(t *testing.T) {
	r := NewRing(8)
	r.Append([]byte("1234"))

	c := r.ReadAfter(4, 8)
	if c.Gap {
		t.Fatalf("did not expect a gap: %+v", c)
	}
	if len(c.Data) != 0 {
		t.Fatalf("expected no data yet, got %+v", c)
	}
	if c.Next != 4 {
		t.Fatalf("got Next=%d, want 4", c.Next)
	}
}

func TestRingAppendLargerThanCapacityKeepsTail(t *testing.T) {
	r := NewRing(4)
	r.Append([]byte("0123456789"))

	c := r.ReadAfter(0, 4)
	if string(c.Data) != "6789" {
		t.Fatalf("%+v", c)
	}
}
