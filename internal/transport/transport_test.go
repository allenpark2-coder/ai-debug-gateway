package transport

import "testing"

func TestIdentityKnown(t *testing.T) {
	if (Identity{}).Known() {
		t.Fatal("zero-value identity must not be Known")
	}
	if !(Identity{Kind: "usb-serial-by-id", Key: "x"}).Known() {
		t.Fatal("identity with kind and key must be Known")
	}
}
