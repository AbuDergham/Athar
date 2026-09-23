package syncer

import "os"

// MachineID returns the Windows MachineGuid (read-only registry read), or the
// hostname when unavailable ("unknown" when both are).
func MachineID() string {
	if id := machineGUID(); id != "" {
		return id
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}
