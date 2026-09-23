package launch

import (
	"runtime"
	"sort"
	"strings"
)

// WTArgs returns the wt.exe arguments that open c in a new Windows Terminal tab
// (spec §6.1):
//
//	-w 0 new-tab --title <T> --suppressApplicationTitle --inheritEnvironment -d <Dir> cmd.exe /d /s /k <CmdLine>
//
// Every ';' in the title, folder and command line is written '\;' so that wt
// does not treat it as a subcommand delimiter. wt wraps the command line in
// quotes (it contains a space) and `cmd /s` strips exactly that pair.
func WTArgs(c Command) []string {
	esc := func(s string) string { return strings.ReplaceAll(s, ";", `\;`) }
	line := c.CmdLine
	if !strings.Contains(line, " ") && strings.Contains(line, `"`) {
		// wt only quotes tokens containing a space; without the pair cmd /s
		// would strip the program's own quotes.
		line += " "
	}
	title := c.Title
	if title == "" {
		title = "Athar"
	}
	args := []string{"-w", "0", "new-tab", "--title", esc(title), "--suppressApplicationTitle", "--inheritEnvironment"}
	if c.Dir != "" {
		args = append(args, "-d", esc(c.Dir))
	}
	return append(args, "cmd.exe", "/d", "/s", "/k", esc(line))
}

// consoleCmdLine is the raw command line of cmd.exe when Windows Terminal is
// not available: cmd.exe /d /s /k "title <T> && <CmdLine>". sw is "/k" (tests
// use "/c").
func consoleCmdLine(c Command, sw string) string {
	title := SanitizeTitle(c.Title)
	if title == "" {
		title = "Athar"
	}
	return `cmd.exe /d /s ` + sw + ` "title ` + title + ` && ` + c.CmdLine + `"`
}

// mergeEnv returns base with the variables of set applied: an empty value
// removes the variable, any other value replaces it. Names compare
// case-insensitively on Windows.
func mergeEnv(base []string, set map[string]string) []string {
	same := func(a, b string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		name := kv
		// Windows keeps per-drive folders in variables named "=C:"; the name
		// ends at the first '=' after the first character.
		if i := strings.IndexByte(kv[min(1, len(kv)):], '='); i >= 0 {
			name = kv[:i+min(1, len(kv))]
		}
		drop := false
		for k := range set {
			if same(name, k) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := set[k]; v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}
