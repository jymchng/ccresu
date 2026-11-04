# ccresu

[![Website](https://img.shields.io/badge/website-jymchng.github.io%2Fccresu-2ea44f)](https://jymchng.github.io/ccresu/)
[![Docs](https://img.shields.io/badge/docs-read%20the%20manual-blue)](https://jymchng.github.io/ccresu/docs/)
[![CI](https://github.com/jymchng/ccresu/actions/workflows/ci.yml/badge.svg)](https://github.com/jymchng/ccresu/actions/workflows/ci.yml)

**Website and documentation: <https://jymchng.github.io/ccresu/>**

Automatically resume your original Claude Code session after a usage limit resets.

`ccresu` watches Claude Code sessions, detects usage-limit interruptions, extracts
the reset time and its timezone, waits for the reset, and resumes **the same**
session — preserving conversation history, working directory and active goal —
then verifies the recovery actually happened. Telegram notifications are optional.

```text
You've hit your 5-hour limit · resets 12pm (Europe/Berlin)
Goal paused · usage limit reached · send a message after it resets
```

## Documentation

The documentation is published at **<https://jymchng.github.io/ccresu/docs/>**,
together with the landing page at <https://jymchng.github.io/ccresu/>. The same
material is kept in this repository under [`docs/`](docs/README.md), so it is
readable offline and reviewable in pull requests.

Hosted pages: [installation](https://jymchng.github.io/ccresu/docs/installation/)
· [configuration](https://jymchng.github.io/ccresu/docs/configuration/)
· [CLI](https://jymchng.github.io/ccresu/docs/cli/)
· [recovery](https://jymchng.github.io/ccresu/docs/recovery/)
· [Telegram](https://jymchng.github.io/ccresu/docs/telegram/)
· [deployment](https://jymchng.github.io/ccresu/docs/deployment/)
· [security](https://jymchng.github.io/ccresu/docs/security/)
· [troubleshooting](https://jymchng.github.io/ccresu/docs/troubleshooting/)
· [known limitations](https://jymchng.github.io/ccresu/docs/limitations/)

The in-repository sources:

| Document | Read it for |
| --- | --- |
| [docs/README.md](docs/README.md) | Overview and quick start |
| [docs/cli.md](docs/cli.md) | Every command, flag and exit code |
| [docs/configuration.md](docs/configuration.md) | Every setting, its default and its environment override |
| [docs/recovery.md](docs/recovery.md) | Detection, scheduling, resumption and the state machine |
| [docs/architecture.md](docs/architecture.md) | How the pieces fit together |
| [docs/api.md](docs/api.md) | The local REST and SSE API |
| [docs/telegram.md](docs/telegram.md) | Notification setup, delivery guarantees and troubleshooting |
| [docs/deployment.md](docs/deployment.md) | Running it as a service, logs, monitoring |
| [docs/upgrade.md](docs/upgrade.md) | Installing, upgrading and uninstalling |
| [docs/security.md](docs/security.md) | What it can touch, and what it keeps secret |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Symptom-to-cause tables, and known limitations |
| [docs/development.md](docs/development.md) | Layout, seams, tests, conventions |
| [docs/releases.md](docs/releases.md) | Tagging, assets, checksums |
| [docs/website.md](docs/website.md) | The static landing and documentation site |

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
- A static landing page and documentation site at
  <https://jymchng.github.io/ccresu/>, deployed to GitHub Pages.

`ccresu` ships **no web UI**: the binary is the CLI, the daemon and the API.

## Development

```bash
go build ./...
go test ./... && go test -race ./...
make cover-check     # enforces the 80% statement-coverage gate
```

## License

[MIT](LICENSE)
