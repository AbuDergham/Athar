# Contributing to Athar

Thanks for helping! The most valuable contribution is **support for another agent**.

## Ground rules

- **Read-only toward agents.** An adapter never writes, creates, locks against deletion, moves or deletes anything in an agent's folders, and never runs an agent binary. Read JSON/JSONL through `internal/fsread` (`Open`, `ReadFile`, `Lines`, `ZstdLines`) and SQLite **only** through `fsread.CopySQLite` (opening an agent database in place, even read-only, writes to its `-shm` file).
- **Privacy.** Fixtures are synthetic. Never commit real conversations, titles, usernames or paths.
- **Simple.** No daemon, no network access, no new dependencies without discussion.

## Adding an agent

1. Create a package `internal/adapters/<id>/` (package name = id) with a type implementing `adapters.Adapter` (`internal/adapters/adapter.go`) and a constructor `New()`.
2. `Sources(env)` returns the files/folders to read from the agent's default locations (and its environment-variable overrides). Missing locations are skipped silently. `Source.Files` lists the files whose size and modification time make up the fingerprint.
3. `Read(ctx, env, src)` returns `model.Session`s: `NativeID` (the agent's own id), `Cwd` (the exact folder the agent resumes from), title and title source, UTC timestamps, `Coverage`, and `Messages` (user / assistant text, tool calls, the first 2 KB of tool outputs; no thinking). Bad records are counted in `Skipped`, never fatal.
4. `Caps()` describes how to resume (`Resume`, `Fork`, `SkipPermissions`, `RootEnv`) or which app opens the project (`App`).
5. Add fixtures in `internal/adapters/<id>/testdata/` and golden tests (`adapterstest.FixtureEnv`, `adapterstest.ReadAll`). JSONL adapters include a record larger than 2 MB. Add `func TestRealData(t *testing.T) { adapterstest.RealData(t, New()) }`.
6. Register the constructor in `internal/registry/registry.go`.
7. Check it against your own data without printing content: `ATHAR_REALDATA=1 go test ./internal/adapters/<id>/ -run TestRealData -v`.

## Development

```sh
go vet ./...
go test ./...
go build -o athar.exe ./cmd/athar
```

Design: `docs/superpowers/specs/2026-09-23-athar-design.md`.
