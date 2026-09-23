//go:build windows

package launch

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	procRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// CopyToClipboard puts text on the Windows clipboard (CF_UNICODETEXT).
func CopyToClipboard(text string) error {
	u16, err := windows.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}
	// The clipboard is opened and closed by the same OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()

	if r, _, err := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("clipboard: EmptyClipboard: %w", err)
	}
	size := uintptr(len(u16) * 2)
	h, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("clipboard: GlobalAlloc: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("clipboard: GlobalLock: %w", err)
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u16[0])), size)
	procGlobalUnlock.Call(h)
	if r, _, err := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("clipboard: SetClipboardData: %w", err)
	}
	// The system owns h now.
	return nil
}

// openClipboard retries for a short while: another program may hold it.
func openClipboard() error {
	var last error
	for i := 0; i < 20; i++ {
		r, _, err := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		last = err
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) && i >= 2 {
			break // e.g. no access to the desktop's clipboard; retrying will not help
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("clipboard: cannot open it (in use by another program?): %w", last)
}
