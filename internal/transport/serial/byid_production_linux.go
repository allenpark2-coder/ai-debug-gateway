//go:build linux && !integrationtest

package serial

const trustedByIDDir = "/dev/serial/by-id"

func serialByIDDir() string { return trustedByIDDir }

// Production discovery trusts upstream's serial enumeration. A by-id entry
// can annotate an enumerated port but can never introduce a new device.
func appendIntegrationPorts(ports []Port, _ map[string]string) []Port { return ports }
