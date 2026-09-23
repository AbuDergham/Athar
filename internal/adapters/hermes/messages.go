package hermes

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// Markers Hermes writes into message content (agent/context_compressor.py,
// agent/skill_commands.py). They must match Hermes byte for byte.
const (
	contentJSONPrefix = "\x00json:" // list/dict content (multimodal parts)

	compactionPrefix    = "[CONTEXT COMPACTION" // current summary carrier (any dash)
	legacySummaryPrefix = "[CONTEXT SUMMARY]:"  // older summary carrier
	summaryEndMarker    = "--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---"
	mergedPriorHeader   = "[PRIOR CONTEXT — for reference only; not a new message]"
	mergedDelimiter     = "[END OF PRIOR CONTEXT — COMPACTION SUMMARY BELOW]"

	skillPrefix           = "[IMPORTANT: The user has invoked the "
	skillSingleMarker     = "The full skill content is loaded below.]"
	skillInstruction      = "The user has provided the following instruction alongside the skill invocation: "
	skillRuntimeNote      = "\n\n[Runtime note:"
	skillBundleMarker     = " skill bundle,"
	skillBundleUser       = "\nUser instruction: "
	skillBundleFirstSkill = "\n\n[Loaded as part of the "
)

var skillNameRE = regexp.MustCompile(`^` + regexp.QuoteMeta(skillPrefix) + `"([^"]*)"`)

// titleRunes is the length of a title taken from a prompt.
const titleRunes = 100

// toolArgKeys are the tool-call arguments that best describe a call, in order.
var toolArgKeys = []string{"command", "path", "file_path", "pattern", "query", "url", "code", "question", "name", "target"}

// toolOutputKeys are the keys of a JSON tool result that hold its text, in order.
var toolOutputKeys = []string{"output", "content", "output_preview", "result", "error", "message", "summary"}

// convert turns one display row into zero or more messages. bad reports a
// part of the row that could not be parsed (counted as skipped).
func convert(r messageRow) (out []model.Message, bad bool) {
	if r.summary || r.displayKind == "hidden" {
		return nil, false
	}
	at := unix(r.timestamp)
	add := func(role model.Role, kind model.Kind, text string) {
		out = append(out, model.Message{Role: role, Kind: kind, At: at, Text: text})
	}
	switch r.role {
	case "user":
		text, ok := stripCompaction(contentText(r.content))
		if !ok {
			return nil, false
		}
		switch {
		case r.displayKind == "async_delegation_complete":
			add(model.RoleTool, model.KindToolOutput, truncate(text, model.MaxToolOutput))
		case strings.HasPrefix(text, skillPrefix):
			if s := describeSkill(text); s != "" {
				add(model.RoleUser, model.KindText, s)
			}
		case r.displayKind == "model_switch" || isSystemNote(text):
			add(model.RoleSystem, model.KindText, text)
		default:
			add(model.RoleUser, model.KindText, text)
		}
	case "assistant":
		if text, ok := stripCompaction(contentText(r.content)); ok {
			add(model.RoleAssistant, model.KindText, text)
		}
		calls, ok := toolCalls(r.toolCalls)
		if !ok {
			bad = true
		}
		for _, c := range calls {
			add(model.RoleAssistant, model.KindToolCall, c)
		}
	case "tool":
		if text := toolOutput(r.content); text != "" {
			add(model.RoleTool, model.KindToolOutput, truncate(text, model.MaxToolOutput))
		}
	}
	return out, bad
}

// contentText decodes a content column: plain text, or Hermes' JSON-encoded
// list of parts (only text parts are kept).
func contentText(s string) string {
	rest, ok := strings.CutPrefix(s, contentJSONPrefix)
	if !ok {
		return strings.TrimSpace(s)
	}
	var v any
	if json.Unmarshal([]byte(rest), &v) != nil {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(partsText(v))
}

// partsText collects the text of a multimodal content value.
func partsText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, p := range x {
			if t := strings.TrimSpace(partsText(p)); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if t, ok := x["text"].(string); ok {
			return t
		}
		if c, ok := x["content"]; ok {
			return partsText(c)
		}
	}
	return ""
}

// stripCompaction removes compaction scaffolding: a pure summary carrier is
// dropped (ok=false), a carrier followed by the user's message keeps that
// message, and a merged carrier keeps the prior text before the summary.
func stripCompaction(text string) (string, bool) {
	if strings.HasPrefix(text, compactionPrefix) || strings.HasPrefix(text, legacySummaryPrefix) {
		i := strings.Index(text, summaryEndMarker)
		if i < 0 {
			return "", false
		}
		text = strings.TrimSpace(text[i+len(summaryEndMarker):])
	} else if i := strings.Index(text, mergedDelimiter); i >= 0 {
		after := strings.TrimSpace(text[i+len(mergedDelimiter):])
		if strings.HasPrefix(after, compactionPrefix) || strings.HasPrefix(after, legacySummaryPrefix) {
			prior := strings.TrimSpace(text[:i])
			prior = strings.TrimSpace(strings.TrimPrefix(prior, mergedPriorHeader))
			text = prior
		}
	}
	return text, text != ""
}

