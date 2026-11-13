# Architecture

ccresu is one Go binary (`cmd/ccresu`) plus a set of internal packages. The same
binary is the CLI, the watchdog daemon and the local API:

```text
        ccresu daemon ──┐
           ccresu <cmd> ─┼─→ internal/app ─→ recovery / detector / scheduler / telegram
        ccresu serve ────┘                        │
                                                  ↓
                                          internal/storage (SQLite)
```

There is no server component, no database service and no web UI. Everything runs
as the user who owns the Claude Code sessions.

## Packages

| Package | Responsibility |
| --- | --- |
| `cmd/ccresu` | `main`: injects version metadata, builds the app, exits with its code. |
| `configs` | Embeds `config.example.yaml` so `ccresu config init` and the release archive ship byte-identical configuration. |
| `internal/app` | CLI surface: global flags, the command table, `--help`, exit-code mapping (`ExitInterrupted = 130`), wiring of the runtime, the `daemon`/`serve` loops. |
| `internal/config` | YAML schema, defaults, `CCRESU_*` environment overrides, path resolution, strict validation. |
| `internal/logging` | `slog` JSON/text handlers with secret redaction. |
| `internal/clock` | `Clock` interface plus real and fake implementations, so time is injectable. |
| `internal/process` | Process identity capture (PID/parent-PID from `/proc`) and a fake manager. |
| `internal/storage` | Pure-Go `modernc.org/sqlite` store: embedded migrations, WAL, repositories. |
| `internal/claude` | The only place that talks to Claude Code: discovery, transcripts, version floor, resume. |
| `internal/detector` | Normalisation plus configurable patterns that recognise a usage limit, its reset phrase and a paused goal — and refuse to classify auth, network, cancellation or completion messages as limits. |
| `internal/scheduler` | Reset-phrase parsing (AM/PM, 24h, noon/midnight, relative, `tomorrow`, explicit dates, IANA zones, DST), schedules and the unknown-reset backoff. |
| `internal/recovery` | The persisted state machine: eligibility, per-session locking, concurrency limit, backoff, the refusal parker and the verifier that proves a resume happened. |
| `internal/telegram` | Bot API client, HTML templates, seven notification events, persistent queue with delivery status, token redaction. |
| `internal/server` | REST + SSE API on `127.0.0.1:8787` (`DefaultAddr`) under `/api/v1` (`APIPrefix`), bearer token, Origin allowlist, limits. |

The runtime is assembled in one place, `internal/app/runtime.go`. The order is
load-bearing: `storage.Open`, then `store.Migrate`, then the provider, then the
scheduler and the detector, and only then the recovery manager — because the
manager takes the refusal parker, which needs a scheduler to plan with
(`runtime.go:115-148`).

## Data flow of one interruption

