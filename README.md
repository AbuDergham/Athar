# Athar · أثر

**Find any past conversation with any AI agent on your PC — then read it or jump back in.**

You remember talking to *some* AI agent about *something*, weeks ago, in *some* project. Athar searches the local history of all your agents at once (Claude Code, Codex, Gemini CLI, Antigravity, Cursor, Copilot Chat, Cline, OpenCode and many more), in Arabic or English, and resumes the session in a new terminal tab.

- **One screen.** Type what you remember; results update as you type. An empty search shows your timeline, day by day.
- **Every agent on your machine,** discovered automatically. See [Supported agents](#supported-agents).
- **Never lose history.** Claude Code and Gemini CLI delete transcripts after 30 days by default. Athar keeps its own archive next to `athar.exe`, and it can recover conversations that AgentsView or Pi Session Manager already captured.
- **Arabic-aware search.** `مشروع` finds `المشروع` and `بمشروعي`, `احمد` finds `أحمد`, diacritics and hamza variants don't matter. English partial words work too (`deploy` finds `deployment`).
- **Private.** Everything stays on your PC. Athar only reads agent files (never writes to them). The only network request is a once-a-day update check, which you can turn off.

## Install

1. Download `athar_<version>_windows_amd64.exe` from [Releases](https://github.com/AbuDergham/Athar/releases/latest) and rename it to `athar.exe`.
2. Put it in a folder you own, for example `D:\Tools\Athar\`. Athar keeps its archive in `athar-data\` next to the exe, so the folder must be writable and not temporary.
3. Double-click it. The first run reads your agents' history (a minute or two); after that it opens instantly.

Windows SmartScreen may warn the first time because the exe is not code-signed. Choose *More info → Run anyway*. Release files are signed with [minisign](https://jedisct1.github.io/minisign/) and Athar verifies that signature before installing an update.

## Use

| Key | Action |
|---|---|
| type | search (newest results first; words may be in different messages; `"exact phrase"`; words under 3 letters match titles and projects) |
| `↑` `↓` `PgUp` `PgDn` `Home` `End` | move |
| `Ctrl+↑` / `Ctrl+↓` | previous / next day in the timeline |
| `Enter` | actions for the selected session |
| `Shift+Enter` | resume the selected session **without permission prompts** right away, without opening the menu (e.g. `--dangerously-skip-permissions`) |
| `Tab` | filter by agent |
| `Ctrl+T` | filter by time (today, 7 days, 30 days, 12 months) |
| `F2` | sources, archive, updates and help |
| `Esc` | clear the search, or quit |

Actions (`Enter`), which also work with the Arabic keyboard layout:

| Key | Action |
|---|---|
| `R` (ق) | resume the session in a new Windows Terminal tab (or open the project in the agent's app) |
| `F` (ب) | fork: resume as a new session (offered when the session is open in another window) |
| `!` | resume without permission prompts, at once (`Shift+Enter` on the list does the same without opening the menu) |
| `O` (خ) | read the whole conversation in your browser (Arabic displayed right-to-left) |
| `C` (ؤ) | copy the resume command |
| `D` (ي) | open the project folder |

### Keep the archive fresh

Athar syncs while it is open. To also archive sessions when you don't open it for a while (agents delete old sessions after 30 days), schedule a daily headless sync:

```bat
schtasks /Create /SC DAILY /ST 12:00 /TN "Athar sync" /TR "\"D:\Tools\Athar\athar.exe\" --sync"
```

### Options

- `athar --sync` — sync once without the UI (for the scheduled task)
- `athar --rescan` — re-read every source
- `athar --data-dir <path>` — keep the archive somewhere else
- `athar --no-update-check` — never contact GitHub
- `athar.toml` next to the exe — override resume commands, Arabic display (`shaped`/`raw`), update checks

## Supported agents

| Agent | What Athar reads | Get back in |
|---|---|---|
| Claude Code (every config folder, incl. `CLAUDE_CONFIG_DIR`) | full conversations + prompt history of deleted sessions | `claude --resume` |
| Codex CLI / Codex in VS Code | full | `codex resume` |
| Gemini CLI | full | `gemini --resume` |
| Antigravity IDE | titles, dates, projects, plans and walkthroughs (conversations are encrypted) | opens Antigravity |
| Antigravity CLI (agy) | prompts | `agy --conversation` |
| OpenCode, Kilo Code, Hermes, ZCode, Zed | full | per agent |
| Cline, Kilo Code (legacy), Continue, GitHub Copilot Chat (VS Code) | full | opens VS Code / the editor |
| Cursor | full | opens Cursor |
| Qwen Code, Qoder, Neovate, Command Code, DeepSeek dsh, Codebuff | full or prompts | per agent |
| LM Studio, Trae, Windsurf | full / partial / dates only | opens the app |
| AgentsView and Pi Session Manager archives | imported (recovers sessions the agents deleted) | read only |

Missing your agent? See [CONTRIBUTING.md](CONTRIBUTING.md): an adapter is one Go package.

## Privacy and security

- No telemetry, no accounts, no listening ports.
- Agent files are opened with full sharing and closed immediately; SQLite databases are read from a private temporary copy, so agents are never blocked or modified.
- Commands built from conversation data are validated before they reach a shell.
- The archive is a plain SQLite file in `athar-data\` — as private as your agents' own files.

## Build from source

```sh
go build -o athar.exe ./cmd/athar
go test ./...
```

The icon comes from `cmd/athar/winres/athar.ico` (drawn from the SVGs next to it); after changing it, run `go generate ./cmd/athar`.

Requires Go 1.26+. Design notes: [docs/superpowers/specs](docs/superpowers/specs).

## License

MIT © AbuDergham

---

## بالعربي

**أثر** أداة بتلاقيلك أي محادثة قديمة مع أي وكيل ذكاء اصطناعي على جهازك، وبتخليك تقرأها أو تكمّلها.

بتتذكر إنك حكيت مع وكيل ما عن شي ما، بمشروع ما، من كم أسبوع؟ اكتب اللي بتتذكره، وأثر بيدوّر بسجل كل الوكلاء مرة وحدة: Claude Code و Codex و Gemini و Antigravity و Cursor و Copilot و Cline و OpenCode وغيرهم.

- **شاشة وحدة:** اكتب، والنتايج بتطلع وإنت عم تكتب، الأحدث فوق والأقدم تحت. وإذا البحث فاضي بيطلعلك تسلسل شغلك يوم بيوم.
- **ما بيضيع شي:** Claude Code و Gemini بيمحوا الجلسات بعد 30 يوم. أثر بيحتفظ بنسخة دائمة جنب البرنامج، وبيقدر يرجّع الجلسات اللي كانت محفوظة عند AgentsView أو Pi Session Manager.
- **بحث عربي ذكي:** "مشروع" بتلاقي "المشروع" و"بمشروعي"، و"احمد" بتلاقي "أحمد"، والتشكيل والهمزات ما بيأثروا.
- **خاص:** كل شي بيضل على جهازك. أثر بيقرأ ملفات الوكلاء بس وما بيعدّل عليها، والاتصال الوحيد بالنت هو فحص التحديثات مرة باليوم، وفيك تطفيه.

**التشغيل:** نزّل `athar.exe` من [Releases](https://github.com/AbuDergham/Athar/releases/latest)، حطّه بمجلد إلك (مثلاً `D:\Tools\Athar`)، ودبل كليك. أول مرة بياخد دقيقة أو ثنتين ليقرأ السجل، وبعدها بيفتح فوراً.

**الأزرار:** اكتب لتبحث · `Shift+Enter` بيكمّل الجلسة فوراً بدون أسئلة إذن (dangerously) · `Enter` للخيارات: `R`/ق كمّل الجلسة بتاب جديد · `O`/خ اقرأها بالمتصفح (العربي مرتب) · `C`/ؤ انسخ الأمر · `D`/ي افتح المجلد · `Tab` اختار الوكيل · `Ctrl+T` الفترة · `F2` المصادر والمساعدة.
