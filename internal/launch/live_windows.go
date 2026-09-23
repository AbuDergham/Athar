//go:build windows

package launch

import "golang.org/x/sys/windows"

// stillActive is the exit code GetExitCodeProcess reports for a running process.
const stillActive = 259

// processStart returns the creation time (FILETIME as uint64) of a running
// process. It only queries the process (PROCESS_QUERY_LIMITED_INFORMATION);
// image names are never checked.
func processStart(pid uint32) (uint64, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		return 0, false
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, false
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), true
}
