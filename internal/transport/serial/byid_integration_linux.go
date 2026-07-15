//go:build linux && integrationtest

package serial

import "os"

const trustedByIDDir = "/dev/serial/by-id"

func serialByIDDir() string {
	if dir := os.Getenv("GATEWAYD_INTEGRATION_SERIAL_BY_ID_DIR"); dir != "" {
		return dir
	}
	return trustedByIDDir
}

func appendIntegrationPorts(ports []Port, byID map[string]string) []Port {
	seen := make(map[string]bool, len(ports))
	for _, port := range ports {
		seen[port.Path] = true
	}
	for device, link := range byID {
		if !seen[device] {
			ports = append(ports, Port{Path: device, ByIDPath: link})
		}
	}
	return ports
}
