//go:build linux

package serial

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListUsesConfiguredByIDDirectory(t *testing.T) {
	dir := t.TempDir()
	device := filepath.Join(dir, "tty-test")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	byID := filepath.Join(dir, "by-id")
	if err := os.Mkdir(byID, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(byID, "usb-test")
	if err := os.Symlink(device, identity); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAYD_SERIAL_BY_ID_DIR", byID)
	ports, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range ports {
		if port.Path == device && port.ByIDPath == identity {
			return
		}
	}
	t.Fatalf("configured identity %q -> %q absent from ports: %+v", identity, device, ports)
}