// describeSkill renders slash-skill scaffolding as the user typed it:
// "/name — instruction", or "/name" for a bare invocation (Hermes'
// describe_skill_invocation).
func describeSkill(text string) string {
	name := ""
	if m := skillNameRE.FindStringSubmatch(text); m != nil {
		name = strings.TrimSpace(m[1])
	}
	label := name
	if !strings.HasPrefix(name, "/") {
		label = "/" + name
	}
	var instruction string
	switch {
	case strings.Contains(text, skillBundleMarker):
		instruction = cutAfter(text, skillBundleUser, skillBundleFirstSkill, strings.Index)
	case strings.Contains(text, skillSingleMarker):
		instruction = cutAfter(text, skillInstruction, skillRuntimeNote, strings.LastIndex)
	}
	instruction = strings.Join(strings.Fields(instruction), " ")
	switch {
	case instruction != "" && name != "":
		return label + " — " + instruction
	case instruction != "":
		return instruction
	case name != "":
		return label
	}
	return ""
}

// cutAfter returns the text between marker (found with find) and stop.
func cutAfter(s, marker, stop string, find func(string, string) int) string {
	i := find(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	if j := strings.Index(rest, stop); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// isSystemNote reports a note Hermes injects as a user row ("[System: …]").
func isSystemNote(text string) bool {
	return strings.HasPrefix(text, "[System:") || strings.HasPrefix(text, "[SYSTEM:")
}

// toolCalls renders an assistant row's tool_calls JSON, one line per call:
// "name: <command, path, query, … or the arguments JSON>". ok=false when the
// column is not valid JSON.
func toolCalls(raw string) (out []string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || raw == "[]" {
		return nil, true
	}
	var calls []struct {
		Name     string `json:"name"`
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal([]byte(raw), &calls) != nil {
		return nil, false
	}
	for _, c := range calls {
		name := c.Function.Name
		if name == "" {
			name = c.Name
		}
		out = append(out, toolSummary(name, c.Function.Arguments))
	}
	return out, true
}

// toolSummary describes one call. Arguments may be a JSON object or a string
// holding one (the OpenAI wire format Hermes stores).
func toolSummary(name string, args json.RawMessage) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	args = bytes.TrimSpace(args)
	var s string
	if json.Unmarshal(args, &s) == nil {
		args = bytes.TrimSpace([]byte(s))
	}
	if len(args) == 0 || string(args) == "null" || string(args) == "{}" {
		return name
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(args, &fields) == nil {
		for _, k := range toolArgKeys {
			var v string
			if json.Unmarshal(fields[k], &v) == nil && strings.TrimSpace(v) != "" {
				return name + ": " + firstRunes(strings.TrimSpace(v), 1000)
			}
		}
		var compact bytes.Buffer
		if json.Compact(&compact, args) == nil {
			args = compact.Bytes()
		}
	}
	return name + ": " + firstRunes(string(args), 200)
}

// toolOutput extracts the text of a tool result: Hermes tools return JSON
// objects ({"output": …, "exit_code": …}, {"content": …}, {"error": …});
// anything else is kept as it is.
func toolOutput(content string) string {
	text := contentText(content)
	if !strings.HasPrefix(text, "{") {
		return text
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &fields) != nil {
		return text
	}
	for _, k := range toolOutputKeys {
		var v string
		if json.Unmarshal(fields[k], &v) == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return text
}

// title picks the conversation title: the best stored title along the chain
// (user > agent-generated > derived; the earliest on ties), else the first
// prompt, else the gateway display name.
func title(chain []*sessionRow, msgs []model.Message) (string, model.TitleSource) {
	best, bestRank := "", -1
	for _, s := range chain {
		t := cleanTitle(s.title)
		if t == "" {
			continue
		}
		rank := 0 // derived (e.g. "Title #2" of a continuation)
		switch {
		case s.titleSourceNull || s.titleSource == "user":
			rank = 2 // NULL: a manual /title from before provenance existed
		case s.titleSource == "llm":
			rank = 1
		}
		if rank > bestRank {
			best, bestRank = t, rank
		}
	}
	if bestRank == 2 {
		return best, model.TitleUser
	}
	if bestRank >= 0 {
		return best, model.TitleAgent
	}
	for _, m := range msgs {
		if m.Role == model.RoleUser && m.Kind == model.KindText {
			if t := promptTitle(m.Text); t != "" {
				return t, model.TitlePrompt
			}
		}
	}
	for _, s := range chain {
		if t := cleanTitle(s.displayName); t != "" {
			return t, model.TitleNone
		}
	}
	return "", model.TitleNone
}

// cleanTitle collapses whitespace.
func cleanTitle(s string) string { return strings.Join(strings.Fields(s), " ") }

// promptTitle collapses whitespace and keeps the first titleRunes runes.
func promptTitle(s string) string {
	out := make([]rune, 0, titleRunes)
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = len(out) > 0
			continue
		}
		if len(out) >= titleRunes || space && len(out)+1 >= titleRunes {
			break
		}
		if space {
			out = append(out, ' ')
			space = false
		}
		out = append(out, r)
	}
	return string(out)
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// firstRunes returns at most n runes of s.
func firstRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