1. **Discovery** — `internal/claude` merges three verified sources, each labelled
with a `claude.Source` constant (`internal/claude/claude.go:48-54`): `claude
agents --json` (`SourceAgents`, `provider.go:213,233`), the transcripts under
`~/.claude/projects/<slug>/<session-id>.jsonl` (`SourceTranscript`,
`provider.go:271`), and the per-session environment directories
`~/.claude/session-env/<session-id>/` (`SourceSessionEnv`, `provider.go:273`) —
the last two read together by `Store.List` (`transcript.go:490-494`). The CLI's
entries win the merge: an on-disk record sets `Source` only when it is still
empty (`provider.go:269-274`), so a session the CLI does not report is still
discovered from its transcript, or failing that from its session-env directory.
Each session is then recorded with `UpsertSession`
(`internal/storage/sessions.go:40`). The upsert refreshes only discovery-owned
columns — paths, identity, transcript metrics, recoverability — and deliberately
leaves `state` and `enabled` alone (`sessions.go:36-39`), so a rediscovery can
neither resurrect a disabled session nor reset a recovery in progress.
2. **Detection** — the daemon's `detectIn` tails the newest
`defaultTranscriptTail = 25` transcript lines (`internal/app/daemon.go:23-27`,
used at `daemon.go:248`), joins them and hands the text to the detector. The
detector normalises it (lowercase, collapsed whitespace, folded quotes/dashes,
ANSI stripped) and scores it against the built-in patterns and any
`detector.custom_patterns`; guards subtract penalties for conditional, modal,
negated, quoted, fenced or merely *described* prose, so a sentence that talks
*about* a limit does not count as one. A confirmed match produces a
`detector.UsageLimitEvent` carrying the limit kind, the confidence, the reset
phrase, the matched rule id and whether the goal was reported paused.
3. **Recording** — `RecordUsageLimit` (`internal/storage/usagelimits.go:18`)
validates the session id, defaults an empty type to `LimitTypeOther`, rejects an
unknown type and a confidence outside `[0,1]`, and writes the banner (bounded to
2000 bytes), the plan's reset fields, the confidence, the paused flag and
`DetectedAt`. Idempotence is by comparison: the newest `usage_limits` row is the
fingerprint, and the same `reset_raw` with the same truncated `raw_message`
records nothing and sends nothing (`daemon.go:212-214`, `alreadyDetected` at
`daemon.go:317-324`).
4. **Reset resolution** — `Scheduler.PlanEvent` (`scheduler.go:162-164`) calls
`Plan`, which turns `resets 12pm (Europe/Berlin)` into an absolute instant in
that zone, independent of the host timezone. A phrase that names a zone is
resolved in that zone; an unrecognised zone is treated as unknown rather than
guessed, because guessing could shift the wake-up by hours. A failed resolution
degrades to polling instead of giving up: the schedule is marked `Unknown` with
`NextPollAt = now + initial`.
5. **Wait** — `SaveSchedule` (`internal/storage/schedules.go:21`) upserts the
row, and the session moves `USAGE_LIMIT_DETECTED → WAITING_FOR_RESET` with
reason `waiting until <RFC3339>` when the reset is known. The row, not a timer,
is the source of truth: a restart, an upgrade or a reboot mid-wait resumes from
the database. `LoadSchedules` orders the earliest deadline first and excludes
rows with no deadline at all, because such a row could never become due.
6. **Resume** — when a schedule is due, `actOnSchedules` records
`RESET_TIME_REACHED` first and then asks for a recovery
(`daemon.go:387-406`, `markResetReached` at `442-461`). The manager records the
attempt *before* it issues the resume (`StartAttempt` at `manager.go:451-454`)
and calls `claude.ResumeArgs`, whose vector is
`[]string{"-p", "--resume", sessionID, "--output-format", "json", message}`
(`internal/claude/cli.go:353-355`). The continuation message is the **last**
element because the CLI takes the prompt as a positional argument after the
flags, so it follows the session id it belongs to instead of sitting between
flags. The attempt's `command` column is a preview rendered in the same order
(`manager.go:1007-1013`) — a preview that printed the flags after the message
described a command that was never run.
7. **Verification** — `Verifier.Verify` (`internal/recovery/verify.go:66-77`)
performs two independent checks and sets `Proven` only when
`SessionIDPreserved && TranscriptGrew`. The session-id check requires that the
CLI reported an id and that it equals the baseline; the growth check compares
the transcript against the stored `transcript_size`/`transcript_lines` baseline,
accepting a line-count increase, or an unchanged line count with a larger size
when the baseline size is non-zero. A live process is reported but never used as
proof — a launched process is not a resumed conversation. A failed resume's own
writes are absorbed into the baseline by `absorbAttemptWrites`
(`manager.go:967-1001`) before any decision, otherwise the next status check
would see growth it caused itself and report a refused resume as a success.
8. **Refusal** — if the CLI ran but said the limit is still in force, the
session does **not** go to `RETRY_WAIT`: it returns to `WAITING_FOR_RESET`
through the `RESUMING → WAITING_FOR_RESET` edge, because a backoff would ask the
same question a minute later. A refusal is not a failure and is not retried.
9. **Notification** — the `deliver` stage drains the queue. Each message carries
a `dedupe_key` that is `UNIQUE` in the table, which is what stops a crash or a
restart from double-sending; the notifier can never block or fail a recovery.

## Persistence

The SQLite database (default `~/.local/share/ccresu/ccresu.db`,
`internal/config/defaults.go:74`) holds eight tables, created by embedded
migrations under `internal/storage/migrations`, plus one bookkeeping table:

