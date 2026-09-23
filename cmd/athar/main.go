// Command athar finds, reads and resumes your past conversations with any AI agent.
//
// Usage:
//
//	athar                     open the search screen (syncs in the background)
//	athar --sync              sync once without the UI and exit 0/1
//	athar --rescan            re-read every source, not only the changed ones
//	athar --data-dir <path>   keep the archive in <path> instead of athar-data next to athar.exe
//	athar --no-update-check   never contact GitHub
//	athar --version           print the version
//
// The archive lives in athar-data next to athar.exe (spec §4.1). Athar refuses
// to start from a temporary or read-only folder unless --data-dir is given.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

// Names inside the data folder.
const (
	dataDirName = "athar-data"    // default data folder next to athar.exe
	archiveName = "archive.db"    // the archive (spec §4.1)
	syncLogName = "last-sync.txt" // errors of --sync runs (spec §4.3)
	backupName  = "backup"        // weekly backups (spec §4.8)
)

func main() {
	os.Exit(run(os.Args[1:], realSystem()))
}

// run is athar with its command-line arguments; it returns the exit code:
// 0 success, 1 failure, 2 bad command line.
func run(args []string, sys *system) int {
	opts, err := parseFlags(args, sys.stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if opts.version {
		fmt.Fprintln(sys.stdout, "athar", version)
		return 0
	}

	sys.cleanupOld()
	exe, err := sys.executable()
	if err != nil {
		return sys.fatal(opts, fmt.Errorf("cannot locate athar.exe: %w", err))
	}
	dataDir, err := resolveDataDir(opts.dataDir, filepath.Dir(exe), sys)
	if err != nil {
		return sys.fatal(opts, err)
	}
	if opts.sync {
		return runSync(opts, dataDir, sys)
	}
	return runUI(opts, exe, dataDir, sys)
}
