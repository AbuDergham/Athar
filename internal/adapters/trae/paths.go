package trae

import (
	"encoding/json"
	"strings"
)

// workspaceFolder reads a workspaceStorage workspace.json and returns the
// project folder: "folder" for a folder window, or the folder that contains
// the .code-workspace file of a multi-root window ("workspace", older
// builds "configuration"). Non-file URIs (vscode-remote://…) have no local
// folder and yield "".
func workspaceFolder(data []byte) string {
	var w struct {
		Folder        json.RawMessage `json:"folder"`
		Workspace     json.RawMessage `json:"workspace"`
		Configuration json.RawMessage `json:"configuration"`
	}
	if json.Unmarshal(data, &w) != nil {
		return ""
	}
	if p := fileURIPath(str(w.Folder)); p != "" {
		return p
	}
	for _, raw := range []json.RawMessage{w.Workspace, w.Configuration} {
		if p := fileURIPath(str(raw)); p != "" {
			return parentDir(p)
		}
	}
	return ""
}

// fileURIPath turns a VS Code file URI ("file:///d%3A/work/x",
// "file://server/share/x") into a Windows path with an upper-case drive
// letter. Anything that is not a file URI yields "".
func fileURIPath(uri string) string {
	uri = strings.TrimSpace(uri)
	const scheme = "file://"
	if len(uri) < len(scheme) || !strings.EqualFold(uri[:len(scheme)], scheme) {
		return ""
	}
	rest := uri[len(scheme):]
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

// parentDir returns the folder of a Windows (or slash) path, keeping the
// trailing backslash of a drive root ("C:\a.code-workspace" → "C:\").
func parentDir(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return ""
	}
	dir := p[:i]
	if len(dir) == 2 && dir[1] == ':' {
		return dir + `\`
	}
	return dir
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
