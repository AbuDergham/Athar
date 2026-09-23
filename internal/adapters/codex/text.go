package codex

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

const (
	// maxTitleRunes caps titles taken from a prompt.
	maxTitleRunes = 100
	// maxArgRunes caps the raw arguments shown for a tool call without a known field.
	maxArgRunes = 200
	imageMarker = "[image]"

	ideContextPrefix = "# Context from my IDE setup"
	ideRequestMarker = "## My request for Codex:"
)

// injectedPrefixes start user-role texts that Codex injects itself (project
// instructions, environment, review triggers); they are not prompts.
var injectedPrefixes = []string{
	"<environment_context>",
	"<user_instructions>",
	"<user_action>",
	"<permissions instructions>",
	"<turn_aborted>",
	"<INSTRUCTIONS>",
	"# AGENTS.md instructions",
}

func isInjected(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range injectedPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

var cwdTag = regexp.MustCompile(`<cwd>([^<]*)</cwd>`)

// envContextCwd returns the <cwd> of an injected <environment_context> text.
func envContextCwd(text string) string {
	if m := cwdTag.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// userPromptText returns what the user typed: the VS Code extension prefixes
// prompts with an IDE-context block ending in "## My request for Codex:".
// Other prompts are returned verbatim.
func userPromptText(msg string) string {
	if strings.HasPrefix(strings.TrimSpace(msg), ideContextPrefix) {
		if i := strings.Index(msg, ideRequestMarker); i >= 0 {
			return strings.TrimSpace(msg[i+len(ideRequestMarker):])
		}
	}
	return msg
}

// titleFrom collapses whitespace, drops image markers and keeps the first 100 runes.
func titleFrom(text string) string {
	var words []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == imageMarker {
			continue
		}
		words = append(words, strings.Fields(line)...)
	}
	return truncateRunes(strings.Join(words, " "), maxTitleRunes)
}

// collapse joins the whitespace-separated fields of s with single spaces.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// truncateBytes keeps at most n bytes of s without splitting a UTF-8 sequence.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// skipSource reports whether a session_meta source marks a thread that
// Athar does not list: sub-agents and non-interactive runs (exec, mcp).
func skipSource(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(s) {
		case "exec", "mcp", "subagent":
			return true
		}
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		_, sub := obj["subagent"]
		return sub
	}
	return false
}

// shellFlags precede the script in wrapped commands like ["bash","-lc","make"].
var shellFlags = map[string]bool{"-c": true, "-lc": true, "-ic": true, "-Command": true, "-command": true, "/c": true, "/C": true}

// commandText renders a command given as a string or an argv array.
func commandText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return ""
	}
	if len(argv) >= 3 && shellFlags[argv[len(argv)-2]] {
		return strings.TrimSpace(argv[len(argv)-1])
	}
	return strings.TrimSpace(strings.Join(argv, " "))
}

// argFields are the tool arguments shown after the tool name, in this order.
var argFields = []string{"pattern", "query", "file_path", "path", "dir_path", "url"}

// callText renders a function_call as "name: <command or paths>" (spec §5.1:
// tool name, command line and file paths), else "name: <first 200 runes of the arguments>".
func callText(name, arguments string) string {
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &args) == nil {
		if name == "apply_patch" {
			var input string
			if json.Unmarshal(args["input"], &input) == nil && input != "" {
				return customCallText(name, input)
			}
		}
		for _, k := range []string{"command", "cmd"} {
			if c := commandText(args[k]); c != "" {
				return name + ": " + c
			}
		}
		var vals []string
		for _, k := range argFields {
			var v string
			if json.Unmarshal(args[k], &v) == nil && strings.TrimSpace(v) != "" {
				vals = append(vals, strings.TrimSpace(v))
			}
		}
		if len(vals) > 0 {
			return name + ": " + strings.Join(vals, " ")
		}
	}
	a := strings.TrimSpace(arguments)
	if a == "" || a == "{}" || a == "null" {
		return name
	}
	return name + ": " + truncateRunes(a, maxArgRunes)
}

// patchFilePrefixes mark the file lines of an apply_patch body.
var patchFilePrefixes = []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}

// customCallText renders a custom_tool_call (free-form input). For
// apply_patch it lists the files the patch touches.
func customCallText(name, input string) string {
	var paths []string
	seen := map[string]bool{}
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, p := range patchFilePrefixes {
			if strings.HasPrefix(line, p) {
				f := strings.TrimSpace(line[len(p):])
				if f != "" && !seen[f] {
					seen[f] = true
					paths = append(paths, f)
				}
			}
		}
	}
	if len(paths) > 0 {
		return name + ": " + strings.Join(paths, ", ")
	}
	in := strings.TrimSpace(input)
	if in == "" {
		return name
	}
	return name + ": " + truncateRunes(in, maxArgRunes)
}

// contentItem is a part of a message or of a structured tool output.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// itemsText joins the text parts of content items; images become "[image]".
func itemsText(items []contentItem) string {
	var parts []string
	for _, it := range items {
		switch it.Type {
		case "input_text", "output_text", "text":
			if it.Text != "" {
				parts = append(parts, it.Text)
			}
		case "input_image", "image", "output_image":
			parts = append(parts, imageMarker)
		}
	}
	return strings.Join(parts, "\n")
}

// outputText extracts the text of a function_call_output / custom_tool_call_output:
// a plain string, a JSON string {"output": …, "metadata": …} (older CLIs),
// an array of content items, or an object with "content" / "output".
// The caller truncates it to model.MaxToolOutput.
func outputText(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		if strings.HasPrefix(strings.TrimSpace(s), "{") {
			var o struct {
				Output *string `json:"output"`
			}
			if json.Unmarshal([]byte(s), &o) == nil && o.Output != nil {
				return *o.Output
			}
		}
		return s
	case '[':
		var items []contentItem
		if json.Unmarshal(raw, &items) != nil {
			return ""
		}
		return itemsText(items)
	case '{':
		var o struct {
			Content json.RawMessage `json:"content"`
			Output  json.RawMessage `json:"output"`
		}
		if json.Unmarshal(raw, &o) != nil {
			return ""
		}
		if len(o.Content) > 0 {
			return outputText(o.Content)
		}
		return outputText(o.Output)
	}
	return ""
}

// toolOutput builds a tool-output message, keeping at most MaxToolOutput bytes.
func toolOutput(text string, at time.Time) (model.Message, bool) {
	if strings.TrimSpace(text) == "" {
		return model.Message{}, false
	}
	return model.Message{Role: model.RoleTool, Kind: model.KindToolOutput, At: at,
		Text: truncateBytes(text, model.MaxToolOutput)}, true
}
