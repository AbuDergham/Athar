//go:build windows

package launch

import (
	"errors"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procCountClipboardFormats      = user32.NewProc("CountClipboardFormats")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
)

// readClipboardText returns the CF_UNICODETEXT content (ok=false if none).
func readClipboardText(t *testing.T) (string, bool) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		t.Fatal(err)
	}
	defer procCloseClipboard.Call()
	if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
		return "", false
	}
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	size, _, _ := procGlobalSize.Call(h)
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 || size < 2 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)
	buf := make([]uint16, size/2)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), p, size)
	return windows.UTF16ToString(buf), true
}

// TestCopyToClipboard uses the real clipboard, so it only runs with
// ATHAR_CLIPBOARD_TEST=1 and only when the clipboard holds nothing but text,
// which it restores afterwards.
func TestCopyToClipboard(t *testing.T) {
	if os.Getenv("ATHAR_CLIPBOARD_TEST") != "1" {
		t.Skip("set ATHAR_CLIPBOARD_TEST=1 to use the real clipboard")
	}
	func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := openClipboard(); err != nil {
			if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Skipf("no clipboard access in this session: %v", err)
			}
			t.Fatal(err)
		}
		defer procCloseClipboard.Call()
		for _, f := range []uintptr{2 /* CF_BITMAP */, 8 /* CF_DIB */, 15 /* CF_HDROP */} {
			if r, _, _ := procIsClipboardFormatAvailable.Call(f); r != 0 {
				t.Skip("clipboard holds non-text content; not touching it")
			}
		}
		if n, _, _ := procCountClipboardFormats.Call(); n > 0 {
			if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
				t.Skip("clipboard holds non-text content; not touching it")
			}
		}
	}()
	prev, hadText := readClipboardText(t)
	t.Cleanup(func() {
		if hadText {
			_ = CopyToClipboard(prev)
		}
	})

	want := "Set-Location -LiteralPath 'D:\\مشروع'; claude --resume 'x'"
	if err := CopyToClipboard(want); err != nil {
		t.Fatal(err)
	}
	got, ok := readClipboardText(t)
	if !ok || got != want {
		t.Errorf("clipboard = %q (%v), want %q", got, ok, want)
	}
}
