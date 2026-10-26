# Athar — Design Spec

- **Date:** 2026-09-23 (rev. 2, after adversarial review)
- **Status:** Approved by owner section by section; owner authorized autonomous implementation and publishing
- **Name:** Athar (أثر, "trace"), the trace of your work with AI agents
- **Repo:** `github.com/AbuDergham/Athar` (public source) + prebuilt Windows `athar.exe` in Releases
- **Supersedes:** `Resume-Session.cmd` (a Claude Code–only session picker)

## 1. Purpose

The owner uses many AI agents (about 25 tools on one Windows PC) and forgets which agent, which project and when a conversation happened. Athar is a personal "Swiss army knife": type a vague memory, find the conversation with any agent, read it, and get back into it.

### Principles

1. **Coverage beats features.** Every agent that has local history on the owner's PC is supported in v1.
2. **Never lose history.** Agents delete their own transcripts (Claude Code and Gemini CLI default to 30 days). Athar keeps its own archive and never removes archived sessions or messages on its own.
3. **Simple.** One screen, a search box and a list. No daemon, no dashboards, no accounts.
4. **Read-only toward agents.** Athar never writes, creates, locks against deletion, moves or deletes any file in an agent's folders. It never runs an agent binary except when the user resumes a session.
5. **Local and private.** No telemetry. The only network traffic is the update check (and the download after the user confirms an update). It can be disabled.
6. **Arabic-aware search.** Search works for Arabic the way people type it (with or without ال, hamza variants, diacritics), plus English.

### Non-goals (v1)

- Web chats (claude.ai, ChatGPT, Gemini web, Perplexity): server-side, no export importers in v1.
- Reading other machines, cloud sync, WSL distros. (The archive is portable, see §4.6, but Athar only reads the PC it runs on.)
- Analytics, cost/token dashboards, AI summaries, embeddings/semantic search, MCP server, local HTTP API.
- Converting a session from one agent to another; editing/renaming/deleting sessions in agent stores.
- Adapters for agents with no data on the owner's PC (Pi, Goose, Crush, Copilot CLI, Factory droid, Kiro, Amp, Aider, Roo Code, Cursor Agent CLI). They can be contributed later as one adapter file plus fixtures.
- Subagent / workflow transcripts: skipped entirely (not archived, not indexed, not listed). The parent session keeps its own tool call and the result the subagent returned.

## 2. User experience

### 2.1 Launch

- Double-click `athar.exe` (or run `athar` in a terminal). Windows opens it in the default terminal (Windows Terminal on the owner's PC).
- The main screen appears immediately from the archive; sync runs in the background (§4.3). On the very first run the list shows: *"First run: reading your agents' history (about 1–2 minutes). Results appear as each source finishes."*
- The UI chrome (labels, keys, messages) is **English**. Conversation content is shown as-is, including Arabic.
- If `athar.exe` runs from a folder that is not writable or is under `%TEMP%` (e.g. double-clicked inside a zip), Athar does not start. It prints: *"Athar keeps its archive next to athar.exe, but <folder> is not writable (or is temporary). Move athar.exe to a folder you own, for example D:\Tools\Athar, or run it with --data-dir <path>."* and waits for a key. There is no automatic fallback location.

### 2.2 Main screen

```
┌──────────────────────────────────────── 212 sessions · 14 agents ┐
│ Search: telegram video█                  [All agents] [Any time]  │
├────────────────────────────────────┬──────────────────────────────┤
│ > Claude      3d   TelegramVideos    │ Claude · D:\Projects\Telegr… │
│   "…download every video from the…"  │ started 2026-09-20 21:30 ·   │
│   Codex       7mo  Bot               │ last 2026-09-20 23:05 · 42 · │
│   "…telegram bot: upload the video…" │ full                         │
│ Partial matches (1 of 2 words)       │ You: …                       │
│   Antigravity 4mo  Scraper           │ Claude: …                    │
│   "Telegram scraper plan" (titles)   │                              │
├────────────────────────────────────┴──────────────────────────────┤
│ ↑↓ move  Enter actions  Tab agent  Ctrl+T time  F2 sources  Esc   │
└────────────────────────────────────────────────────────────────────┘
```

- **Search-as-you-type** over full conversation text (§5). The search box always has focus; letter keys always type.
- **Result row:** agent, relative age of last activity, project name, and the title or a snippet around the match. Markers: `●` open in another window (§6.5), `🗄` archive-only (the agent deleted its copy), coverage word when not `full` (e.g. `(titles)`, `(prompts)`).
- **Preview pane:** first line `Agent · full project path · started <local datetime> · last active <local datetime> · <n> messages · <coverage>`; then the matching message with the previous and next message dimmed ("sandwich" view). With an empty query it shows the start of the session.
- **Empty query = timeline:** all sessions newest first, one header per calendar day that has sessions (`Today · 3`, `Yesterday · 5`, then e.g. `Mon 21 Sep 2026 · 2`). `Ctrl+↑`/`Ctrl+↓` jump to the previous/next header; `Home`/`End` go to top/bottom.
- **Time:** stored in UTC, shown and grouped in the PC's local time zone. Order, day headers, relative age and the time filter all use `UpdatedAt` (last activity).
- **Filters (only two):** `Tab` cycles the agent filter (All → each agent that has sessions); `Ctrl+T` cycles the time range (Any time, Today, 7 days, 30 days, 12 months).
- **Titles, in order of preference:** the user's own name in the agent (Claude `custom-title`/`agent-name`, Qwen `custom_title`, Copilot `customTitle`, …), the agent's AI title (Claude `ai-title`, Gemini `summary`, OpenCode/Codex/Continue `title`, Antigravity summary title), then the first real user prompt.

### 2.3 Action menu (Enter)

Enter never launches anything by itself. It opens a small menu. The first applicable item is highlighted; `↑↓` + `Enter` works, and each item also has a key. Each key accepts both its Latin letter and the character the same physical key produces on the Arabic (101) layout, so the menu works without switching layout.

| Key | Arabic key | Action | Shown when |
|---|---|---|---|
| `R` | `ق` | **Resume.** CLI agents: resume in a new tab. GUI agents (Cursor, VS Code, Antigravity, Trae, LM Studio): open the project in the app and show *"Opened <project> in <App>. Pick «<title>» from its chat history."* | the agent has a resume or app command, the session has a live source on this machine (§4.6), and it is not a live background session (§6.5) |
| `F` | `ب` | **Fork:** resume as a new session id | the session is live (`●`) and the agent supports forking (Claude, Command Code); listed first when live |
| `!` | `!` | **Resume without permission prompts** — runs R or F (whichever applies) with the agent's flag added, at once (the y/N confirmation was removed in v1.2 at the owner's request) | R or F is shown and the agent has such a flag (§6.3) |
| `O` | `خ` | **Read** the full conversation in the browser (§2.4) | always |
| `C` | `ؤ` | **Copy** the resume/open command (PowerShell syntax, §6.4) | a command exists |
| `D` | `ي` | **Open the project folder** in Explorer | the folder exists |

