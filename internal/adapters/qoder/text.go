package qoder

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// systemReminder matches blocks the agent injects into user text.
var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// wrapperPrefixes mark user text that the agent wrote on the user's behalf
// (slash commands, their output, caveats), not a real prompt.
var wrapperPrefixes = []string{
	"<command-name>", "<command-message>", "<command-args>", "<local-command-",
	"<system-reminder>", "<task-notification>", "<bash-stdout>", "<bash-stderr>",
	"Caveat: The messages below", "[Request interrupted",
}

// cleanUserText removes injected reminder blocks and returns "" for wrappers.
func cleanUserText(s string) string {
	s = strings.TrimSpace(systemReminder.ReplaceAllString(s, ""))
	for _, p := range wrapperPrefixes {
		if strings.HasPrefix(s, p) {
			return ""
		}
	}
	return s
}

// promptTitle collapses whitespace and keeps the first 100 runes.
func promptTitle(s string) string {
	return firstRunes(strings.Join(strings.Fields(s), " "), 100)
}

// toolInputKeys are tried in order for the one-line summary of a tool call.
var toolInputKeys = []string{"command", "file_path", "pattern", "path", "url", "query"}

// toolCallText returns "name: detail", where detail is the command line or
// file path from the input JSON, else the first 200 runes of the input.
func toolCallText(name, input string) string {
	if name = strings.TrimSpace(name); name == "" {
		name = "tool"
	}
	detail := ""
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err == nil {
		for _, k := range toolInputKeys {
			if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
				detail = v
				break
			}
		}
		if detail == "" && len(args) > 0 {
			detail = input
		}
	} else {
		detail = input
	}
	detail = firstRunes(strings.Join(strings.Fields(detail), " "), 200)
	if detail == "" {
		return name
	}
	return name + ": " + detail
}

// rawText returns a JSON string's value, or the raw JSON for other values.
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// firstRunes returns at most n runes of s.
func firstRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// truncateBytes returns at most n bytes of s, cut on a rune boundary.
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
