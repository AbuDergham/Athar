package launch

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrUnsafe is returned (wrapped) by Build when a value that must appear in the
// command text fails the whitelist of spec §6.1 and cannot be passed another way.
var ErrUnsafe = errors.New("unsafe value")

// ErrUnsupported is returned (wrapped) by Build when the agent has no command for
// the requested action (no resume/app command, no fork, no skip-permissions flag).
var ErrUnsupported = errors.New("not supported")

// maxTitle is the maximum length of a tab/window title in runes.
const maxTitle = 60

var (
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// varNamePattern also allows the parentheses of %ProgramFiles(x86)%.
	varNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_()]*$`)
	// psBare matches arguments PowerShell passes to a native command unchanged
	// when written without quotes: flags, plain words and ".".
	psBare = regexp.MustCompile(`^(\.|--?[A-Za-z][A-Za-z0-9-]*|[A-Za-z][A-Za-z0-9._-]*)$`)
	// psBareProgram matches program names that need neither quotes nor '&'.
	psBareProgram = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*$`)
)

// validID reports whether a session id may be embedded in a command line.
func validID(id string) bool { return idPattern.MatchString(id) }

// cmdSafe reports whether a path or argument may be embedded in a cmd.exe
// command line (inside double quotes when needed): it must not be empty and must
// not contain '"', '%', '!', '^' or control characters (CR, LF, TAB, NUL, ...).
func cmdSafe(s string) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == '"' || r == '%' || r == '!' || r == '^':
			return false
		case r < 0x20 || r == 0x7f:
			return false
		}
	}
	return true
}

// SanitizeTitle keeps only letters (any script), digits, spaces and ".-_·",
// collapses whitespace, drops leading '-' and '.', and cuts to 60 runes. The
// result is safe to embed anywhere in a cmd.exe or wt.exe command line.
func SanitizeTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' || r == '·':
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		case unicode.IsSpace(r):
			space = true
		}
	}
	out := strings.TrimLeft(b.String(), "-. ")
	if utf8.RuneCountInString(out) > maxTitle {
		out = string([]rune(out)[:maxTitle])
	}
	return strings.TrimRight(out, " ")
}

// quoteCmd quotes a validated token for a cmd.exe command line. Tokens that
// contain a space or a character cmd.exe or a batch shim would interpret
// (& | < > ( ) , ; =) are wrapped in double quotes; trailing backslashes are
// doubled so the closing quote is not escaped for the program's argv parser.
func quoteCmd(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t&|<>(),;=") {
		return s
	}
	n := len(s) - len(strings.TrimRight(s, `\`))
	return `"` + s + strings.Repeat(`\`, n) + `"`
}

// isPSQuote reports whether PowerShell treats r as a single quote.
func isPSQuote(r rune) bool {
	return r == '\'' || r == '‘' || r == '’' || r == '‚' || r == '‛'
}

// quotePS returns s as a PowerShell single-quoted string literal. Every kind of
// single quote PowerShell recognises (', ‘, ’, ‚, ‛) is doubled.
func quotePS(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		if isPSQuote(r) {
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// psArg writes one argument of a native command in PowerShell syntax.
func psArg(s string) string {
	if psBare.MatchString(s) {
		return s
	}
	return quotePS(s)
}
