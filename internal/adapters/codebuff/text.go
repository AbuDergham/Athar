package codebuff

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/AbuDergham/Athar/internal/model"
)

// toolArgKeys are the input fields that name what a tool call worked on, in
// order of preference (spec §5.1: tool name, command line and file paths).
var toolArgKeys = []string{"command", "path", "file_path", "filePath", "paths", "pattern", "query", "url", "agents"}

// toolCallText is "name: <command | path(s) | pattern | query | url | spawned
// agents | first 200 runes of the input JSON>", at most model.MaxToolOutput bytes.
func toolCallText(name string, input []byte) string {
	if name == "" {
		name = "tool"
	}
	arg := toolArg(input)
	if arg == "" {
		return name
	}
	return truncate(name+": "+arg, model.MaxToolOutput)
}

func toolArg(input []byte) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) == nil {
		for _, k := range toolArgKeys {
			v, ok := fields[k]
			if !ok {
				continue
			}
			var s string
			if k == "agents" {
				s = spawnedAgents(v)
			} else {
				s = stringOrList(v)
			}
			if s != "" {
				return s
			}
		}
	}
	js := compactJSON(input)
	if js == "" || js == "{}" || js == "null" || js == `""` || js == "[]" {
		return ""
	}
	return firstRunes(js, 200)
}

// stringOrList returns a JSON string, or the strings of a JSON array joined by spaces.
func stringOrList(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	var parts []string
	for _, item := range list {
		if json.Unmarshal(item, &s) == nil && strings.TrimSpace(s) != "" {
			parts = append(parts, strings.TrimSpace(s))
		}
	}
	return strings.Join(parts, " ")
}

// spawnedAgents renders spawn_agents' "agents" list as "type: prompt; type".
func spawnedAgents(raw json.RawMessage) string {
	var agents []map[string]json.RawMessage
	if json.Unmarshal(raw, &agents) != nil {
		return ""
	}
	var parts []string
	for _, a := range agents {
		typ := ""
		for _, k := range []string{"agent_type", "agentType", "agent", "name"} {
			if typ = stringOrList(a[k]); typ != "" {
				break
			}
		}
		if typ == "" {
			typ = "agent"
		}
		if p := stringOrList(a["prompt"]); p != "" {
			typ += ": " + p
		}
		parts = append(parts, typ)
	}
	return strings.Join(parts, "; ")
}

// outputText renders a tool or subagent result: a string as is, an array of
// result parts ({type: json|text|media, value|text}) part by part, anything
// else as compact JSON. At most model.MaxToolOutput bytes.
func outputText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return truncate(strings.TrimSpace(s), model.MaxToolOutput)
	}
	if parts, ok := resultParts(raw); ok {
		var sb strings.Builder
		for _, p := range parts {
			t := strings.TrimSpace(p.text())
			if t == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(t)
			if sb.Len() >= model.MaxToolOutput {
				break
			}
		}
		return truncate(sb.String(), model.MaxToolOutput)
	}
	return truncate(compactJSON(raw), model.MaxToolOutput)
}

// resultPart is one element of a tool result's content array.
type resultPart struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
	Text  string          `json:"text"`
}

func (p resultPart) text() string {
	switch p.Type {
	case "media", "image":
		return "[image]"
	case "text":
		if p.Text != "" {
			return p.Text
		}
	}
	var s string
	if json.Unmarshal(p.Value, &s) == nil {
		return s
	}
	if len(bytes.TrimSpace(p.Value)) > 0 {
		return compactJSON(p.Value)
	}
	return p.Text
}

// resultParts decodes raw as a result-part array: every element must be an
// object with a known "type".
func resultParts(raw json.RawMessage) ([]resultPart, bool) {
	var parts []resultPart
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return nil, false
	}
	for _, p := range parts {
		switch p.Type {
		case "json", "text", "media", "image":
		default:
			return nil, false
		}
	}
	return parts, true
}

// compactJSON returns raw without insignificant whitespace (raw itself when it
// is not valid JSON). HTML characters are not escaped.
func compactJSON(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}

// cleanTitle collapses whitespace.
func cleanTitle(s string) string { return strings.Join(strings.Fields(s), " ") }

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
