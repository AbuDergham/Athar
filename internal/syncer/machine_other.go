//go:build !windows

package syncer

// machineGUID is Windows only; MachineID falls back to the hostname.
func machineGUID() string { return "" }
