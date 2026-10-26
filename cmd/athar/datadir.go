package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// refusal is the §2.1 message: the default data folder next to athar.exe
// cannot be used. There is no fallback location.
type refusal struct {
	folder string // the folder of athar.exe
	reason string // "is not writable" | "is a temporary folder"
	err    error  // what failed, for "is not writable"
}

func (r *refusal) Error() string {
	return fmt.Sprintf("Athar keeps its archive next to athar.exe, but %s %s. "+
		"Move athar.exe to a folder you own, for example D:\\Tools\\Athar, or run it with --data-dir <path>.",
		r.folder, r.reason)
}

func (r *refusal) Unwrap() error { return r.err }

// probePrefix names the file created and removed to test that a folder is writable.
const probePrefix = ".athar-probe-"

// resolveDataDir returns the data folder, created when missing.
//
// With --data-dir (flagDir != "") that folder is used wherever it is; it only
// has to be writable. Otherwise, on Windows, the folder is athar-data next to
// athar.exe, and Athar refuses (spec §2.1, §4.1) when exeDir is under a
// temporary folder (e.g. athar.exe opened from inside a zip) or when it cannot
// write there. On other systems see unixDataDir.
func resolveDataDir(flagDir, exeDir string, sys *system) (string, error) {
	if flagDir != "" {
		dir, err := filepath.Abs(flagDir)
		if err != nil {
			return "", fmt.Errorf("--data-dir %s: %w", flagDir, err)
		}
		if err := makeWritable(dir); err != nil {
			return "", fmt.Errorf("Athar cannot use the data folder %s (--data-dir): %w", dir, err)
		}
		return dir, nil
	}
	if sys.goos != "windows" {
		return unixDataDir(exeDir, sys)
	}

	if isTemporary(exeDir, tempRoots(sys)) {
		return "", &refusal{folder: exeDir, reason: "is a temporary folder"}
	}
	dir := filepath.Join(exeDir, dataDirName)
	// Probe athar-data when it exists (that is where Athar writes), else the
	// exe folder, so a read-only folder never gets an athar-data created in it.
	probeDir := exeDir
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		probeDir = dir
	}
	if err := writable(probeDir); err != nil {
		return "", &refusal{folder: exeDir, reason: "is not writable", err: err}
	}
	if err := makeWritable(dir); err != nil {
		return "", &refusal{folder: exeDir, reason: "is not writable", err: err}
	}
	return dir, nil
}

// unixDataDir is the data folder on Linux and macOS (spec §4.1): athar-data
// next to the binary when that folder exists (a portable copy), else the
// user's data folder, $XDG_DATA_HOME/athar (~/.local/share/athar) or, on
// macOS, ~/Library/Application Support/Athar. Programs on these systems
// usually live in folders the user cannot write to, so the binary's folder
// is never created into.
func unixDataDir(exeDir string, sys *system) (string, error) {
	portable := filepath.Join(exeDir, dataDirName)
	if fi, err := os.Stat(portable); err == nil && fi.IsDir() {
		if err := writable(portable); err != nil {
			return "", fmt.Errorf("Athar cannot use the data folder %s next to athar: %w. "+
				"Make it writable, remove it to use your user data folder, or run athar with --data-dir <path>", portable, err)
		}
		return portable, nil
	}
	dir, err := userDataDir(sys)
	if err != nil {
		return "", err
	}
	if err := makeWritable(dir); err != nil {
		return "", fmt.Errorf("Athar cannot use the data folder %s: %w. Run athar with --data-dir <path>", dir, err)
	}
	return dir, nil
}

// userDataDir is where Athar keeps its archive for the user on Linux and
// macOS (not created here).
func userDataDir(sys *system) (string, error) {
	if sys.goos != "darwin" {
		// XDG: only an absolute $XDG_DATA_HOME counts.
		if x := sys.getenv("XDG_DATA_HOME"); filepath.IsAbs(x) {
			return filepath.Join(x, "athar"), nil
		}
	}
	home, err := sys.homeDir()
	if err == nil && home == "" {
		err = errors.New("no home folder")
	}
	if err != nil {
		return "", fmt.Errorf("Athar cannot find your data folder (%w); run it with --data-dir <path>", err)
	}
	if sys.goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Athar"), nil
	}
	return filepath.Join(home, ".local", "share", "athar"), nil
}

// makeWritable creates dir when missing and checks that files can be created in it.
func makeWritable(dir string) error {
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return fmt.Errorf("%s is a file, not a folder", dir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writable(dir)
}

// writable creates and removes a probe file in dir.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, probePrefix+"*")
	if err != nil {
		return err
	}
	name := f.Name()
	cerr := f.Close()
	if err := os.Remove(name); err != nil {
		return err
	}
	return cerr
}

// tempRoots lists the temporary folders: os.TempDir(), %TEMP%, %TMP% and
// %LOCALAPPDATA%\Temp, canonicalized. A drive root is left out, since it
// would make every folder of that drive temporary.
func tempRoots(sys *system) []string {
	cands := []string{sys.tempDir(), sys.getenv("TEMP"), sys.getenv("TMP")}
	if la := sys.getenv("LOCALAPPDATA"); la != "" {
		cands = append(cands, filepath.Join(la, "Temp"))
	}
	var roots []string
	for _, c := range cands {
		if c == "" {
			continue
		}
		r := canonical(c)
		if filepath.Dir(r) == r { // a volume root such as D:\ or /
			continue
		}
		roots = append(roots, r)
	}
	return roots
}

// isTemporary reports whether dir is one of roots or inside one, after
// resolving 8.3 short names, links and letter case.
func isTemporary(dir string, roots []string) bool {
	canon := make([]string, len(roots))
	for i, r := range roots {
		canon[i] = canonical(r)
	}
	return underAny(canonical(dir), canon)
}

// canonical makes p absolute and, when it exists, resolves links and 8.3
// short names (filepath.EvalSymlinks returns the long names on Windows).
func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return filepath.Clean(p)
}

// underAny reports whether the clean path dir equals or is inside one of the
// clean paths roots, comparing case-insensitively (Windows paths).
func underAny(dir string, roots []string) bool {
	for _, r := range roots {
		r = strings.TrimRight(r, `\/`)
		if r == "" {
			continue
		}
		if strings.EqualFold(dir, r) {
			return true
		}
		if len(dir) > len(r) && os.IsPathSeparator(dir[len(r)]) && strings.EqualFold(dir[:len(r)], r) {
			return true
		}
	}
	return false
}

// cloudFolders are the names of synced folders in which a live SQLite file
// can be corrupted (spec §4.1).
var cloudFolders = []string{"OneDrive", "Dropbox", "Google Drive", "iCloudDrive"}

// cloudFolder returns the cloud service whose folder contains dir, or "".
func cloudFolder(dir string) string {
	lower := strings.ToLower(dir)
	for _, name := range cloudFolders {
		if strings.Contains(lower, strings.ToLower(name)) {
			return name
		}
	}
	return ""
}
