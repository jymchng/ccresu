# Architecture

ccresu is one Go binary (`cmd/ccresu`) plus a set of internal packages. The same
binary is the CLI, the watchdog daemon and the local API:

```text
        ccresu daemon ──┐
           ccresu <cmd> ─┼─→ internal/app ─→ recovery / detector / scheduler / telegram
        ccresu serve ───┘                        │
                                                  ↓
                                          internal/storage (SQLite)
```

There is no server component, no database service and no web UI. Everything runs
as the user who owns the Claude Code sessions.

## Packages

| Package | Responsibility |
| --- | --- |
| `cmd/ccresu` | `main`: injects version metadata, builds the app, exits with its code. |
| `internal/app` | CLI surface: global flags, the command table, `--help`, exit-code mapping, wiring of the runtime, `daemon`/`serve` loops. |
| `internal/config` | YAML schema, defaults, `CCRESU_*` environment overrides, path resolution, strict validation. |
| `internal/logging` | `slog` JSON/text handlers with secret redaction. |
| `internal/clock` | `Clock` interface plus real and fake implementations, so time is injectable. |
| `internal/process` | Process identity capture (PID/parent-PID from `/proc`) and a fake manager. |
| `internal/storage` | Pure-Go `modernc.org/sqlite` store: embedded migrations, WAL, repositories. |
| `internal/claude` | The only place that talks to Claude Code: discovery, transcripts, version floor, resume. |
| `internal/detector` | Normalisation plus configurable patterns that recognise a usage limit, its reset phrase and a paused goal — and refuse to classify auth, network, cancellation or completion messages as limits. |
| `internal/scheduler` | Reset-phrase parsing (AM/PM, 24h, noon/midnight, relative, `tomorrow`, explicit dates, IANA zones, DST), schedules and the unknown-reset backoff. |
| `internal/recovery` | The persisted state machine: eligibility, per-session locking, concurrency limit, backoff, and the verifier that proves a resume happened. |
| `internal/telegram` | Bot API client, HTML templates, six/seven event types, persistent queue with delivery status, token redaction. |
| `internal/server` | REST + SSE API on `127.0.0.1:8787` under `/api/v1`, bearer token, Origin allowlist, limits. |

## Data flow of one interruption

1. **Discovery** — `internal/claude` reads `claude agents --json` and the
transcripts under `~/.claude/projects/<slug>/<session-id>.jsonl`, and records
each session in `sessions`.
2. **Detection** — `internal/detector` normalises the last output (lowercase,
collapsed whitespace, folded quotes/dashes) and matches it against the built-in
patterns and any `detector.custom_patterns`. A match produces a
`UsageLimitEvent` carrying the limit kind, the confidence, the reset phrase, the
matched rule id and whether the goal was reported paused.
3. **Reset resolution** — `internal/scheduler` turns `resets 12pm
   (Europe/Berlin)` into an absolute instant in that zone, independent of the
   host timezone, and persists a row in `schedules`.
4. **Wait** — the row, not a timer, is the source of truth. A restart, an
   upgrade or a reboot mid-wait resumes from the database.
5. **Resume** — `internal/recovery` walks the state machine, calls
   `claude -p --resume <session-id> --output-format json "<continuation message>"`, and records an attempt.
6. **Verification** — the verifier re-reads the transcript and requires both that
the reported session id equals the original and that the transcript grew
before the attempt counts as a success.
7. **Notification** — `internal/telegram` drains the notification queue; it can
never block or fail a recovery.

## Persistence

The SQLite database (default `~/.local/share/ccresu/ccresu.db`) holds seven
tables, created by embedded migrations under `internal/storage/migrations`:

| Table | Holds |
| --- | --- |
| `sessions` | one row per discovered session: state, project path, type, recovery code, attempt count |
| `usage_limits` | each detected limit: kind, confidence, matched rule, raw phrase, resolved reset instant and zone |
| `recovery_attempts` | every resume: success, session-id-preserved, transcript-grew, error, timing |
| `state_transitions` | the audit trail: from-state, to-state, reason, timestamp |
| `schedules` | the durable wait: reset instant, zone, raw phrase, due time |
| `notifications` | queued/sent/failed messages with the event they carry |
| `settings` | operator overrides (`enable`/`disable` and similar) |

Design choices behind it:

- **Pure-Go driver.** `modernc.org/sqlite` means `CGO_ENABLED=0`, so every
release is cross-compiled and the binary has no libc requirement.
- **WAL plus a busy timeout.** The daemon, the CLI and the API can touch the
database concurrently; WAL keeps readers from blocking the writer and the busy
timeout (`storage.busy_timeout`, default 5s) absorbs the rest.
- **Foreign keys on, files restrictive.** Sessions cascade to their limits,
attempts and transitions. The database and its directory are created with
restrictive permissions.
- **Migrations are forward-only at runtime.** They are applied on open, so an
upgraded binary adopts an older database automatically; an older binary cannot
read a newer schema, which is why downgrades need a backup.

## Why recovery is idempotent

Three independent guards, all in `internal/recovery`:

- **Transition validation.** `CanTransition`/`ValidateTransition` reject any edge
that is not in the state map, so a duplicate tick cannot move a session from
`RESUMED` straight back into `RESUMING`.
- **Per-session locking.** Two loops (daemon tick, manual `ccresu recover`) cannot
work the same session at once.
- **Settled states.** `RESUMED`, `COMPLETED`, `USER_TERMINATED`, `DISABLED` and
`UNRECOVERABLE` are settled: recovery refuses them, which is what prevents a
finished conversation from being resumed again.

## Concurrency and shutdown

`recovery.max_concurrent_recoveries` (default 3) bounds parallel resumes;
excess sessions stay in `RESET_TIME_REACHED` until a slot frees up.
`ccresu daemon` and `ccresu serve` take a context cancelled by `SIGINT`/`SIGTERM`,
stop accepting work, and exit with code 130. No goroutine outlives the process
and no child `claude` process is left parentless.

## Testing seams

Every external effect has an injected implementation: `clock.Clock` for time,
`claude.Execer` for process execution, `claude.FakeProvider` and
`storage.Store` for the store, `telegram.Sender` for HTTP delivery, and
`recovery.Store`/`Provider` interfaces for the state machine. `tests/support`
bundles those into a real-SQLite fixture, and `tests/e2e` drives the whole
scenario with nothing stubbed at the seams it proves.

## Deliberate non-goals

- **No web UI.** The binary ships the CLI, the daemon and a loopback REST/SSE
API. The Next.js site in `web/` is a marketing and documentation site that
never talks to a running daemon.
- **No account, no cloud, no telemetry.** State stays in your database; the only
outbound traffic is the Telegram Bot API when you enable it.
- **No terminal injection.** ccresu never types into your TTY, never forks a
session and never invents a CLI subcommand.
