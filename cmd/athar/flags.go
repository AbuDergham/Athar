package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// options are the command-line flags.
type options struct {
	dataDir       string // --data-dir: "" = athar-data next to athar.exe
	sync          bool   // --sync: one sync without the UI
	rescan        bool   // --rescan: ignore stored fingerprints
	noUpdateCheck bool   // --no-update-check
	version       bool   // --version
}

const usage = `Usage: athar [options]

Athar finds, reads and resumes your past conversations with AI agents.

Options:
  --sync              sync once without the UI and exit (0 = ok, 1 = errors;
                      errors are appended to athar-data\last-sync.txt)
  --rescan            re-read every source, not only the changed ones
  --data-dir <path>   keep the archive in <path> instead of athar-data next to athar.exe
  --no-update-check   never contact GitHub
  --version           print the version and exit
`

// parseFlags parses the command line. Both -flag and --flag work. Errors
// (and the usage text) are written to w; --help returns flag.ErrHelp.
func parseFlags(args []string, w io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("athar", flag.ContinueOnError)
	fs.SetOutput(w)
	fs.Usage = func() { fmt.Fprint(w, usage) }
	fs.StringVar(&o.dataDir, "data-dir", "", "")
	fs.BoolVar(&o.sync, "sync", false, "")
	fs.BoolVar(&o.rescan, "rescan", false, "")
	fs.BoolVar(&o.noUpdateCheck, "no-update-check", false, "")
	fs.BoolVar(&o.version, "version", false, "")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	fail := func(msg string) (options, error) {
		fmt.Fprintln(w, msg)
		fs.Usage()
		return options{}, errors.New(msg)
	}
	if fs.NArg() > 0 {
		return fail(fmt.Sprintf("unexpected argument %q (type your search in Athar's search box)", fs.Arg(0)))
	}
	dataDirSet := false
	fs.Visit(func(f *flag.Flag) { dataDirSet = dataDirSet || f.Name == "data-dir" })
	if dataDirSet && o.dataDir == "" {
		return fail("--data-dir needs a folder")
	}
	return o, nil
}

// restartArgs are the arguments for the new athar.exe after an update:
// the same data folder and update setting, without a second --rescan.
// dataDir is the --data-dir folder, "" when the default is used.
func restartArgs(o options, dataDir string) []string {
	var args []string
	if dataDir != "" {
		args = append(args, "--data-dir", dataDir)
	}
	if o.noUpdateCheck {
		args = append(args, "--no-update-check")
	}
	return args
}
