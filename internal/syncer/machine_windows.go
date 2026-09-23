//go:build windows

package syncer

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// machineGUID reads HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid from the
// 64-bit registry view (also from a 32-bit process); "" when unavailable.
// The key is opened with QUERY_VALUE only: nothing is ever written.
func machineGUID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}
