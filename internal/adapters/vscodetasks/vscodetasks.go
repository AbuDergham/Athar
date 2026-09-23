// Package vscodetasks reads the per-task JSON folders of Cline-family VS Code
// extensions (Cline and legacy Kilo Code) in every VS Code-family host, plus
// the sessions of Cline's SDK bundle and CLI.
//
// Layout (verified on real files, structure only: Cline in VS Code, legacy
// Kilo Code in Antigravity):
//
//	%APPDATA%\<Host>\User\globalStorage\<ext>\tasks\<id>\
//	    api_conversation_history.json  [{role, content[], ts}] (what the model saw)
//	    ui_messages.json               [{ts, type ask|say, ask|say, text, ...}] (the chat UI)
//	    task_metadata.json, context_history.json, checkpoints\ (not read)
//	    history_item.json              (newer Roo-derived builds: the index entry)
//	%APPDATA%\<Host>\User\globalStorage\saoudrizwan.claude-dev\state\taskHistory.json
//	    Cline index [{id, ts, task, cwdOnTaskInitialization, ...}]
//	%APPDATA%\<Host>\User\globalStorage\state.vscdb  ItemTable[<ext>] = {taskHistory:[{id, ts, task, workspace}]}
//	    Kilo legacy index (and old Cline builds); read from a temp copy only.
//	%USERPROFILE%\.cline\data\tasks\<id>\ (pre-SDK CLI/JetBrains, same task layout)
//	%USERPROFILE%\.cline\data\sessions\<id>\<id>.json + <id>.messages.json (SDK bundle)
//
// <Host> is any folder under %APPDATA% (Code, Code - Insiders, VSCodium,
// Cursor, Antigravity, Windsurf, Trae, ...); it is passed to Read as
// Source.Extra["host"]. <ext> is saoudrizwan.claude-dev (Cline) or
// kilocode.kilo-code (Kilo Code before 7.x; 7.x keeps sessions in kilo.db,
// read by the opencode package, and never migrated the legacy tasks).
//
// Rules:
//   - One Source per task folder; its fingerprint covers the chat files (and
//     history_item.json when present).
//     The index is looked up in Read (and cached per index fingerprint), so a
//     frozen task folder never costs a state.vscdb copy after its first read.
//   - Messages come from api_conversation_history.json. Cline's XML tool
//     protocol keeps tool calls inside assistant text (<execute_command>...);
//     they become tool calls, and attempt_completion / ask_followup_question /
//     plan_mode_respond become assistant text. Native tool_use / tool_result
//     blocks (Kilo) are handled too. User text is what sits in <task>,
//     <feedback>, <answer> or <user_message>; environment details, attached
//     file contents, "[tool] Result:" headers and system notices are not
//     user text. Thinking and reasoning are never read.
//   - When api_conversation_history.json is missing or empty, the chat UI log
//     (ui_messages.json) gives the prompts and answers (coverage partial).
//   - Times: api ts and ui_messages ts (epoch ms). Title: the index "task" (the
//     first prompt), else the first user prompt. Cwd: the index
//     cwdOnTaskInitialization / workspace, else the first environment details.
//   - Whole-file JSON modified within the last 5 s is retried on the next sync.
package vscodetasks

import (
	"sync"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
)

const (
	clineID  = "cline"
	kiloID   = "kilo"
	clineExt = "saoudrizwan.claude-dev"
	kiloExt  = "kilocode.kilo-code"

	// settle: whole-file JSON modified this recently may be mid-write (spec §4.3).
	settle = 5 * time.Second

	// Source.Extra keys and values.
	extraKind = "kind"
	extraHost = "host"
	kindTask  = "task"
	kindSDK   = "sdk"
	// hostCLI is the host name of tasks kept by the Cline CLI / JetBrains plugin.
	hostCLI = "cline-cli"
)

// flavor is what differs between the Cline and the Kilo legacy adapters.
type flavor struct {
	id, name, ext string
	sdk           bool // also read ~\.cline (Cline only)
	caps          adapters.Caps
}

var clineFlavor = flavor{
	id: clineID, name: "Cline", ext: clineExt, sdk: true,
	// Cline has no link or command that opens a given task: Athar opens the
	// project in VS Code and the user picks the task from Cline's history.
	caps: adapters.Caps{App: &adapters.AppCommand{
		Name:    "VS Code",
		Program: "code",
		Args:    []string{"{cwd}"},
		Candidates: []string{
			`%LOCALAPPDATA%\Programs\Microsoft VS Code\bin\code.cmd`,
			`%ProgramFiles%\Microsoft VS Code\bin\code.cmd`,
		},
	}},
}

var kiloFlavor = flavor{
	id: kiloID, name: "Kilo Code (legacy)", ext: kiloExt,
	// Legacy tasks can only be picked from the History view of the extension;
	// on this machine they live in Antigravity (spec §6.3).
	caps: adapters.Caps{App: &adapters.AppCommand{
		Name:       "Antigravity",
		Program:    "antigravity",
		Args:       []string{"{cwd}"},
		Candidates: []string{`%LOCALAPPDATA%\Programs\Antigravity\bin\antigravity.cmd`},
	}},
}

// adapter reads one flavor. It caches parsed task indexes by fingerprint.
type adapter struct {
	f   flavor
	now func() time.Time // nil = time.Now; tests move the clock

	mu    sync.Mutex
	index map[string]indexCache // by index file path
}

// NewCline returns the Cline adapter (id "cline").
func NewCline() adapters.Adapter { return &adapter{f: clineFlavor} }

// NewKiloLegacy returns the adapter for Kilo Code's legacy (<= 5.x) task
// folders. Its id is "kilo", shared with the kilo.db adapter: legacy task ids
// and kilo.db session ids never collide.
func NewKiloLegacy() adapters.Adapter { return &adapter{f: kiloFlavor} }

func (a *adapter) ID() string   { return a.f.id }
func (a *adapter) Name() string { return a.f.name }
func (a *adapter) Version() int { return 1 }

// Caps returns the app that opens a task's project (no terminal resume).
func (a *adapter) Caps() adapters.Caps { return a.f.caps }

func (a *adapter) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}
