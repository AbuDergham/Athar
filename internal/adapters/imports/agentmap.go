package imports

import (
	"regexp"
	"strings"
)

// claudeRoot matches a transcript under a Claude data root:
// <...>\.claude<suffix>\projects\... ; group 1 is the root folder.
var claudeRoot = regexp.MustCompile(`(?i)^(.*[\\/]\.claude[^\\/]*)[\\/]projects[\\/]`)

// pathRules map a (lower-case, backslash) source file path to the live agent
// that wrote it. Order matters: the more specific folders come first
// (antigravity-cli lives inside .gemini, Cursor is a VS Code fork).
var pathRules = []struct{ needle, agent string }{
	{`\antigravity-cli\`, "agy"},
	{`\.gemini\antigravity`, "antigravity"}, // antigravity, antigravity-backup, antigravity-ide
	{`\.gemini\tmp\`, "gemini"},
	{`\.codex\`, "codex"},
	{`\saoudrizwan.claude-dev\`, "cline"},
	{`\.cline\`, "cline"},
	{`\kilocode.kilo-code\`, "kilo"},
	{`\share\kilo\`, "kilo"},
	{`\cursor\user\`, "cursor"},
	{`\.cursor\`, "cursor"},
	{`\chatsessions\`, "copilot"},
	{`\emptywindowchatsessions\`, "copilot"},
	{`\opencode\`, "opencode"},
	{`\.qoder\`, "qoder"},
	{`\.qwen\`, "qwen"},
	{`\.zcode\`, "zcode"},
	{`\zed\threads\`, "zed"},
	{`\trae\user\`, "trae"},
	{`\trae cn\user\`, "trae"},
	{`\.continue\`, "continue"},
	{`\.lmstudio\`, "lmstudio"},
	{`\.neovate\`, "neovate"},
	{`\.commandcode\`, "commandcode"},
	{`\.dsh\`, "dsh"},
	{`\manicode\`, "codebuff"},
	{`\.codeium\windsurf\`, "windsurf"},
	{`\.hermes\`, "hermes"},
	{`\hermes\state.db`, "hermes"},
	{`\.claude\`, "claude"},
}

// labelAgents maps the tools' own agent names (AgentsView agent column, Pi
// Session Manager provider slug) to Athar adapter ids, for paths no rule knows.
var labelAgents = map[string]string{
	"claude":          "claude",
	"claude-code":     "claude",
	"claude_code":     "claude",
	"codex":           "codex",
	"gemini":          "gemini",
	"gemini-cli":      "gemini",
	"antigravity-cli": "agy",
	"agy":             "agy",
	"antigravity":     "antigravity",
	"vscode-copilot":  "copilot",
	"copilot-chat":    "copilot",
	"copilot":         "copilot-cli", // AgentsView's "copilot" is the GitHub Copilot CLI
	"cline":           "cline",
	"kilo":            "kilo",
	"kilo-code":       "kilo",
	"kilocode":        "kilo",
	"opencode":        "opencode",
	"qoder":           "qoder",
	"qwen":            "qwen",
	"qwen-code":       "qwen",
	"zcode":           "zcode",
	"zed":             "zed",
	"trae":            "trae",
	"cursor":          "cursor",
	"continue":        "continue",
	"hermes":          "hermes",
	"codebuff":        "codebuff",
	"windsurf":        "windsurf",
	"commandcode":     "commandcode",
	"command-code":    "commandcode",
	"neovate":         "neovate",
	"lmstudio":        "lmstudio",
	"dsh":             "dsh",
}

// mapAgent returns the live agent id and data root for a session whose source
// file is path and whose tool-assigned label is label. root is "" for the
// agent's default root; it is set only for Claude roots other than .claude
// (what the Claude adapter uses as Session.Root).
func mapAgent(path, label string) (agent, root string) {
	if m := claudeRoot.FindStringSubmatch(path); m != nil {
		root = m[1]
		base := root[strings.LastIndexAny(root, `\/`)+1:]
		if strings.EqualFold(base, ".claude") {
			root = ""
		}
		return "claude", strings.ReplaceAll(root, "/", `\`)
	}
	p := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	for _, r := range pathRules {
		if strings.Contains(p, r.needle) {
			return r.agent, ""
		}
	}
	return labelAgent(label), ""
}

// labelAgent maps a tool label through labelAgents; unknown labels are kept
// (lower-cased, as a stable id), an empty label becomes "unknown".
func labelAgent(label string) string {
	l := strings.ToLower(strings.TrimSpace(label))
	if a, ok := labelAgents[l]; ok {
		return a
	}
	if l == "" {
		return "unknown"
	}
	return l
}

// stripPrefix removes one "<p>:" prefix (the first candidate that matches).
func stripPrefix(id string, prefixes ...string) string {
	for _, p := range prefixes {
		if p != "" && len(id) > len(p)+1 && strings.HasPrefix(id, p+":") {
			return id[len(p)+1:]
		}
	}
	return id
}