| Table | Holds |
| --- | --- |
| `sessions` | one row per discovered session: `id` (the Claude Code UUID, primary key), `project_path`, `transcript_path`, `provider`, `session_type`, `pid`, `ppid`, `process_start_time`, `state`, `enabled`, `recoverable`, `reason`, `last_error`, `transcript_size`, `transcript_lines`, `retry_at`, `discovered_at`, `last_seen_at`, `updated_at` |
| `usage_limits` | each detected limit: `session_id`, `limit_type`, `raw_message`, `reset_at`, `reset_timezone`, `reset_raw`, `confidence`, `paused_goal`, `detected_at` |
| `recovery_attempts` | every resume: `session_id`, `usage_limit_id`, `attempt_number`, `state`, `command`, `started_at`, `finished_at`, `success`, `output`, `error`, `session_id_verified`, `transcript_grew` |
| `state_transitions` | the append-only audit trail: `session_id`, `attempt_id`, `from_state`, `to_state`, `reason`, `occurred_at` |
| `schedules` | the durable wait: `session_id` (primary key), `usage_limit_id`, `reset_at`, `reset_timezone`, `reset_raw`, `unknown_reset`, `attempts`, `next_poll_at`, `created_at`, `updated_at` |
| `notifications` | queued/sent/failed messages: `session_id`, `attempt_id`, `event`, `dedupe_key`, `body`, `status`, `attempts`, `last_error`, `created_at`, `updated_at`, `sent_at` |
| `settings` | operator overrides as `key`/`value`/`updated_at` (global enable/disable and the API token fingerprint) |
| `deleted_sessions` | tombstones: `id`, `project_path`, `transcript_path`, `state`, `deleted_at` |
| `schema_migrations` | bookkeeping: `version`, `name`, `checksum`, `applied_at` |

`deleted_sessions` is a tombstone rather than a soft-delete flag. Discovery
reads the transcripts on disk, so deleting only the `sessions` row would be
undone by the next pass; discovery therefore skips every id named there
(`0005_deleted_sessions.sql:4-9`, `daemon.go:156-183`). Nothing under `~/.claude`
is touched, and the paths are kept so `ccresu enable <id>` can rebuild the row
without re-reading the transcript.

Relationships are enforced by foreign keys, not by application code. A session
cascades to its limits, attempts, transitions, notifications and schedule
(`ON DELETE CASCADE` at `0001_init.sql:35,55,80,94` and
`0003_schedules.sql:10`), while a deleted `usage_limits` row nulls
`recovery_attempts.usage_limit_id` instead of orphaning it
(`0001_init.sql:56`). `recovery_attempts` also carries
`UNIQUE (session_id, attempt_number)`, so a concurrent duplicate can never be
recorded twice.

Design choices behind it:

- **Pure-Go driver.** `modernc.org/sqlite` means `CGO_ENABLED=0`, so every
release is cross-compiled and the binary has no libc requirement.
- **WAL plus a busy timeout.** The daemon, the CLI and the API can touch the
database concurrently; WAL keeps readers from blocking the writer and the busy
timeout (`storage.busy_timeout`, default 5s — `internal/storage/storage.go:58`)
absorbs the rest.
- **Foreign keys on, files restrictive.** The pragmas are set in the DSN so they
apply to every pooled connection: `busy_timeout`, `journal_mode(WAL)`,
`foreign_keys(1)`, `synchronous(NORMAL)` and `_txlock=immediate`
(`storage.go:128-139`). `foreign_keys` is per-connection and silently off by
default, which is why it is set in the DSN rather than with a one-off
`PRAGMA`; `_txlock=immediate` exists because a deferred transaction that reads
then writes must upgrade its lock, and SQLite returns `SQLITE_BUSY` for that
upgrade without invoking the busy handler. `verifyPragmas` proves on a real
connection that `journal_mode` is `wal` (except in-memory), `foreign_keys` is 1
and `busy_timeout` matches (`storage.go:182-215`); a silent failure would mean
writes are not serialised, the journal is not crash-safe, or cascades do not
fire.
- **Migrations are embedded, forward-only at runtime.** The files are compiled
into the binary (`migrationsFS`, `internal/storage/migrations.go:18-19`) "so a
single static binary carries its own schema". Each file is split at a marker
into an Up and a Down half (`splitMigration`, `migrations.go:98`). Bookkeeping
lives in `schema_migrations`, created by the runner rather than by a migration
because it must exist first (`migrations.go:115-124`). `Migrate` applies every
pending migration in order and is a no-op when run twice, which makes it safe at
every startup; each migration runs inside a transaction together with its
`schema_migrations` insert (`applyMigration`, `migrations.go:315-330`), so a
half-applied migration cannot be recorded, and `verifyChecksums` refuses to run
when an already-applied file changed (`migrations.go:296-297`). The only
production caller is `internal/app/runtime.go:123`, immediately after
`storage.Open` and before anything else is built. An upgraded binary therefore
adopts an older database automatically; an older binary cannot read a newer
schema, which is why downgrades need a backup.

## Why recovery is idempotent

