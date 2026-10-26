//go:build !windows && !linux && !darwin

package syncer

// machineGUID is unknown here; MachineID falls back to the hostname.
func machineGUID() string { return "" }
