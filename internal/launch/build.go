package launch

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/AbuDergham/Athar/internal/adapters"
)

// noCwdSearchEnv stops cmd.exe from looking for a program in the current folder
// before PATH, so a claude.cmd planted in a project folder is never run instead
// of the real agent. It is set in the child's environment block and embedded
// at the start of every CmdLine.
const noCwdSearchEnv = "NoDefaultCurrentDirectoryInExePath"

// Package variables replaced by tests so that program resolution does not depend
// on what is installed on the test machine. Nothing else modifies them.
var (
	lookPath    = exec.LookPath
	getenv      = os.Getenv
	userHomeDir = os.UserHomeDir
)

// Build prepares the command for a target using the agent's caps and optional
// overrides from athar.toml. skip adds the agent's skip-permissions arguments.
// It validates every embedded value (§6.1) and returns an error if a value is unsafe
// and cannot be passed another way.
//
// override, when non-empty, replaces the agent's own command (program + arguments
// with {id} and {cwd} placeholders); for an app-only agent it replaces the app
// command. Errors wrap ErrUnsupported or ErrUnsafe.
func Build(t Target, caps adapters.Caps, action Action, skip bool, override []string) (Command, error) {
	var (
		base       []string
		candidates []string
		app        *adapters.AppCommand
	)
	switch {
	case len(caps.Resume) > 0 || (len(override) > 0 && caps.App == nil):
		base, candidates = caps.Resume, caps.ProgramCandidates
	case caps.App != nil:
		app = caps.App
		base = append([]string{app.Program}, app.Args...)
		candidates = app.Candidates
	default:
		return Command{}, fmt.Errorf("launch: %s has no resume or app command: %w", t.Agent, ErrUnsupported)
	}
	if len(override) > 0 {
		if len(base) == 0 || !strings.EqualFold(override[0], base[0]) {
			candidates = nil // the agent's install paths belong to its own program
		}
		base = override
	}
	if len(base) == 0 || base[0] == "" {
		return Command{}, fmt.Errorf("launch: empty program for %s: %w", t.Agent, ErrUnsafe)
	}

	var extra []string
	switch action {
	case ActionResume:
	case ActionFork:
		if app != nil || len(caps.Fork) == 0 {
			return Command{}, fmt.Errorf("launch: %s cannot fork a session: %w", t.Agent, ErrUnsupported)
		}
		extra = append(extra, caps.Fork...)
	default:
		return Command{}, fmt.Errorf("launch: unknown action %d: %w", action, ErrUnsupported)
	}
	if skip {
		if app != nil || len(caps.SkipPermissions) == 0 {
			return Command{}, fmt.Errorf("launch: %s has no skip-permissions flag: %w", t.Agent, ErrUnsupported)
		}
		extra = append(extra, caps.SkipPermissions...)
	}

	args, err := substitute(append(append([]string{}, base[1:]...), extra...), t)
	if err != nil {
		return Command{}, err
	}

	prog := base[0]
	if p, ok := expandVars(prog); ok {
		prog = p
	}
	if !cmdSafe(prog) {
		return Command{}, fmt.Errorf("launch: program %q: %w", prog, ErrUnsafe)
	}
	program := resolveProgram(prog, candidates)
	if !cmdSafe(program) {
		return Command{}, fmt.Errorf("launch: program %q: %w", program, ErrUnsafe)
	}

	env := map[string]string{}
	if caps.RootEnv != "" {
		if !envNamePattern.MatchString(caps.RootEnv) {
			return Command{}, fmt.Errorf("launch: environment variable name %q: %w", caps.RootEnv, ErrUnsafe)
		}
		env[caps.RootEnv] = t.Root
	}

	dir := t.Cwd
	if dir == "" {
		if h, err := userHomeDir(); err == nil {
			dir = h
		}
	}

	title := SanitizeTitle(t.Title)
	if title == "" {
		title = SanitizeTitle(t.Agent)
	}
	if title == "" {
		title = "Athar"
	}

	c := Command{
		Title:   title,
		Dir:     dir,
		Env:     env,
		Program: program,
		Args:    args,
	}
	if app != nil {
		c.IsApp = true
		c.AppName = app.Name
	}
	c.CmdLine = cmdLine(program, args, env)
	c.PowerShell = powerShell(t.Cwd, program, args, env)
	c.Shell = shellLine(t.Cwd, program, args, env)
	return c, nil
}