Four independent guards, all in `internal/recovery` and `internal/storage`:

- **Transition validation.** `CanTransition` and `ValidateTransition`
(`state.go:166-205`) reject any edge that is not in the `transitions` map, so a
duplicate tick cannot move a session from `RESUMED` straight back into
`RESUMING`. The map is the authoritative table: 14 states, and only the edges
written there are legal. A repeat `WAITING_FOR_RESET → WAITING_FOR_RESET` is
allowed because an unknown-reset session re-enters the waiting state after every
poll and a restart re-observes the wait; moving to the state it is already in
still writes the audit row, because "the limit was checked again and was still
in force" is worth keeping (`manager.go:848-871`).
- **Per-session locking.** `manager.sessionLocks` (`manager.go:873-936`) takes a
non-blocking lock per session, so the daemon tick and a manual `ccresu recover`
cannot work the same session at once (`manager.go:289-293`).
- **Settled states.** `Settled(s)` — `RESUMED`, `COMPLETED`, `USER_TERMINATED`,
`DISABLED`, `UNRECOVERABLE` (`state.go:213-224`) — refuses further work, which is
what prevents a finished conversation from being resumed again.
- **The persisted attempt plus the unique constraint.** The attempt row is
written before the resume and its `UNIQUE (session_id, attempt_number)` index is
the arbiter, so a duplicate allocation loses in the database rather than in a
race. `StartAttempt` retries allocation up to
`maxAttemptAllocationRetries = 8` and treats the unique index as the final word
(`attempts.go:19-45`).

The state machine itself is defined once. `recovery.State` is an alias of
`storage.State` (`state.go:12-17`), so a state passes straight to the repository
with no translation layer. The happy path is
`DISCOVERED → RUNNING → USAGE_LIMIT_DETECTED → WAITING_FOR_RESET →
RESET_TIME_REACHED → CHECKING_SESSION → CHECKING_LIMIT → RESUMING → RESUMED`,
with `RETRY_WAIT` looping back to `CHECKING_SESSION`. There is no terminal
failure state: every state that permits automatic work has an edge back to
`DISCOVERED`, and entering `DISCOVERED` resets the attempt budget, so a spent
budget is a pause rather than a failure. `Terminal(s)` is true only when no edge
leaves `s` (`COMPLETED`, `USER_TERMINATED`, `UNRECOVERABLE`); `Pending(s)` is
what a restarted daemon must pick up (`USAGE_LIMIT_DETECTED`,
`WAITING_FOR_RESET`, `RESET_TIME_REACHED`, `CHECKING_SESSION`, `CHECKING_LIMIT`,
`RESUMING`, `RETRY_WAIT`).

## Concurrency and shutdown

One reconcile pass is a five-stage pipeline in fixed order — `discover`,
`detect`, `schedule`, `recover`, `deliver` (`internal/app/daemon.go:127-154`).
Every stage is best-effort: a failure is joined into the returned error and
logged, but never aborts the others, because a broken Telegram token must not
stop recoveries.

`recovery.max_concurrent_recoveries` (default 3,
`internal/config/defaults.go:49`) bounds parallel resumes through a semaphore
(`DefaultMaxConcurrent = 3`, `manager.go:19-20`, `manager.go:295-301`); excess
sessions stay in `RESET_TIME_REACHED` until a slot frees up. A per-session lock
sits in front of the semaphore so a session never occupies two slots.

`Run` starts the notifier goroutine, runs the first pass immediately so a
just-started daemon does not wait a whole interval, then loops on a timer
(`daemon.go:71-122`). The interval defaults to `DefaultDaemonInterval = 30s`
(`daemon.go:20-21`); note that `NewDaemon` overrides even an explicit
`--interval` when `claude.discovery_interval` is set (`daemon.go:47-55`), and
that key defaults to 30s.

Shutdown is cooperative: `ccresu daemon` and `ccresu serve` take a context
cancelled by `SIGINT`/`SIGTERM`, stop accepting work, and exit with code 130
(`ExitInterrupted`, `internal/app/app.go:47`). `Run` does not return until both
the notifier drain and the loop have finished (`daemon.go:65-70`, `106-114`), so
no goroutine and no database write outlive the process, and no child `claude`
process is left parentless.

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
never talks to a running daemon; it does not read `docs/` at build time.
- **No account, no cloud, no telemetry.** State stays in your database; the only
outbound traffic is the Telegram Bot API when you enable it.
- **No terminal injection.** ccresu never types into your TTY, never forks a
session and never invents a CLI subcommand.
