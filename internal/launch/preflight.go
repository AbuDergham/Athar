package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Preflight checks the folder, the drive and that the program exists (lookup only).
// The error messages are the ones the UI shows (spec §6.2):
// "Drive D: is not connected", "Folder no longer exists: <path>",
// "`codex` is not installed or not on PATH".
func Preflight(c Command) error {
	if c.Dir != "" {
		if vol := filepath.VolumeName(c.Dir); vol != "" {
			if _, err := os.Stat(vol + string(filepath.Separator)); err != nil {
				return fmt.Errorf("Drive %s is not connected", vol)
			}
		}
		if fi, err := os.Stat(c.Dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("Folder no longer exists: %s", c.Dir)
		}
	}
	if !programExists(c.Program) {
		return fmt.Errorf("`%s` is not installed or not on PATH", programName(c.Program))
	}
	return nil
}

// programExists looks the program up without running it.
func programExists(p string) bool {
	if p == "" {
		return false
	}
	if _, err := lookPath(p); err == nil {
		return true
	}
	if filepath.IsAbs(p) {
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	return false
}

// programName is the short name shown in messages: "codex" for codex,
// "opencode-cli" for C:\...\opencode-cli.exe.
func programName(p string) string {
	base := filepath.Base(p)
	if strings.ContainsAny(p, `\/`) {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return base
}