**Shift+Enter on the list** runs the `!` action directly: it resumes (or, for a live session, forks) the selected session with the agent's skip-permissions flag in a new tab, without opening the menu (added in v1.1 at the owner's request). When `!` does not apply, the menu opens with the reason.

When a session is live and interactive, `R` shows a warning line: *"Open in another window: resuming here means two processes write one transcript."*

### 2.4 Reading page (`O`)

- Athar renders the conversation to a self-contained HTML file in `%TEMP%\athar\read\` and opens it in the default browser. No network resources.
- Each message uses `dir="auto"`, so Arabic renders right-to-left with correct shaping. Header: agent, project path, dates, coverage.
- The page opens scrolled to the message shown in the preview (that message has `id="hit"`; an inline script calls `scrollIntoView()`). Every message that matched the query gets a coloured side bar, and a small `hit 2/7 ▲▼` control jumps between them.
- Tool calls are collapsed (`<details>`). Tool outputs over 20 KB are cut with *"(N KB more, truncated)"*. Images appear as `[image]`. Thinking blocks are not shown.

### 2.5 Sources screen (`F2`)

No search box here, so letter keys are commands.

```
Claude Code      36 sessions   full        resume ✓    last read 2 min ago
Codex            18 sessions   full        resume ✓
Antigravity IDE  31 sessions   titles+     resume via app
Windsurf          3 sessions   meta        —           encrypted; app removed
AgentsView index  N sessions   imported    read only
...
Archive: D:\Tools\Athar\athar-data\archive.db (312 MB) · last sync 2026-09-23 03:10
Update v1.1.0 available — press U for what's new
Keys & search: ↑↓ PgUp PgDn Home End · Enter actions · Tab agent · Ctrl+T time ·
  "exact phrase" · words may be in different messages · words under 3 letters match titles and projects only
```

- Per-source errors (unreadable file, torn copy, parse errors with counts, "source shrank, archived copy kept") are shown here, never as pop-ups.
- `U` opens the release notes of an available update with `I` install / `Esc` back (§9).

### 2.6 Arabic in the terminal

Windows terminals render Arabic poorly (no bidi reordering, sometimes no shaping). v1 plan:

1. Milestone 1 includes a spike on the owner's Windows Terminal: rows and preview text with Arabic, and typing, moving the cursor through and deleting an Arabic query in the search box; also the column widths of `●`, `🗄`, `…`.
2. If raw text is unreadable, add a display-only transform in this order: (1) shape to Arabic presentation forms in logical order (joining tables; a small internal table or `github.com/AbdullahDiaa/garabic` without its RTL reversal); (2) wrap to the pane width; (3) reorder each visual line with `golang.org/x/text/unicode/bidi`. The spike picks the default; `[display] arabic = "shaped" | "raw"` in `athar.toml` overrides it.
3. The browser reading page (§2.4) is always correct regardless.

### 2.7 Keys

Main screen: `↑↓` `PgUp` `PgDn` `Home` `End` move · `Ctrl+↑/↓` jump between day headers · `Enter` action menu · `Shift+Enter` resume without permission prompts · `Tab` agent filter · `Ctrl+T` time filter · `F2` sources & help · `Esc` clears the search, or quits when the search is empty · `Ctrl+C` quits.

### 2.8 Empty and failed states

- No sessions at all: *"No agent history found. F2 shows where Athar looked."*
- No results with a filter on: *"No matches in Codex · 7 days. 4 matches with all agents and any time — press Tab / Ctrl+T."*
- No results at all: *"No matches. Try fewer or shorter words."*
- Archive cannot be opened or fails `quick_check`: show the path and the SQLite error, then offer the newest backup (§4.8). Never create an empty archive over a damaged one.
- Fatal startup error: print it, exit with a non-zero code, and wait for a key before closing (double-click windows would otherwise vanish).
- Another Athar window is syncing: *"Another Athar window is syncing — results will refresh."*

## 3. Architecture

### 3.1 Packages

```
cmd/athar/main.go          entry point; flags --data-dir, --sync, --rescan, --no-update-check, --version
internal/model/            normalized Session / Message types, coverage enum
internal/adapters/         one file per agent + testdata/<agent>/ fixtures
internal/adapters/registry.go   the list of adapters
internal/fsread/           share-friendly file reading (FILE_SHARE_DELETE), SQLite temp-copy reader, long-line JSONL reader
internal/archive/          SQLite schema, migrations, backups, sync state, upserts (never-delete rules)
internal/search/           Arabic/Latin normalization, query parsing, per-session AND, ranking, snippets
internal/sync/             orchestrates adapters → archive; lock file; background loop
internal/tui/              Bubble Tea screens: main, action menu, sources/help, update notes
internal/launch/           building and starting resume/open commands on Windows; clipboard; live-session detection
internal/render/           HTML reading page
internal/update/           update check, notes, signature/checksum verification, self-replace
internal/config/           optional athar.toml next to the exe
```

### 3.2 Data flow

```
agent files (read-only) ─▶ adapter.Read ─▶ model.Session ─▶ archive (SQLite + FTS5)
                                                                   │
                     TUI ◀── search (normalized, per-session AND) ◀┘
                      │
                      └─▶ launch (new tab / app) · render (HTML) · clipboard
