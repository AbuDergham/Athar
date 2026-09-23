package continuedev

import (
	"strconv"
	"strings"
	"time"
)

// workspacePath turns Continue's workspaceDirectory into a folder path.
// VS Code file URIs ("file:///d%3A/work/x", "file://server/share/x") become
// Windows paths with an upper-case drive letter; anything that is not a
// file URI (a plain path, vscode-remote://…) is returned unchanged.
func workspacePath(ws string) string {
	ws = strings.TrimSpace(ws)
	const scheme = "file://"
	if len(ws) < len(scheme) || !strings.EqualFold(ws[:len(scheme)], scheme) {
		return ws
	}
	rest := ws[len(scheme):]
	host, p := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, p = rest[:i], rest[i:]
	}
	host, p = percentDecode(host), percentDecode(p)
	if strings.EqualFold(host, "localhost") {
		host = ""
	}
	if host != "" { // UNC share
		return `\\` + host + strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)
	}
	if len(p) >= 3 && p[0] == '/' && isASCIILetter(p[1]) && p[2] == ':' {
		tail := strings.Trim(strings.ReplaceAll(p[3:], "/", `\`), `\`)
		return strings.ToUpper(p[1:2]) + `:\` + tail
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// percentDecode decodes %XX escapes and leaves malformed ones as they are.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isASCIILetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'f'
}

func unhex(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return (c | 0x20) - 'a' + 10
}

// parseDateCreated reads the index's dateCreated: epoch milliseconds as a
// string (Date.now().toString()), epoch seconds, or an RFC 3339 date. Zero
// when unknown.
func parseDateCreated(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		switch {
		case n <= 0:
			return time.Time{}
		case n >= 100_000_000_000: // milliseconds (seconds would be year 5138+)
			return time.UnixMilli(n).UTC()
		default:
			return time.Unix(n, 0).UTC()
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
