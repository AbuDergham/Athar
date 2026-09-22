// Package model holds the agent-independent representation of a conversation
// that every adapter produces and the archive stores.
package model

import "time"

// Coverage says how much of a conversation Athar could read.
type Coverage string

const (
	CoverageFull    Coverage = "full"    // all user and assistant messages
	CoveragePartial Coverage = "partial" // some text (e.g. plans, a paged or log-like store)
	CoveragePrompts Coverage = "prompts" // only the user's prompts (prompt logs)
	CoverageTitles  Coverage = "titles"  // title/date/project, maybe a few previews
	CoverageMeta    Coverage = "meta"    // id and dates only (encrypted stores)
)

// Rank orders coverages from least to most complete; used when merging sources.
func (c Coverage) Rank() int {
	switch c {
	case CoverageFull:
		return 4
	case CoveragePartial:
		return 3
	case CoveragePrompts:
		return 2
	case CoverageTitles:
		return 1
	default:
		return 0
	}
}

// TitleSource records where Session.Title came from.
type TitleSource string

const (
	TitleUser   TitleSource = "user"   // the user renamed it in the agent
	TitleAgent  TitleSource = "agent"  // the agent generated it
	TitlePrompt TitleSource = "prompt" // first real user prompt
	TitleNone   TitleSource = "none"
)

// Origin records how a session entered the archive.
type Origin string

const (
	OriginLive       Origin = "live"
	OriginAgentsView Origin = "import:agentsview"
	OriginPSM        Origin = "import:psm"
	OriginPromptLog  Origin = "prompt-log"
)

// Role of a message author.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// Kind of message content.
type Kind string

const (
	KindText       Kind = "text"
	KindToolCall   Kind = "tool_call"   // tool name + command line / file paths
	KindToolOutput Kind = "tool_output" // adapters keep at most MaxToolOutput bytes
)

// MaxToolOutput is the number of bytes of a tool output an adapter keeps.
const MaxToolOutput = 2048

// Message is one entry of a conversation, in order.
type Message struct {
	Role Role
	Kind Kind
	At   time.Time // UTC; zero when unknown
	Text string
}

// Session is one conversation as read from one source.
type Session struct {
	Agent    string // adapter ID, e.g. "claude"
	NativeID string // the agent's own id, without any "<agent>:" prefix

	// Cwd is the exact launch directory, verbatim from the agent. For agents
	// that file sessions by their starting folder (Claude, Qwen, Qoder, Neovate,
	// Command Code) this is the FIRST cwd record.
	Cwd string

	Title       string
	TitleSource TitleSource
	StartedAt   time.Time // UTC
	UpdatedAt   time.Time // UTC, last activity
	Coverage    Coverage
	Origin      Origin

	// Root is the agent data root when it is not the default one
	// (e.g. %USERPROFILE%\.claude-local for Claude); "" means default.
	Root string

	// Machine is set by the importers for sessions recorded on another PC; "" = this PC.
	Machine string

	Messages []Message
}