```

### 3.3 Adapter interface

```go
type Adapter interface {
    ID() string      // "claude", "codex", ...; stable, used in the archive
    Name() string    // "Claude Code"
    Version() int    // bump when parsing changes; existing sources are re-read in full
    // Sources returns files/DBs to read at default locations and env overrides.
    // Missing locations are skipped silently.
    Sources(env Env) ([]Source, error)
    // Read parses one source. prev is the stored sync state (size, mtime, offset, adapter version).
    Read(ctx context.Context, src Source, prev SyncState) (ReadResult, error)
    Caps() Caps      // coverage, resume/fork/skip flags, app command, env var for the root
}
```

- The registry is a Go slice. Adding an agent = one file + one registry line + fixtures.
- An agent is identified by path (most specific root first), never by content sniffing.
- A panic or error in one adapter is recovered, recorded on the Sources screen, and never stops the others.
- Adapters never execute binaries.

### 3.4 Normalized model

```go
type Coverage string // "full" | "partial" | "prompts" | "titles" | "meta"

type Session struct {
    Agent, NativeID string   // NativeID without any "<agent>:" prefix
    Cwd          string      // exact launch directory, verbatim; for Claude/Qwen/Qoder/Neovate/Command Code
                             // this is the FIRST cwd record and later records never change it
    ProjectPath  string      // git root of Cwd (walk up to a .git entry, never run git), else Cwd; display/grouping
    ProjectName  string      // computed once at first archive: git root basename, else Cwd basename;
                             // "~" for %USERPROFILE%, "D:" for a drive root, "(no project)" when Cwd is empty
    Title, TitleSource string  // TitleSource: user | agent | prompt | none
    StartedAt, UpdatedAt time.Time  // UTC
    Coverage     Coverage
    Root         string      // agent data root for env (e.g. CLAUDE_CONFIG_DIR); "" = default root
    Messages     []Message
}
type Message struct {
    Role string    // user | assistant | tool | system
    Kind string    // text | tool_call | tool_output
    At   time.Time // zero if unknown
    Text string
}
```

### 3.5 Dependencies

`charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2` (TUI; Go ≥ 1.26 because of transitive requirements) · `modernc.org/sqlite` pinned at v1.59.0 or later (SQLite 3.53.4: FTS5 + trigram, pure Go, windows/amd64+arm64) · `github.com/klauspost/compress/zstd` · `google.golang.org/protobuf/encoding/protowire` · `aead.dev/minisign` · `golang.org/x/text` (NFKC, bidi) · `golang.org/x/sys/windows` · `github.com/BurntSushi/toml` · optionally `github.com/AbdullahDiaa/garabic` (Apache-2.0) if the spike needs shaping tables. Nothing else without a documented reason.

## 4. Archive

### 4.1 Location

- `athar-data\archive.db` **next to `athar.exe`** (portable: copy the folder, the history comes with it). `--data-dir <path>` overrides.
- Not writable or under `%TEMP%`: refuse to start (§2.1). No fallback location.
- Inside a cloud-synced folder (path contains `OneDrive`, `Dropbox`, `Google Drive`, `iCloudDrive`): one-time warning that syncing a live SQLite file can corrupt it.
- Linux and macOS (plan `2026-10-26-linux-macos.md`): programs usually live in folders the user cannot write to, so the archive is `athar-data` next to the binary only when that folder already exists (a portable copy); otherwise `$XDG_DATA_HOME/athar` (default `~/.local/share/athar`) on Linux and `~/Library/Application Support/Athar` on macOS. `--data-dir` still overrides.

### 4.2 Schema (sketch)

```sql
sources  (id PK, machine, adapter, adapter_version, path, size, mtime, offset,
          last_ok_at, last_error, UNIQUE(machine, path))
sessions (id PK, agent, native_id, machine,          -- machine = last seen live on
          cwd, project_path, project_name, title, title_source,
          started_at, updated_at, message_count, coverage, root,
          source_id, source_missing_at, origin,     -- origin: live | import:agentsview | import:psm | prompt-log
          UNIQUE(agent, native_id))
messages (id PK, session_id, seq, role, kind, at, text, text_hash)
fts      -- FTS5 trigram over normalized text; rowid = messages.id
         -- contentless with contentless_delete=1, or external-content with explicit 'delete' commands;
         -- chosen in M1 by benchmark. Session title+project are indexed via a second small FTS table.
