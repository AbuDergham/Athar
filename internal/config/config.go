// Package config reads the optional athar.toml next to the exe (spec §8).
//
// Example:
//
//	[resume.codex]                     # per agent id; all keys optional
//	resume = "codex resume {id}"       # program + arguments, no shell syntax; {id} {cwd}
//	fork = ["--fork"]                  # extra arguments for a fork
//	skip_permissions = "--yolo"        # extra arguments to skip permission prompts
//	[display]
//	arabic = "shaped"                  # "shaped" | "raw"
//	[update]
//	check = true
//
// Every command may be written as one string (split on spaces, double quotes group a
// token, backslashes are literal) or as an array of strings. Unknown keys, bad values
// and shell metacharacters are errors, so typos do not go unnoticed.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the name of the configuration file, looked up next to the exe.
const FileName = "athar.toml"

// Values of [display] arabic.
const (
	ArabicShaped = "shaped" // shape Arabic to presentation forms and reorder for the terminal
	ArabicRaw    = "raw"    // print Arabic text unchanged
)

// Resume overrides one agent's commands. Resume is program + arguments; Fork and
// SkipPermissions are extra arguments (as in adapters.Caps). All are plain tokens
// (no shell syntax) with {id} and {cwd} placeholders. nil means "not set" (use the
// agent's default); an explicitly empty list (fork = []) is kept as an empty slice.
type Resume struct {
	Resume          []string `toml:"resume"`
	Fork            []string `toml:"fork"`
	SkipPermissions []string `toml:"skip_permissions"`
}

// Config is athar.toml.
type Config struct {
	Resume  map[string]Resume `toml:"resume"` // keyed by lower-case agent id
	Display struct {
		Arabic string `toml:"arabic"` // "shaped" | "raw" | "" (default chosen by the spike)
	} `toml:"display"`
	Update struct {
		Check *bool `toml:"check"`
	} `toml:"update"`
}

// Error reports everything wrong with an athar.toml that parsed as TOML:
// unknown keys and invalid values.
type Error struct {
	Path     string
	Problems []string
}

func (e *Error) Error() string {
	return e.Path + ": " + strings.Join(e.Problems, "; ")
}

// fileResume and fileConfig mirror Config for decoding: commands accept a string or
// an array of strings.
type fileResume struct {
	Resume          command `toml:"resume"`
	Fork            command `toml:"fork"`
	SkipPermissions command `toml:"skip_permissions"`
}

type fileConfig struct {
	Resume  map[string]fileResume `toml:"resume"`
	Display struct {
		Arabic string `toml:"arabic"`
	} `toml:"display"`
	Update struct {
		Check *bool `toml:"check"`
	} `toml:"update"`
}

// Load reads dir/athar.toml. A missing file returns the zero Config and no error.
// On any error (unreadable file, invalid TOML, unknown keys, bad values) it returns
// the zero Config, so a caller can report the error and continue with defaults.
// Syntax errors carry the line number; other problems are collected in an *Error.
func Load(dir string) (Config, error) {
	path := filepath.Join(dir, FileName)
	var fc fileConfig
	md, err := toml.DecodeFile(path, &fc)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}

	var problems []string
	if names := leafKeys(md.Undecoded()); len(names) > 0 {
		problems = append(problems, "unknown keys: "+strings.Join(names, ", "))
	}

	var c Config
	switch a := strings.ToLower(strings.TrimSpace(fc.Display.Arabic)); a {
	case "", ArabicShaped, ArabicRaw:
		c.Display.Arabic = a
	default:
		problems = append(problems, fmt.Sprintf("display.arabic = %q: must be %q or %q", fc.Display.Arabic, ArabicShaped, ArabicRaw))
	}
	c.Update.Check = fc.Update.Check

	resume, rp := convertResume(fc.Resume)
	problems = append(problems, rp...)
	c.Resume = resume

	if len(problems) > 0 {
		return Config{}, &Error{Path: path, Problems: problems}
	}
	return c, nil
}

// leafKeys returns the dotted names of keys in file order, leaving out a table
// header when one of its keys is also listed ("updates.check", not "updates" too).
func leafKeys(keys []toml.Key) []string {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.String()
	}
	var out []string
	for _, n := range names {
		parent := false
		for _, m := range names {
			if strings.HasPrefix(m, n+".") {
				parent = true
				break
			}
		}
		if !parent {
			out = append(out, n)
		}
	}
	return out
}

// convertResume validates the [resume.<agent>] tables and lower-cases agent ids.
func convertResume(in map[string]fileResume) (map[string]Resume, []string) {
	if len(in) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var problems []string
	out := make(map[string]Resume, len(in))
	for _, key := range keys {
		fr := in[key]
		id := strings.ToLower(strings.TrimSpace(key))
		if id == "" {
			problems = append(problems, fmt.Sprintf("resume.%q: empty agent id", key))
			continue
		}
		if _, dup := out[id]; dup {
			problems = append(problems, fmt.Sprintf("resume.%s: agent %q is configured twice", key, id))
			continue
		}
		prefix := "resume." + key + "."
		if fr.Resume != nil && (len(fr.Resume) == 0 || strings.TrimSpace(fr.Resume[0]) == "") {
			problems = append(problems, prefix+"resume: must start with a program")
		}
		for _, f := range []struct {
			name string
			args command
		}{{"resume", fr.Resume}, {"fork", fr.Fork}, {"skip_permissions", fr.SkipPermissions}} {
			if err := checkArgs(f.args); err != nil {
				problems = append(problems, prefix+f.name+": "+err.Error())
			}
		}
		out[id] = Resume{
			Resume:          []string(fr.Resume),
			Fork:            []string(fr.Fork),
			SkipPermissions: []string(fr.SkipPermissions),
		}
	}
	return out, problems
}

// UpdateCheckEnabled reports whether update checks are on (default true).
func (c Config) UpdateCheckEnabled() bool { return c.Update.Check == nil || *c.Update.Check }
