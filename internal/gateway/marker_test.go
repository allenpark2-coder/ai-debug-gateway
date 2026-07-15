package gateway

import "testing"

func TestMarkerRoundTripsExitCode(t *testing.T) {
	m := newMarker("txn-1")
	if len(m.nonce) != 32 { // 128-bit nonce, hex-encoded
		t.Fatalf("got nonce len %d, want 32", len(m.nonce))
	}

	line := m.shellSuffix()
	// Simulate the shell echoing the marker line back with exit status 0.
	buf := []byte("some output\n" + "GWMARK:" + m.transactionID + ":" + m.nonce + ":0\n")
	_ = line

	code, ok := m.find(buf)
	if !ok || code != 0 {
		t.Fatalf("got code=%d ok=%v, want 0 true", code, ok)
	}
}

func TestMarkerRejectsWrongTransactionOrNonce(t *testing.T) {
	m := newMarker("txn-1")
	other := newMarker("txn-2")

	buf := []byte("GWMARK:" + other.transactionID + ":" + other.nonce + ":0\n")
	if _, ok := m.find(buf); ok {
		t.Fatal("must not match a marker from a different transaction/nonce")
	}
}

func TestMarkerExtractsNonZeroExitCode(t *testing.T) {
	m := newMarker("txn-3")
	buf := []byte("GWMARK:" + m.transactionID + ":" + m.nonce + ":17\n")
	code, ok := m.find(buf)
	if !ok || code != 17 {
		t.Fatalf("got code=%d ok=%v, want 17 true", code, ok)
	}
}

func TestMarkerNotYetPresent(t *testing.T) {
	m := newMarker("txn-4")
	if _, ok := m.find([]byte("still running, no marker yet\n")); ok {
		t.Fatal("must not find a marker in output that has none")
	}
}
