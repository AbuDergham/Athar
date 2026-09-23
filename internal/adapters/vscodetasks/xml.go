package vscodetasks

import (
	"regexp"
	"strings"
)

// segment is a piece of assistant text: plain text, or one XML tool call.
type segment struct {
	text string // plain text (tool == "")
	tool string // tool name
	body string // the XML between <tool> and </tool>
}

// xmlTools are the tool names of Cline's (and Roo/Kilo's) XML tool protocol.
var xmlTools = map[string]bool{
	"execute_command": true, "read_file": true, "write_to_file": true, "replace_in_file": true,
	"apply_diff": true, "insert_content": true, "search_and_replace": true, "edit_file": true,
	"write_file": true, "apply_patch": true, "search_files": true, "list_files": true,
	"list_code_definition_names": true, "codebase_search": true, "browser_action": true,
	"use_mcp_tool": true, "access_mcp_resource": true, "load_mcp_documentation": true,
	"ask_followup_question": true, "attempt_completion": true, "plan_mode_respond": true,
	"act_mode_respond": true, "new_task": true, "switch_mode": true, "fetch_instructions": true,
	"update_todo_list": true, "condense": true, "summarize_task": true, "report_bug": true,
	"new_rule": true, "web_fetch": true, "web_search": true, "focus_chain": true,
	"run_slash_command": true, "generate_image": true, "use_subagents": true,
}

// droppedTags are sections of assistant text that are not kept: reasoning,
// and Cline's task_progress checklist (repeated in every message).
var droppedTags = map[string]bool{"thinking": true, "task_progress": true}

// notTools are underscore tag names that are never tool calls.
var notTools = map[string]bool{
	"environment_details": true, "file_content": true, "final_file_content": true,
	"user_message": true, "explicit_instructions": true, "task_progress": true,
}

var (
	tagNameRe     = regexp.MustCompile(`^<([a-z][a-z0-9_]{0,63})>`)
	genericToolRe = regexp.MustCompile(`^[a-z]+(?:_[a-z0-9]+)+$`)
)

// openTag returns the name of a plain opening tag "<name>" at s[p].
func openTag(s string, p int) (string, bool) {
	end := p + 66
	if end > len(s) {
		end = len(s)
	}
	m := tagNameRe.FindStringSubmatch(s[p:end])
	if m == nil {
		return "", false
	}
	return m[1], true
}

// atLineStart reports whether only spaces or tabs precede s[p] on its line.
func atLineStart(s string, p int) bool {
	for i := p - 1; i >= 0; i-- {
		switch s[i] {
		case ' ', '\t':
			continue
		case '\n', '\r':
			return true
		default:
			return false
		}
	}
	return true
}

// parseAssistant splits assistant text into plain text and XML tool calls,
// dropping <thinking> and <task_progress> sections. A known tool tag counts
// when it is closed or starts a line; another underscore tag counts only when
// both hold, so prose and HTML are left alone. An unterminated tool (a cut-off
// stream) runs to the end of the text.
func parseAssistant(s string) []segment {
	var segs []segment
	var text strings.Builder
	flushText := func() {
		if strings.TrimSpace(text.String()) != "" {
			segs = append(segs, segment{text: text.String()})
		}
		text.Reset()
	}
	i := 0
	for i < len(s) {
		j := strings.IndexByte(s[i:], '<')
		if j < 0 {
			text.WriteString(s[i:])
			break
		}
		p := i + j
		name, ok := openTag(s, p)
		bodyStart := p + len(name) + 2
		closeTag := "</" + name + ">"
		closed := ok && strings.Contains(s[bodyStart:], closeTag)
		tool := ok && !notTools[name] &&
			((xmlTools[name] && (closed || atLineStart(s, p))) ||
				(genericToolRe.MatchString(name) && closed && atLineStart(s, p)))
		if !ok || (!tool && !droppedTags[name]) {
			text.WriteString(s[i : p+1])
			i = p + 1
			continue
		}
		text.WriteString(s[i:p])
		body, next := s[bodyStart:], len(s)
		if end := strings.Index(s[bodyStart:], closeTag); end >= 0 {
			body, next = s[bodyStart:bodyStart+end], bodyStart+end+len(closeTag)
		}
		if tool {
			flushText()
			segs = append(segs, segment{tool: name, body: body})
		}
		i = next
	}
	flushText()
	return segs
}

// longParams may contain their own closing tag inside (file contents,
// diffs, answers), so they end at the LAST closing tag, as in Cline's parser.
var longParams = map[string]bool{"content": true, "diff": true, "result": true, "response": true, "context": true}

// toolParams parses the top-level <param>value</param> elements of a tool body.
// Values are trimmed of surrounding newlines. The first occurrence wins.
func toolParams(body string) map[string]string {
	params := map[string]string{}
	i := 0
	for i < len(body) {
		j := strings.IndexByte(body[i:], '<')
		if j < 0 {
			break
		}
		p := i + j
		name, ok := openTag(body, p)
		if !ok {
			i = p + 1
			continue
		}
		start := p + len(name) + 2
		closeTag := "</" + name + ">"
		var end int
		if longParams[name] {
			end = strings.LastIndex(body[start:], closeTag)
		} else {
			end = strings.Index(body[start:], closeTag)
		}
		if end < 0 {
			if _, dup := params[name]; !dup {
				params[name] = strings.Trim(body[start:], "\r\n")
			}
			break
		}
		if _, dup := params[name]; !dup {
			params[name] = strings.Trim(body[start:start+end], "\r\n")
		}
		i = start + end + len(closeTag)
	}
	return params
}
