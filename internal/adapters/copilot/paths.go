package copilot

import (
	"encoding/json"
	"regexp"
	"strings"
)

// workspacePath turns a VS Code folder URI into a folder path. File URIs
// ("file:///c%3A/work/x", "file://server/share/x") become Windows paths with
// an upper-case drive letter; anything that is not a file URI (a plain path,
// vscode-remote://…) is returned unchanged.
func workspacePath(ws string) string {
	ws = strings.TrimSpace(ws)
	const scheme = "file://"
	if len(ws) < len(scheme) || !strings.EqualFold(ws[:len(scheme)], scheme) {
		return ws
	}
	rest := ws[len(scheme):]
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
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
	return drivePath(p)
}

// drivePath turns a URI path ("/c:/work/x") into a Windows path ("C:\work\x").
// Paths without a drive letter keep their forward slashes.
func drivePath(p string) string {
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

// uriObject is a serialized vscode.Uri ({$mid, scheme, path, fsPath, external}).
type uriObject struct {
	Scheme   string `json:"scheme"`
	Path     string `json:"path"`
	FsPath   string `json:"fsPath"`
	External string `json:"external"`
}

// uriPath returns a readable path for a serialized Uri: fsPath, else the
// decoded path of a file URI, else the external form. "" when unknown.
func uriPath(raw json.RawMessage) string {
	var u uriObject
	if len(raw) == 0 || json.Unmarshal(raw, &u) != nil {
		return ""
	}
	return u.readable()
}

func (u uriObject) readable() string {
	switch {
	case u.FsPath != "":
		// VS Code writes fsPath with a lower-case drive letter.
		if len(u.FsPath) >= 3 && isASCIILetter(u.FsPath[0]) && u.FsPath[1] == ':' && u.FsPath[2] == '\\' {
			return strings.ToUpper(u.FsPath[:1]) + u.FsPath[1:]
		}
		return u.FsPath
	case strings.EqualFold(u.Scheme, "file") && u.Path != "":
		return drivePath(u.Path)
	case u.External != "":
		return u.External
	}
	return u.Path
}

// baseName is the last element of a Windows or slash path.
func baseName(p string) string {
	p = strings.TrimRight(p, `\/`)
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// mdLink matches a markdown link [label](target).
var mdLink = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)

// cleanLinks rewrites markdown links the way VS Code shows them: an empty
// label (a file widget) becomes the target's path. When all is true, labelled
// links are also reduced to their label.
func cleanLinks(s string, all bool) string {
	if !strings.Contains(s, "](") {
		return s
	}
	return mdLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLink.FindStringSubmatch(m)
		label, target := sub[1], sub[2]
		if label != "" {
			if all {
				return label
			}
			return m
		}
		if i := strings.IndexByte(target, '#'); i >= 0 {
			target = target[:i]
		}
		return workspacePath(target)
	})
}