meta     (key PK, value)   -- schema_version, machine_id, last_sync_at, last_update_check_at, last_backup_at
```

### 4.3 Sync

- **When:** in the background at startup, each time the main screen regains control after an action, and every 60 s while Athar is open. `athar --sync` runs one sync with no UI and exits (exit code 0/1; errors appended to `athar-data\last-sync.txt`). Nothing runs when Athar is closed; the README documents an optional daily `schtasks` line (Athar never registers it itself).
- **Lock:** only one process syncs (`athar-data\sync.lock`, OS file lock). A second instance searches and shows the §2.8 message.
- **Skip unchanged:** a source is skipped when size and mtime are unchanged and `adapter_version` matches. `--rescan` ignores stored state and re-reads everything (subject to §4.4).
- **Changed sources are re-read in full** (v1; the §4.4 merge rules make this safe and idempotent; byte-offset reading is a possible later optimization; measured in October 2026: a 45 MB Claude transcript with 16,000 messages re-reads in about 0.9 s, almost all of it JSON decoding, and the archive appends only the new messages in about 30 ms, so it is not worth the complexity yet). Lines of any length are accepted (`bufio.Reader`, never the 64 KiB `bufio.Scanner` default). A line that fails to parse is skipped and counted, never ends the read; a partial last line is left for the next sync.
- **Opening files:** JSON/JSONL files are opened with `CreateFile(FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE)` and closed right after reading, so agents can still append, rename or delete them. Whole-file JSON sources modified within the last 5 s are skipped until the next sync.
- **SQLite sources are never opened in place** (even `mode=ro` writes `-shm`/creates `-wal`). When size or mtime changed, Athar copies `db`, `-wal`, `-shm` (whichever exist) to `%TEMP%\athar\copy\<source-id>\`, opens the copy (`mode=ro&immutable=1` is safe on the copy), runs `PRAGMA quick_check`, and retries the copy up to 3 times if it fails. The copy is deleted after the source's transaction.
- One source per transaction; a failure rolls back that source only.

### 4.4 Never delete

- **Sources disappearing:** `source_missing_at` is set only at the end of a sync, and only if no live source on this machine still contains the session. Such sessions stay searchable and readable (`🗄`).
- **Sessions moving:** if an existing `(agent, native_id)` shows up at a different source path (e.g. `codex archive`, Antigravity copies, several VS Code hosts), the session points at the newest live source and `source_missing_at` is cleared. With several live copies, messages come from the most recently modified one.
- **Sources shrinking:** when a re-read returns at least as many messages and the stored ones are its prefix (by `text_hash`), it replaces them. Otherwise Athar keeps the stored messages, appends the re-read messages that are not already stored (matched by role + `text_hash`), and notes *"source shrank, archived copy kept"* on the Sources screen.
- Removing sessions from the archive is not a v1 feature.

### 4.5 Imports and prompt logs (ordinary sources, synced every run)

1. **AgentsView** (`~\.agentsview\sessions.db`) and **Pi Session Manager** (`~\.pi\agent\sessions\sessions.db`) indexes, plus any `*.db` with a matching schema under `Documents\AI-History-Backup\`. Read via temp copy. The agent of each row comes from its file path using the live adapters' path rules, falling back to a label table (`vscode-copilot`→`copilot`, `antigravity-cli`→`agy`, PSM `pi` rows by path → `gemini` or `claude`). `native_id` = id with any `<agent>:` prefix removed. Rows with `relationship_type='subagent'` or a parent session id are skipped. Import rows never override a live source's session; they fill sessions that have no live source or have fewer messages.
2. **Prompt logs** are sources of their agent's adapter: Claude `history.jsonl` (every Claude root), Codex `history.jsonl`, Gemini/Qwen `logs.json`, Antigravity CLI `history.jsonl`, Command Code `history.jsonl`. They create `coverage=prompts` sessions for session ids with no transcript anywhere, and are merged into a fuller session when one exists.
3. One session per `(agent, native_id)`. Each importer has a fixture test proving an imported row and its live twin collapse into one session.

### 4.6 Machine

- Machine id = Windows `MachineGuid` (read-only from `HKLM\SOFTWARE\Microsoft\Cryptography`), shown with the hostname. On Linux `/etc/machine-id` (then `/var/lib/dbus/machine-id`), on macOS the `IOPlatformUUID` reported by `ioreg`; the hostname when none is available. Sync reads and marks missing only the current machine's sources.
- A session's `machine` means "last seen live on". `R`/`F`/`!`/`D` appear only when the current machine has a live source for it; otherwise the preview says *"From <hostname> — read only"*.
- Merging archives from two PCs is not a v1 feature; a portable archive simply keeps what it already holds.

### 4.7 Stale-archive warning

The Sources screen shows the last sync time. When the last sync is more than 20 days old at startup, the footer shows *"Last sync 23 days ago — Claude Code and Gemini delete sessions after 30 days"* until the sync finishes.

### 4.8 Backups

Before any schema migration, and otherwise at most once every 7 days at startup, Athar writes `athar-data\backup\archive-YYYY-MM-DD.db` with `VACUUM INTO` and keeps the newest 2. If `archive.db` fails to open or `PRAGMA quick_check`, Athar says so and offers to restore the newest backup.

## 5. Search

### 5.1 What is indexed

- User and assistant message text, in full.
- Tool calls: tool name, command line and file paths.
- Tool outputs: first 2 KB of each.
- Session title and project name (separate table, weighted higher).
- Not indexed: thinking/reasoning, images, encrypted content, `(no project)`.

### 5.2 Normalization (indexed text and queries)

1. Unicode NFKC (also folds Arabic presentation forms).
2. Remove Arabic diacritics U+064B–U+065F, U+0670, Quranic marks U+06D6–U+06ED, and tatweel U+0640.
3. Unify letters: أ إ آ ٱ → ا; ة → ه; ى → ي; ؤ → و; ئ → ي; Persian ک → ك, ی → ي.
4. Digits: U+0660–U+0669 and U+06F0–U+06F9 → 0–9.
5. Latin case folding.

Tests assert: مشروع finds المشروع and بمشروعي; احمد finds أحمد; إسلام finds وإسلام; كتب finds كُتُب; deploy finds deployment; ٢٠٢٦ finds 2026 in message text; ٢٣ finds a session whose title contains 23.

### 5.3 Matching and ranking

- SQLite FTS5 `trigram` tokenizer over normalized text (substring matching handles ال/و/ب prefixes and partial English words).
- **Per-session AND:** each whitespace-separated term must match somewhere in the same session (any message, the title or the project); terms may sit in different messages. Implementation: one FTS query per term → set of session ids → intersect. A quoted `"exact phrase"` must match inside one message.
- Terms shorter than 3 characters match titles and project names only.
- If no session matches every term, show the sessions that match the most terms under `Partial matches (k of n words)`.
- The preview shows the message that matches the most terms (ties by bm25). Ranking (v1.2, owner's choice): sessions matching all terms first, then partial matches (fewest missing words first); within each group the newest session (last activity) is on top.
- Results are paged lazily.

### 5.4 Performance targets

Keystroke to results < 150 ms on the owner's archive; first full sync ≤ 3 minutes; incremental sync with nothing changed < 2 s. Archive size measured in M1 (expected 150–400 MB).

## 6. Launching and resuming

### 6.1 New tab

- **Windows Terminal (`wt.exe` found):** argv `-w 0 new-tab --title <T> --suppressApplicationTitle --inheritEnvironment -d <Cwd> cmd.exe /d /s /k <C>`, where `<C>` is the whole command as **one** argv element (WT wraps it in quotes; `/s` makes cmd strip exactly that pair). Every `;` in `<T>`, `<Cwd>`, `<C>` is written `\;`. `-w 0` targets the most recently used WT window, or creates one; Athar does not try to detect its host (`WT_SESSION` is unset under default-terminal handoff).
- **Otherwise:** start `cmd.exe` directly with `CREATE_NEW_CONSOLE`, `Dir=<Cwd>`, the prepared environment block, and `SysProcAttr.CmdLine = cmd.exe /d /s /k "title <safe-title> && <C>"`. No `cmd /c start` wrapper.
- **Safety:** `<safe-title>` keeps only letters, digits, spaces and `.-_·`, max 60 chars. Values from agent data are embedded in `<C>` only if they pass a whitelist (ids: `^[A-Za-z0-9._:-]+$`; paths: no `"`, `%`, `!`, `^`, newline). Anything else is passed via the environment block or the working directory, never as command text. If `<Cwd>` fails the whitelist, it is used only as the process working directory.
- **Environment:** Athar sets the child's environment and also embeds the variable that selects the agent's data root, clearing it for the default root: `set "CLAUDE_CONFIG_DIR=%USERPROFILE%\.claude-local" && claude --resume <id>` or `set "CLAUDE_CONFIG_DIR=" && claude --resume <id>`. Same for `CODEX_HOME`, `GEMINI_CLI_HOME`, `HERMES_SESSIONS_DIR`, `CONTINUE_GLOBAL_DIR` where relevant.
- Commands run through `cmd /k` so npm `.cmd` shims resolve via PATHEXT and the tab stays open after the agent exits.
- After launching, Athar stays open and shows *"Opened in new tab"*; the next sync picks up changes.
- **Linux** (plan `2026-10-26-linux-macos.md`, phase 3): the terminal Athar runs in when it can tell (`KITTY_WINDOW_ID`, `WEZTERM_PANE`, `KONSOLE_VERSION`, `GNOME_TERMINAL_SCREEN`, `ALACRITTY_WINDOW_ID`), then `$TERMINAL`, then gnome-terminal, konsole, xfce4-terminal, kitty, wezterm, alacritty, foot, x-terminal-emulator, xterm. The terminal gets an argv, never a shell string: `/bin/sh -c <script> athar <Cwd> /usr/bin/env [-u VAR]… [VAR=value]… <program> <args>…`, where `<script>` is the constant `cd -- "$1" && shift && "$@"; exec "${SHELL:-/bin/sh}"` (it keeps the tab open after the agent exits, like `cmd /k`). Every value is a positional parameter, so no value is ever parsed by a shell. The program is resolved to an absolute path from the absolute PATH entries only, and the child's PATH keeps only absolute entries, so a program in the session folder never runs in its place (the Unix counterpart of `NoDefaultCurrentDirectoryInExePath`). The folder must be absolute. Terminals not in the list, and a `$TERMINAL` that is not one of them, are never used (some re-parse `-e` as a shell string).
- **macOS:** `osascript` tells Terminal to `do script` one command: `exec` of the Linux argv above, every word single-quoted (`'` → `'\''`). It is passed as an argument (`item 1 of argv`), never as AppleScript text. Values with control characters, invisible format characters, `\` or `!` are refused rather than embedded, so the quoted words mean the same in the user's login shell whether it is sh, bash, zsh, fish or csh.
- GUI apps on Linux and macOS are started directly (argv, own session, no terminal). Folders and the reading page open with `xdg-open` (Linux) or `open` (macOS), always with an absolute path.

### 6.2 Pre-flight checks

Before launching, with a clear message and an offer to `O` Read instead:
- Folder no longer exists: *"Folder no longer exists: <path>"*.
- Drive not connected: *"Drive D: is not connected"*.
- Agent binary not found on PATH or known install paths: *"`codex` is not installed or not on PATH"* (lookup only, never executed).

### 6.3 Commands per agent

| Agent | Resume | Fork | Skip-permissions flag | Notes |
|---|---|---|---|---|
| Claude Code | `claude --resume {id}` | `--fork-session` | `--dangerously-skip-permissions` | root via `CLAUDE_CONFIG_DIR` |
| Codex | `codex resume {id}` | — | `--dangerously-bypass-approvals-and-sandbox` | |
| Gemini CLI | `gemini --resume {id}` | — | `--yolo` | full UUID from the first record's `sessionId`; lookup is per project, so Cwd must be the project root (verified against 0.46.0 source) |
| Antigravity CLI | `agy --conversation {id}` | — | `--dangerously-skip-permissions` | |
| Hermes | `hermes --resume {id}` | — | `--yolo` | restores the recorded cwd itself |
| OpenCode | `<opencode> --session {id}` | — | — | `<opencode>` = `opencode` on PATH or `%LOCALAPPDATA%\opencode\opencode-cli.exe`; `-d` sets the directory |
| Kilo 7 | `kilo --session {id}` | — | — | kilo.db sessions only |
| Qoder | `qodercli --resume {id}` | — | `--dangerously-skip-permissions` | |
| Neovate | `neovate --resume {id}` | — | `--approval-mode yolo` | |
| Command Code | `cmdc --resume {id}` | `--fork-session` | `--dangerously-skip-permissions` | `cmd` shim is shadowed by cmd.exe |
| Qwen (legacy logs) | — | — | — | prompts only |
| DeepSeek dsh, Codebuff | — | — | — | resume unconfirmed. Never run `freebuff` (it self-updates on launch) |
| Cursor | app `cursor "{cwd}"` | — | — | pick chat from history |
| VS Code (Copilot Chat, Cline, Continue) | app `code "{cwd}"` | — | — | pick from the extension's history |
| Antigravity IDE, Kilo legacy (Antigravity-hosted) | app `antigravity "{cwd}"` | — | — | |
| Trae | app `Trae.exe "{cwd}"` (install path) | — | — | |
| LM Studio | app: start LM Studio | — | — | |
| Zed, Windsurf | — | — | — | apps not installed here; Read only |

### 6.4 Copy command

PowerShell syntax: `Set-Location -LiteralPath '<Cwd>'; $env:NAME='value'; <command>`. Every embedded `'` is doubled (`O''Brien`); a quoted executable path gets the call operator (`& 'C:\…\opencode-cli.exe' --session …`). Tests cover `'`, `$`, backtick, `&` and `;` in paths.
- `{cwd}` is substituted into arguments only when it is an absolute path (`/…`, `C:\…`, `\\server\…`); otherwise `.` is used, so a recorded cwd can never become an option such as `--user-data-dir=…`.
- On Linux and macOS `C` copies the sh form instead (same character rules as the macOS line): `cd -- '<cwd>' && unset VAR && export VAR='value' && <program> <args>` (every value single-quoted unless it is a plain word). The clipboard is fed through `pbcopy`, `wl-copy`, `xclip` or `xsel` on standard input.

### 6.5 Live-session detection (Claude Code)

Claude Code writes `<root>\sessions\<pid>.json` (`pid`, `sessionId`, `cwd`, `status`, `kind`, `procStart`) for each running session. Athar reads this folder under every Claude root. A record is live only if the pid exists **and** its process creation time (`GetProcessTimes`, FILETIME) equals `procStart`; image names are never checked. Live rows show `●`. If `kind` is `interactive`, `R` shows the warning of §2.3 and `F` is listed first. If `kind` is anything else (background), `R` is hidden (Claude refuses it) and `F` is offered. Records with missing fields count as not live.

## 7. Agent coverage (v1)

| Agent | Where (Windows) | Format | Coverage on owner's PC |
|---|---|---|---|
| Claude Code | `projects\<enc>\<id>.jsonl` under every root: `%USERPROFILE%\.claude`, `CLAUDE_CONFIG_DIR`, any `%USERPROFILE%\.claude*` dir containing `projects\`; skip `<id>\subagents\`, `tool-results\`, `memory\`; prompt log `history.jsonl`; live registry `sessions\` | JSONL events; Cwd = first `cwd` (never decode folder names); titles from last `custom-title`/`agent-name`/`ai-title`/`summary`; skip `isMeta`, `isCompactSummary`, command/caveat/system-reminder/task-notification wrappers for the first prompt | full + prompts |
| Codex | `~\.codex\sessions\YYYY\MM\DD\rollout-*.jsonl`, `archived_sessions\`, `state_*.sqlite` (titles), `history.jsonl`, `CODEX_HOME`; accept `.jsonl.zst` | JSONL `{timestamp,type,payload}`; first prompt from `event_msg`/`user_message` | full + prompts |
| Gemini CLI | `~\.gemini\tmp\<slug>\chats\session-*.jsonl` (+ legacy `.json`), project from `.project_root`, `logs.json`, `GEMINI_CLI_HOME` | JSONL op-log: replay `$set`/`$rewindTo` | full + prompts |
| Qwen Code | `~\.qwen\projects\<enc>\chats\*.jsonl`; legacy `~\.qwen\tmp\*\logs.json` | JSONL / JSON | prompts |
| Antigravity IDE | `~\.gemini\antigravity{,-backup,-ide}\` deduped by id; index `%APPDATA%\Antigravity\User\globalStorage\state.vscdb` key `antigravityUnifiedStateSync.trajectorySummaries` (base64 → protobuf) and `agyhub_summaries_proto.pb`; text from `brain\<id>\*.md` + `*.metadata.json` + `.system_generated\logs\*` | encrypted `.pb` (not read) + protobuf index + Markdown | titles / partial / full |
| Antigravity CLI (agy) | `~\.gemini\antigravity-cli\conversations\<id>.db` (metadata blob), `history.jsonl`, `brain\<id>\.system_generated\messages\*.json` | SQLite (protobuf blobs) + JSONL | prompts |
| OpenCode | `~\.local\share\opencode\opencode*.db` + legacy `storage\` JSON tree | SQLite (detect tables) + JSON | full |
| Kilo Code | 7.x `~\.local\share\kilo\kilo.db`; legacy `%APPDATA%\<Host>\User\globalStorage\kilocode.kilo-code\tasks\` | SQLite (OpenCode schema) / JSON tasks | full |
| Cline | `%APPDATA%\<Host>\User\globalStorage\saoudrizwan.claude-dev\` (tasks + `state\taskHistory.json`); SDK `~\.cline\data\sessions\` | JSON | full |
| Continue | `~\.continue\sessions\` (`CONTINUE_GLOBAL_DIR`) | JSON | full |
| VS Code Copilot Chat | `%APPDATA%\Code*\User\workspaceStorage\<h>\chatSessions\*.json(l)` + `globalStorage\emptyWindowChatSessions\`; project from `workspace.json` | JSON / JSONL mutation log (replay kinds 0–3) | full |
| Cursor IDE | `%APPDATA%\Cursor\User\globalStorage\state.vscdb` (`cursorDiskKV` `composerData:*`, `bubbleId:*`; key-range queries), project via workspace DBs + `workspace.json`; `~\.cursor\projects\*\agent-transcripts\` if present | SQLite key-value JSON | full |
| Hermes | `%LOCALAPPDATA%\hermes\state.db` (also `~\.hermes`, `HERMES_SESSIONS_DIR`) | SQLite | full |
| ZCode | `~\.zcode\cli\db\db.sqlite` | SQLite (OpenCode-like) | full |
| Zed | `%LOCALAPPDATA%\Zed\threads\threads.db` | SQLite, zstd JSON blobs (streaming decoder) | full |
| Qoder CLI | `~\.qoder\projects\<enc>\*.jsonl` + `-session.json` | JSONL | full |
| Neovate | `~\.neovate\projects\<enc>\*.jsonl` | JSONL | full |
| Command Code | `~\.commandcode\projects\<enc>\*.jsonl`, `history.jsonl` | JSONL | full + prompts |
| DeepSeek dsh | `~\.dsh\sessions\--<enc>--\session-*\session*.jsonl.zstd` | zstd JSONL events (epoch ms) | full |
| Codebuff / Freebuff | `~\.config\manicode\projects\*\chats\*\log.jsonl`, `message-history.json` | debug-log JSONL | partial |
| LM Studio | `~\.lmstudio\conversations\*.conversation.json` | JSON | full |
| Trae | `%APPDATA%\Trae\User\workspaceStorage\*\state.vscdb` key `memento/icube-ai-agent-storage` | SQLite key-value JSON (paged) | partial |
| Windsurf | `~\.codeium\windsurf\cascade\*.pb` | encrypted | meta |
| AgentsView / Pi Session Manager indexes | §4.5 | SQLite | imported |

- **VS Code–family hosts** are discovered generically: every `%APPDATA%\<Host>\User\globalStorage\` (Code, Code - Insiders, VSCodium, Cursor, Windsurf, Antigravity, Trae, …) is scanned for known extension folders.
- Per-adapter rules from research (Codex filenames are local time while records are UTC; OpenCode stores forward slashes and ms timestamps; Gemini file names carry only 8 hex chars of the id; Cursor/Copilot empty drafts are skipped; Antigravity's three copies are deduped) live in each adapter's doc comment and fixture tests.

## 8. Configuration (optional)

`athar.toml` next to the exe; absent file = defaults.

```toml
[resume.codex]      # optional per agent: resume / fork / skip_permissions
resume = "codex resume {id}"       # program + arguments, no shell syntax; placeholders {id} {cwd}
[display]
arabic = "shaped"   # "shaped" | "raw"; default chosen by the M1 spike
[update]
check = true
```

Athar always adds the agent's environment and wraps overrides for cmd (launch) or PowerShell (copy).

## 9. Updates and distribution

- **CI:** GitHub Actions. Every push/PR: `go vet`, `go test ./...` on `windows-latest` (+ `ubuntu-latest` build check). Tag `vX.Y.Z`: GoReleaser builds `windows/amd64`, `windows/arm64` (+ macOS/Linux as a by-product). The Windows assets are **bare `athar.exe` files** (`format_overrides: goos windows → binary`), plus `checksums.txt` and `checksums.txt.minisig` (minisign; secret key and password in GitHub Secrets, public key embedded in Athar).
- **No public tag or GitHub release before v1.0.0.** Milestone builds are owner builds (local or CI artifacts).
- **Update check:** at most once per day, in the background, 5 s timeout, silent offline: `GET https://api.github.com/repos/AbuDergham/Athar/releases/latest`. If newer, the footer shows *"Update vX.Y available — see F2"*; on F2, `U` shows the release notes with `I` install / `Esc` back.
- **Install:** download `checksums.txt` + `.minisig`, verify the signature with `aead.dev/minisign` against the embedded key, download the asset for this OS/arch, check its SHA-256, rename the running `athar.exe` to `athar.exe.old`, write the new exe, restart; delete `.old` on the next start. The archive is untouched.
- **Opt-out:** `--no-update-check` or `[update] check = false`.
- **Unsigned exe:** SmartScreen warns on first run; documented in the README. Code signing is out of scope for v1.

