# ccresu

Automatically resume your original Claude Code session after a usage limit resets.

`ccresu` watches Claude Code sessions, detects usage-limit interruptions, extracts
the reset time and its timezone, waits for the reset, and resumes **the same**
session — preserving conversation history, working directory and active goal —
then verifies the recovery actually happened. Telegram notifications are optional.

```text
You've hit your 5-hour limit · resets 12pm (Europe/Berlin)
Goal paused · usage limit reached · send a message after it resets
```

## Status

This repository is under active construction. The sections below are filled in
as each component lands; see `docs/` for the authoritative documentation.

## Verified Claude Code interfaces

ccresu only uses interfaces that exist in the installed CLI (inspected with
`claude --help` / `claude agents --help`, currently Claude Code 2.1.223):

| Purpose | Interface |
|---|---|
| Discover sessions | `claude agents --json` (`--all`, `--cwd <path>`) |
| Session transcripts | `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl` |
| Per-session environment | `~/.claude/session-env/<session-id>/` |
| Resume the original session | `claude -p --resume <session-id> "<continuation message>"` |

No undocumented commands are used, and no new session is ever started in place
of the original one. See [docs/recovery.md](docs/recovery.md) for the exact
mechanism and its limitations.

## Interfaces

- `ccresu` — CLI and watchdog daemon.
- Local REST + SSE API on `127.0.0.1:8787` for scripting and automation.
- A static landing page and documentation site, deployed to GitHub Pages.

`ccresu` ships **no web UI**: the binary is the CLI, the daemon and the API.

## Development

```bash
go build ./...
go test ./... && go test -race ./...
make cover-check     # enforces the 80% statement-coverage gate
```

## License

[MIT](LICENSE)
