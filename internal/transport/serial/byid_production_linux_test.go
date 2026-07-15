//go:build linux && !integrationtest

package serial

import "testing"

func TestProductionByIDDiscoveryCannotBeRedirectedOrAddPorts(t *testing.T) {
	t.Setenv("GATEWAYD_INTEGRATION_SERIAL_BY_ID_DIR", t.TempDir())
	if got := serialByIDDir(); got != "/dev/serial/by-id" {
		t.Fatalf("root = %q", got)
	}
	want := []Port{{Path: "/dev/ttyUSB0"}}
	got := appendIntegrationPorts(want, map[string]string{"/tmp/not-enumerated": "/tmp/by-id"})
	if len(got) != 1 || got[0].Path != want[0].Path {
		t.Fatalf("ports = %+v", got)
	}
}