## 10. Privacy and security

- No telemetry, analytics or crash reporting; no listening sockets.
- Network: only the update check and, after confirmation, the update download.
- Agent data is read-only: JSON/JSONL opened with full sharing and closed immediately; SQLite read only from temp copies; nothing is created in agent folders. Athar never executes agent binaries during discovery (`freebuff --version` triggers a self-update).
- Launch commands never embed unvalidated text from conversations (§6.1).
- The archive is plain SQLite in the user's folder, the same protection level as the agents' own files. The HTML reading page has no external resources.
- License: **MIT**. Ideas may be borrowed from AgentsView, Pi Session Manager and cass/CASR. Code may be copied only from AgentsView (MIT; keep its notice in `NOTICE`). Never copy code from Pi Session Manager (no license) or cass/CASR (license rider excludes Anthropic and anyone acting on its behalf).
- Test fixtures are synthetic; no real conversation text is committed.

## 11. Testing

- **Adapter fixtures:** synthetic, sanitized samples per adapter in `testdata/<agent>/` with golden assertions (sessions, titles, Cwd, project, timestamps, message counts, coverage). Every JSONL adapter has a fixture with a single 2 MB record.
- **Read-only guarantee:** after a sync over the fixture tree, no file in any fixture agent folder has a changed mtime/size and no file was created there (SQLite fixtures in WAL mode included).
- **Normalization:** table tests for §5.2.
- **Search:** per-session AND across messages, partial-matches fallback, phrase matching, <3-char terms.
- **Archive:** incremental append, shrink (never delete), move between sources, missing-source marking only at sync end, import/live dedup, backups and quick_check recovery, adapter-version re-read.
- **Launch:** command strings per agent; round-trip every launch string through WT's join rule (quote tokens containing spaces, no inner escaping) and then `cmd /s /k`, for paths with spaces, `'`, `&`, `;`, `%`; titles with `"`, `&`, `%`, `|`; env set/clear; PowerShell copy strings parsed by the PowerShell parser.
- **Manual acceptance on the owner's PC** before v1.0.0: first-sync counts match the inventory, Arabic and English searches, resume in a new tab for Claude/Codex/Gemini/agy, Read page renders Arabic RTL and scrolls to the hit, no agent file modified (mtime snapshot before/after).