// substitute replaces {id} and {cwd} in args and validates every result.
// {id} must pass the id whitelist. {cwd} becomes the folder when it passes the
// path whitelist, "." otherwise (the folder is still the working directory),
// and an argument containing {cwd} is dropped when the session has no folder.
func substitute(in []string, t Target) ([]string, error) {
	cwdArg := t.Cwd
	if t.Cwd != "" && !cmdSafe(t.Cwd) {
		cwdArg = "."
	}
	out := make([]string, 0, len(in))
	for _, a := range in {
		if strings.Contains(a, "{id}") {
			if !validID(t.NativeID) {
				return nil, fmt.Errorf("launch: session id %q: %w", t.NativeID, ErrUnsafe)
			}
			a = strings.ReplaceAll(a, "{id}", t.NativeID)
		}
		if strings.Contains(a, "{cwd}") {
			if t.Cwd == "" {
				continue
			}
			a = strings.ReplaceAll(a, "{cwd}", cwdArg)
		}
		if !cmdSafe(a) {
			return nil, fmt.Errorf("launch: argument %q: %w", a, ErrUnsafe)
		}
		out = append(out, a)
	}
	return out, nil
}

// resolveProgram returns name when it is found on PATH, else the first existing
// candidate (with %VARS% expanded), else name itself (Preflight reports it).
func resolveProgram(name string, candidates []string) string {
	if _, err := lookPath(name); err == nil {
		return name
	}
	for _, cand := range candidates {
		p, ok := expandVars(cand)
		if !ok || p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return name
}

// expandVars replaces %NAME% with the variable's value. ok is false when a
// referenced variable is not set (the string is returned unchanged).
func expandVars(s string) (string, bool) {
	var b strings.Builder
	rest := s
	for {
		i := strings.IndexByte(rest, '%')
		if i < 0 {
			b.WriteString(rest)
			return b.String(), true
		}
		j := strings.IndexByte(rest[i+1:], '%')
		if j < 0 {
			return s, false
		}
		name := rest[i+1 : i+1+j]
		v := ""
		if varNamePattern.MatchString(name) {
			v = getenv(name)
		}
		if v == "" {
			return s, false
		}
		b.WriteString(rest[:i])
		b.WriteString(v)
		rest = rest[i+2+j:]
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cmdLine builds the one-token command for `cmd /d /s /k`:
// `set "NoDefaultCurrentDirectoryInExePath=1" && `, then `set "VAR=value" && `
// for each variable (sorted), then the program and arguments, quoted where
// needed. Values that fail the whitelist are not embedded; they reach the child
// only through the environment block.
func cmdLine(program string, args []string, env map[string]string) string {
	var b strings.Builder
	b.WriteString(`set "` + noCwdSearchEnv + `=1" && `)
	for _, k := range sortedKeys(env) {
		v := env[k]
		if v != "" && !cmdSafe(v) {
			continue
		}
		b.WriteString(`set "`)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteString(`" && `)
	}
	b.WriteString(quoteCmd(program))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(quoteCmd(a))
	}
	return b.String()
}

// powerShell builds the copyable form (§6.4):
// Set-Location -LiteralPath '<cwd>'; $env:VAR='v'; & '<program>' args…
func powerShell(cwd, program string, args []string, env map[string]string) string {
	var parts []string
	if cwd != "" && !strings.ContainsRune(cwd, 0) {
		parts = append(parts, "Set-Location -LiteralPath "+quotePS(cwd))
	}
	for _, k := range sortedKeys(env) {
		parts = append(parts, "$env:"+k+"="+quotePS(strings.ReplaceAll(env[k], "\x00", "")))
	}
	var b strings.Builder
	if psBareProgram.MatchString(program) {
		b.WriteString(program)
	} else {
		b.WriteString("& ")
		b.WriteString(quotePS(program))
	}
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(psArg(a))
	}
	parts = append(parts, b.String())
	return strings.Join(parts, "; ")
}
