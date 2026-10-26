//go:build linux

package syncer

import "os"

// machineGUID reads the systemd/D-Bus machine id; "" when unavailable.
func machineGUID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if id := parseMachineID(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}