## 12. Milestones (internal)

| # | Scope |
|---|---|
| M0 | Repo, Go module, CI, GoReleaser config (snapshot build only) |
| M1 | fsread + archive + normalization + search + sync + Claude adapter (all roots, history.jsonl, live registry) + TUI + launch + Arabic spike → owner build that replaces `Resume-Session.cmd` |
| M2 | JSONL/JSON CLI agents: Codex, Gemini, Qwen, Qoder, Neovate, Command Code, dsh, Codebuff, agy history, LM Studio, Continue |
| M3 | SQLite agents: OpenCode (+legacy JSON), Kilo 7, Hermes, ZCode, Zed, agy DB metadata |
| M4 | VS Code–family and app agents: Cline, Kilo legacy, Copilot Chat, Cursor, Trae, Windsurf; generic host discovery |
| M5 | Antigravity IDE, AgentsView/PSM imports, Sources screen details, HTML reader polish |
| M6 | Updater + signing + README (English + Arabic section) → **v1.0.0** public release |

Prerequisite on the owner's PC: Go (portable install at `%USERPROFILE%\sdk\go`, 1.27.1).

## 13. Risks and open questions

1. **Arabic rendering in Windows Terminal** — M1 spike decides shaped vs raw.
2. **FTS layout** (contentless vs external content) and archive size — M1 benchmark on the owner's data. Trigram availability is resolved (modernc.org/sqlite v1.59.0 / SQLite 3.53.4). `unicode61` + prefix is **not** an acceptable fallback (fails §5.2).
3. **OpenCode desktop-bundled CLI (1.1.54) vs a DB written by a newer CLI** — test resume against a copy before enabling resume for OpenCode.
4. **Antigravity index** is undocumented nested protobuf/base64 — fragile; fixtures built from the structure (not content) of the owner's files.
5. **Agent format churn** (Codex `.jsonl.zst`, Cursor store moves, Kiro generations) — fixture tests + adapter versions.
6. **Name:** no AI-session tool named Athar on GitHub as of 2026-09-23; unrelated repos named athar exist (e.g. milkstrawai/athar, a Rails gem). "AgentRecall" was rejected because zszz3/AgentRecall is the same category.

## 14. References

- Session research of 2026-09-23 (local inventory, per-agent storage formats, cross-check) and spec review (fidelity, technical, simplicity lenses).
- AgentsView — https://github.com/kenn-io/agentsview · Pi Session Manager — https://github.com/Dwsy/pi-session-manager · agf — https://github.com/subinium/agf · SpecStory CLI — https://github.com/specstoryai/getspecstory · Agent Sessions — https://github.com/jazzyalex/agent-sessions · chatscan — https://github.com/pmarreck/chatscan
- Windows Terminal command line: https://learn.microsoft.com/windows/terminal/command-line-arguments (PR #15897 environment inheritance)
- SQLite FTS5: https://sqlite.org/fts5.html · SQLite WAL read-only behaviour: https://sqlite.org/wal.html
- Claude Code `cleanupPeriodDays` (default 30, minimum 1) · Gemini CLI `general.sessionRetention` (enabled, 30 d)
