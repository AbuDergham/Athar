# Athar on Linux and macOS: Implementation Plan

**Goal:** the same Athar (one binary, one screen, read-only toward agents) on Linux and macOS, released next to the Windows builds.

**Status (2026-10-26):** Athar is Windows-only (spec §1, `.goreleaser.yaml`). The code builds and the portable tests pass on Linux (CI `test-linux`), but agent locations, launching and updating are Windows-only.

**Spec:** `docs/superpowers/specs/2026-09-23-athar-design.md`. Where this plan changes the spec, the phase updates the spec in the same change.

## Global constraints

- Everything in the spec's "read-only toward agents" and privacy rules holds unchanged. A new OS adds locations to read, never a way to write into agent folders.
- Windows behavior must not change. Every phase keeps the Windows CI job green; Windows-only test cases stay as they are.
- Each OS-specific piece lives behind a build tag (`*_windows.go`, `*_darwin.go`, `*_linux.go`, `*_other.go`) or a pure function taking `goos` that is tested for every OS on every OS.
- No adapter is declared supported on an OS until it has been checked against real data there (`ATHAR_REALDATA=1 go test ./internal/adapters/<id>/ -run TestRealData`), statistics only.

## Where the code depends on Windows (survey)

| Area | Today | Phase |
|---|---|---|
| `adapters.CurrentEnv` | `AppData`/`LocalAppData` from `%APPDATA%`/`%LOCALAPPDATA%`; empty elsewhere, so 7 adapters (Cursor, Copilot, Cline/Kilo legacy, Trae, Zed, Hermes, Antigravity) find nothing | 0 |
| Data folder | `athar-data` next to the exe; refuses temp and read-only folders | 0 |
| Machine id | `MachineGuid` from the registry; hostname elsewhere | 0 |
| Project of a session (`archive/project.go`) | git roots are probed for drive-letter paths only | 1 |
| Imported Claude roots (`imports/agentmap.go`) | always rewritten with `\` | 1 |
| Neovate project keys (`neovate/project.go`) | every key folded case-insensitively, `/` = `\` | 1 |
| VS Code-style `file://` URIs (Copilot, Cursor, Continue, Trae, agy) | already keep Unix paths (`file:///home/u/x` → `/home/u/x`) | none |
| Gemini project hashes | `trueCase` only for drive paths; hash of the path as stored works on Unix | none |
| Claude project folders | never decoded (cwd comes from the records) | none |
| Per-agent locations on Linux/macOS | unknown for most agents | 2 |
| Launch (`launch/start_*.go`, `wt.go`, `validate.go`) | Windows Terminal, `cmd.exe`, PowerShell quoting; `start_other.go` returns "Windows only" | 3 |
| Self-update (`update/install_*.go`) | Windows exe swap; `ErrUnsupported` elsewhere | 4 |
| Release | Windows amd64/arm64 only | 4 |
| Terminal behavior (bidi, `Ctrl+↑`, `Shift+Enter`) | tuned for Windows Terminal | 5 |

## Phase 0: foundations

