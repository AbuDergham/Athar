package syncer

import (
	"os"
	"regexp"
	"strings"
)

// MachineID returns the machine's stable id, or the hostname when it is
// unavailable ("unknown" when both are): the MachineGuid on Windows
// (read-only registry read), /etc/machine-id on Linux, the IOPlatformUUID
// on macOS.
func MachineID() string {
	if id := machineGUID(); id != "" {
		return id
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// parseMachineID returns the id in the contents of /etc/machine-id: 32
// lower-case hex digits on one line; "" for anything else (an empty or
// "uninitialized" file on a system that has not booted fully).
func parseMachineID(s string) string {
	s = strings.TrimSpace(s)
	if !machineIDRe.MatchString(s) {
		return ""
	}
	return s
}

var machineIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// parseIOPlatformUUID returns the IOPlatformUUID in the output of
// "ioreg -rd1 -c IOPlatformExpertDevice", or "".
func parseIOPlatformUUID(s string) string {
	if m := ioPlatformUUIDRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

var ioPlatformUUIDRe = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f-]{36})"`)
