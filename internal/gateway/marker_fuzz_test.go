package gateway

import (
	"strings"
	"testing"
)

// FuzzMarker proves marker.find never reports a completion unless the
// exact marker line for this transaction ID and nonce is literally
// present in the buffer -- a random or mismatched marker (wrong
// transaction, wrong nonce, truncated, or otherwise corrupted) must
// never be mistaken for this transaction's own completion.
func FuzzMarker(f *testing.F) {
	const nonce = "0123456789abcdef0123456789abcdef"
	f.Add("txn-1", "some output\nGWMARK:txn-1:"+nonce+":0\n")
	f.Add("txn-1", "GWMARK:txn-2:fedcba9876543210fedcba9876543210:1\n")
	f.Add("txn-1", "still running, no marker yet\n")
	f.Add("txn-1", "")
	f.Add("txn-1", "GWMARK::::")
	f.Add("txn:1", "GWMARK:txn:1:"+nonce+":0\n")
	f.Add("txn-1", "GWMARK:txn-1:"+nonce[:31]+"g:0\n") // one corrupted nonce char
	f.Fuzz(func(t *testing.T, txn, buf string) {
		m := marker{transactionID: txn, nonce: nonce}
		code, found := m.find([]byte(buf))
		if !found {
			return
		}
		want := "GWMARK:" + m.transactionID + ":" + m.nonce + ":"
		if !strings.Contains(buf, want) {
			t.Fatalf("find() reported found=true (code=%d) for buf=%q, but %q is not literally present", code, buf, want)
		}
	})
}