- [x] `adapters.CurrentEnv` fills `AppData` and `LocalAppData` with each OS's equivalent, from a pure `EnvFor(goos, home, getenv)` tested for all three OSes:
  - Windows: `%APPDATA%`, `%LOCALAPPDATA%` (unchanged).
  - macOS: both `~/Library/Application Support` (Electron apps keep `User/` there, like `%APPDATA%` on Windows).
  - Linux: `$XDG_CONFIG_HOME` or `~/.config` (Electron's `appData`), and `$XDG_DATA_HOME` or `~/.local/share`.
- [x] Data folder on Linux and macOS: `athar-data` next to the binary when that folder already exists (portable copy); otherwise `$XDG_DATA_HOME/athar` (`~/.local/share/athar`) on Linux and `~/Library/Application Support/Athar` on macOS. `--data-dir` wins everywhere. Windows unchanged. Spec §4.1 updated.
- [x] Machine id: `/etc/machine-id` (then `/var/lib/dbus/machine-id`) on Linux, `IOPlatformUUID` from `ioreg` on macOS (a system tool, never an agent binary), hostname as the last fallback. Spec §4.6 updated.

## Phase 1: paths

- [x] `archive/project.go`: probe git roots for any absolute Unix path (`/…`), as for drive-letter paths on Windows. UNC and device paths stay excluded on Windows.
- [x] `imports/agentmap.go`: an imported Claude root keeps the separators of the OS it is read on (`filepath.FromSlash`), so an imported row and its live twin share the root on every OS.
- [x] `neovate/project.go`: fold letter case and `/` vs `\` only for paths spelled the Windows way (drive letter or `\`); Unix paths are compared exactly (trailing separators still ignored).
- [x] The tests gated to Windows in v1.3.0 because of these paths run everywhere again.

## Phase 2: agents (needs real machines)

For each adapter: record the Linux and macOS locations (and their environment overrides), add synthetic fixtures with Unix paths, run `TestRealData` on a real machine.

- [ ] Home-folder agents, expected to work as is: Claude Code, Codex, Gemini CLI, Qwen Code, Antigravity CLI, OpenCode, Kilo, Command Code, Codebuff, Continue, DeepSeek dsh, Neovate, Qoder, ZCode, Cursor transcripts, AgentsView and Pi Session Manager imports.
- [ ] VS Code-family stores through `AppData` (Phase 0): Copilot Chat, Cline, Kilo legacy, Cursor, Trae, Antigravity. Check the host folder names on each OS.
- [ ] Zed: `~/.local/share/zed` (lower-case) on Linux, `~/Library/Application Support/Zed` on macOS.
- [ ] Hermes, LM Studio, Windsurf: locations and formats to be confirmed on real machines.
- [ ] README "Supported agents": a column per OS, filled only from real-data checks.

## Phase 3: launching

- [ ] macOS: resume in a new tab of the terminal Athar runs in (`$TERM_PROGRAM`: iTerm2 or Terminal.app through `osascript`), else a new Terminal.app window. `open` for folders, files and apps; `pbcopy` for the clipboard.
- [ ] Linux: `$TERMINAL`, then gnome-terminal, konsole, kitty, wezterm, alacritty, xterm, each with its "new tab" or "new window" form. `xdg-open` for folders and files; `wl-copy`, then `xclip`, then `xsel` for the clipboard.
- [ ] `validate.go`: a POSIX `sh` quoting and validation path next to the `cmd.exe`/PowerShell one; the "copy command" action shows the `sh` form. Security review before merge.
- [ ] "Open in another window" detection: `/proc/<pid>/cmdline` and start time on Linux, `ps -o lstart=` on macOS.
- [ ] Resume-command candidates: `ProgramCandidates` per OS (e.g. `~/.npm-global/bin`, `/opt/homebrew/bin`), `%VARS%` stay Windows-only.

## Phase 4: update and release

- [ ] `update/install_unix.go`: download, verify the minisign signature (unchanged), rename over the running binary (allowed on Unix), keep the old one until the next start.
- [ ] Release `athar_<v>_linux_{amd64,arm64}.tar.gz` and `athar_<v>_darwin_{amd64,arm64}.tar.gz`; asset names match `update.assetName`.
- [ ] macOS Gatekeeper: decide between a Developer ID signature with notarization and documented `xattr -d com.apple.quarantine`. Optional Homebrew tap.

## Phase 5: terminal and CI

- [ ] Arabic display: check `shaped` and `raw` in GNOME Terminal, Konsole, kitty, iTerm2 and Terminal.app; pick the default per terminal where they differ.
- [ ] Keys that some terminals do not send (`Ctrl+↑`/`Ctrl+↓`, `Shift+Enter`): add alternatives and list them in the help screen.
- [ ] CI: a `macos-latest` job running the full test suite; Linux runs the full suite instead of the portable subset once Phase 1–3 tests are OS-aware.

## Estimate and risks

About 3–4 weeks in total: Phase 0–1 ≈ 1 week, Phase 2 ≈ 1–1.5 weeks, Phase 3 ≈ 4 days, Phase 4 ≈ 3 days, Phase 5 ≈ 2 days.

- Agent locations on Linux and macOS can only be confirmed on real machines with real history (Phase 2).
- Shell quoting for `sh` is a new injection surface (Phase 3).
- Probing git roots on network mounts (`/mnt`, `/Volumes`, autofs) can be slow; Windows avoids UNC paths for the same reason. Revisit if it shows up in practice.
