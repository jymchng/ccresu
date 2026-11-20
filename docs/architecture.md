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
(`runtime.go:117-211`).

## Data flow of one interruption

1. **Discovery** — `internal/claude` merges three verified sources, each labelled
with a `claude.Source` constant (`internal/claude/claude.go:49-57`): `claude
agents --json` (`SourceAgents`, `provider.go:273,293`), the transcripts under
`~/.claude/projects/<slug>/<session-id>.jsonl` (`SourceTranscript`,
`provider.go:341`), and the per-session environment directories
`~/.claude/session-env/<session-id>/` (`SourceSessionEnv`, `provider.go:343`) —
the last two read together by `Store.List` (`transcript.go:513-554`). The CLI's
entries win the merge: an on-disk record sets `Source` only when it is still
empty (`provider.go:339-345`), so a session the CLI does not report is still
discovered from its transcript, or failing that from its session-env directory.
Each session is then recorded with `UpsertSession`
(`internal/storage/sessions.go:40`). The upsert refreshes only discovery-owned
columns — paths, identity, transcript metrics, recoverability — and deliberately
leaves `state` and `enabled` alone (`sessions.go:36-39`), so a rediscovery can
neither resurrect a disabled session nor reset a recovery in progress.
2. **Detection** — the daemon's `detectIn` tails the newest
`defaultTranscriptTail = 25` transcript lines (`internal/app/daemon.go:23-27`,
used at `daemon.go:284`), joins them and hands the text to the detector. Which
sessions are read is `scannedStates` (`daemon.go:264-278`): `DISCOVERED` always,
plus `RESUMED` when `monitoring.rescan_resumed` is on — the default. `RESUMED` is
Settled, so no other pass would ever read that transcript again, and a limit that
lands after a resume is otherwise invisible for ever. A rescan can only park: it
writes `DISCOVERED → WAITING_FOR_RESET` — through `DISCOVERED`, because the
machine deliberately has no `RESUMED → WAITING_FOR_RESET` edge — and no edge
from either state reaches `RESUMING`. The
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
records nothing and sends nothing (`daemon.go:304`, `alreadyDetected` at
`daemon.go:362-369`).
4. **Reset resolution** — `Scheduler.PlanEvent` (`scheduler.go:162-164`) calls
`Plan`, which turns `resets 12pm (Europe/Berlin)` into an absolute instant in
that zone, independent of the host timezone. A phrase that names a zone is
resolved in that zone; an unrecognised zone is treated as unknown rather than
guessed, because guessing could shift the wake-up by hours. A failed resolution
degrades to polling instead of giving up: the schedule is marked `Unknown` with
`NextPollAt = now + initial`.
5. **Wait** — `SaveSchedule` (`internal/storage/schedules.go:21`) upserts the
row, and the session moves `DISCOVERED → WAITING_FOR_RESET` with
reason `waiting until <RFC3339>` when the reset is known. The row, not a timer,
is the source of truth: a restart, an upgrade or a reboot mid-wait resumes from
the database. `LoadSchedules` orders the earliest deadline first and excludes
rows with no deadline at all, because such a row could never become due.
6. **Handoff** — when a schedule is due, `actOnSchedules` asks for a recovery
(`daemon.go:414-468`). There is no "the reset time arrived" state to record
first: with seven states the pass wakes the session from whatever state it is in
(`WAITING_FOR_RESET → RESUMING`), so the intermediate beat the old machine needed
is gone. The manager records the
attempt *before* it issues the continuation (`StartAttempt` at
`manager.go:631`) and calls `claude.HandoffArgs`, whose vector is
`[]string{"--bg", "--resume", sessionID, "--name", HandoffLabel(sessionID), message}`
(`internal/claude/handoff.go:148-158`). The message is the **last** element because
the CLI takes the prompt as a positional argument after the flags, so it follows
the session id it belongs to instead of sitting between flags; `--bg` and `-p`
cannot be combined, so the print-mode vector is absent by construction. The label
is `ccresu-<first-8-hex>` derived from the session id, which makes a recovered
session identifiable in agent view without consulting the database. The attempt's
`command` column is a preview rendered in the same order
(`manager.go:1399-1406`). The CLI answers a handoff with a
`backgrounded · <short-id> · <name>` block that `claude` parses into
`Handoff{StateID, Name, Copied, …}`; a handoff that prints no block is
`ErrHandoffNotAcknowledged`, because ccresu could not then name what it created.
7. **Observation** — acceptance is the proof, and the supervisor's published
state is what happens next. `ObservationSource.Observe`
(`internal/recovery/observation.go:71`) reads one `claude agents --json --all`
per pass — always `--all`, whatever `claude.discover_all_sessions` says — indexes
it by `sessionId` (`claude.PublishedListing`, `internal/claude/published.go:62`),
and hands the entry to the total mapping `recovery.Observe`
(`internal/recovery/published.go:123`). `working` after a handoff is `RESUMED`;
`blocked` is **not** a park any more: with a handoff outstanding the continuation
landed, so the session is `RESUMED` and the prompt it waits on is the operator's
to answer, and with no handoff the pass hands it a continuation like any other
session; `done` before a handoff is `COMPLETED`; `failed` and `stopped` are
retries; an unrecognised value is a retry with the value recorded. A session
missing from the listing while a handoff is outstanding is `HandoffUnobserved` →
`WAITING_FOR_RESET`, never a failure, and a read that fails is
`ErrObservationUnavailable`, which keeps the attempt open: an outage is not a
verdict. Transcript growth is still measured, but only as a diagnostic
(`verify.go:189`), and it can never make a blocked session read as anything other
than what the supervisor published.
8. **Refusal** — if the CLI ran but said the limit is still in force, the
session does **not** go to a backoff: it returns to `WAITING_FOR_RESET`
through the `RESUMING → WAITING_FOR_RESET` edge, because a retry a minute later
would ask the same question. A refusal is not a failure and is not retried on the
attempt schedule.
9. **Notification** — the `deliver` stage drains the queue. Each message carries
a `dedupe_key` that is `UNIQUE` in the table, which is what stops a crash or a
restart from double-sending; the notifier can never block or fail a recovery.

## Persistence

The SQLite database (default `~/.local/share/ccresu/ccresu.db`,
`internal/config/defaults.go:85-89`) holds eight tables, created by embedded
migrations under `internal/storage/migrations`, plus one bookkeeping table:

| Table | Holds |
| --- | --- |
| `sessions` | one row per discovered session: `id` (the Claude Code UUID, primary key), `project_path` (where the session **started**, recorded once), `runs_in_path` (where it **actually runs**, refreshed from every observation — the two differ when a background session has moved into a git worktree), `transcript_path`, `provider`, `session_type`, `pid` (evidence only: no lifecycle decision reads it), `state`, `enabled`, `recoverable`, `reason`, `last_error`, `transcript_size`, `transcript_lines`, `retry_at`, `discovered_at`, `last_seen_at`, `updated_at` |
| `usage_limits` | each detected limit: `session_id`, `limit_type`, `raw_message`, `reset_at`, `reset_timezone`, `reset_raw`, `confidence`, `paused_goal`, `detected_at` |
| `recovery_attempts` | every continuation: `session_id`, `usage_limit_id`, `attempt_number`, `state`, `command`, `started_at`, `finished_at`, `success`, `output`, `error`, `session_id_verified`, `transcript_grew`, `cost_usd` (NULL means the CLI reported no cost, which is *unknown*, not zero), the handoff (`handoff_state_id`, `handoff_name`, `handoff_at`, `handoff_copied`) and the observation it rested on (`observed_state`, `observed_status`, `observed_waiting_for`, `observed_at`, `observed_listed`) |
| `state_transitions` | the append-only audit trail: `session_id`, `attempt_id`, `from_state`, `to_state`, `reason`, `occurred_at` |
| `schedules` | the durable wait: `session_id` (primary key), `usage_limit_id`, `reset_at`, `reset_timezone`, `reset_raw`, `unknown_reset`, `attempts`, `next_poll_at`, `created_at`, `updated_at` |
| `notifications` | queued/sent/failed messages: `session_id`, `attempt_id`, `event`, `dedupe_key`, `body`, `status`, `attempts`, `last_error`, `created_at`, `updated_at`, `sent_at` |
| `settings` | operator overrides as `key`/`value`/`updated_at` (global enable/disable and the API token fingerprint) |
| `deleted_sessions` | tombstones: `id`, `project_path`, `transcript_path`, `state`, `deleted_at` |
| `schema_migrations` | bookkeeping: `version`, `name`, `checksum`, `applied_at` |

`deleted_sessions` is a tombstone rather than a soft-delete flag. Discovery
reads the transcripts on disk, so deleting only the `sessions` row would be
undone by the next pass; discovery therefore skips every id named there
(`0005_deleted_sessions.sql:4-9`, `daemon.go:167-178`). Nothing under `~/.claude`
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
production caller is `internal/app/runtime.go:140`, immediately after
`storage.Open` and before anything else is built. An upgraded binary therefore
adopts an older database automatically; an older binary cannot read a newer
schema, which is why downgrades need a backup.

- **Old state names are migrated at startup, not tolerated at runtime.** The
  seven states are a subset of the sixteen, so migration
  `0013_simplify_states.sql` rewrites every row that still carries a retired name
  — in `sessions`, `recovery_attempts`, `state_transitions` and
  `deleted_sessions` — through one explicit many-to-one table: a detected limit
  and a reached reset time both become `WAITING_FOR_RESET`, the two pre-flight
  checks become `RESUMING`, `RUNNING` becomes `RESUMED`, a backoff and a
  `blocked` park become `DISCOVERED`, and `USER_TERMINATED` and
  `SPEND_CAP_REACHED` become `DISABLED`. Nothing is deleted: the conversation,
  the attempt history and the transition timeline all survive. The rows are
  migrated rather than filtered because a retired name is a state the rest of the
  tool no longer accepts: the audit write path rejects any other name
  (`internal/storage/transitions.go:12-15`), the API's state filters accept only
  the seven (`internal/server/dto.go:346`), and `ccresu history` prints the
  stored names verbatim, so an unmigrated timeline would be history in a
  vocabulary nothing else speaks. The reverse is not possible and
  is not attempted: after the collapse nothing records whether a given
  `WAITING_FOR_RESET` was once `USAGE_LIMIT_DETECTED` or `RESET_TIME_REACHED`, and
  writing one of them back would put a session in a state it was never in.

## Why recovery is idempotent

Four independent guards, all in `internal/recovery` and `internal/storage`:

- **Transition validation.** `CanTransition` and `ValidateTransition`
(`state.go:160-209`) reject any edge that is not in the `transitions` map, so a
duplicate tick cannot move a session from `RESUMED` straight back into
`RESUMING`. The map is the authoritative table: seven states, and only the edges
written there are legal. A repeat `WAITING_FOR_RESET → WAITING_FOR_RESET` is
allowed because an unknown-reset session re-enters the waiting state after every
poll and a restart re-observes the wait; moving to the state it is already in
still writes the audit row, because "the limit was checked again and was still
in force" is worth keeping (`manager.go:1238-1256`).
- **Per-session locking.** `manager.sessionLocks` (`manager.go:1261-1314`) takes a
non-blocking lock per session, so the daemon tick and a manual `ccresu recover`
cannot work the same session at once (`manager.go:410`).
- **Settled states.** `Settled(s)` — `RESUMED`, `COMPLETED`, `DISABLED`,
`UNRECOVERABLE` (`state.go:233-240`) — refuses further work, which is what
prevents a finished conversation from being resumed again, and what stops a
just-resumed session from being resumed off its own stale banner.
- **The persisted attempt plus the unique constraint.** The attempt row is
written before the resume and its `UNIQUE (session_id, attempt_number)` index is
the arbiter, so a duplicate allocation loses in the database rather than in a
race. `StartAttempt` retries allocation up to
`maxAttemptAllocationRetries = 8` and treats the unique index as the final word
(`attempts.go:21-51`).

The state machine itself is defined once. `recovery.State` is an alias of
`storage.State` (`state.go:17`), so a state passes straight to the repository
with no translation layer. One lap is four beats and it repeats for as long as
the session keeps hitting its limit:

```text
DISCOVERED → WAITING_FOR_RESET → RESUMING → RESUMED → DISCOVERED
```

`DISCOVERED` is the transcript read, `WAITING_FOR_RESET` is the durable wait on
the instant a banner named, `RESUMING` is the one attempt spent handing the
backgrounded session a continuation, and `RESUMED` is the landing beat — whose
edge back to `DISCOVERED` is what makes the loop a loop. There is no terminal
failure state: every beat that permits automatic work has an edge back to
`DISCOVERED`, and entering `DISCOVERED` resets the attempt budget, so a spent
budget is a pause rather than a failure. `Terminal(s)` is true for `COMPLETED`,
`DISABLED` and `UNRECOVERABLE` (`DISABLED` is terminal to every automatic caller
and has exactly one edge, back to `DISCOVERED`, which only `ccresu enable`
takes); `Pending(s)` — what a restarted daemon picks up — is `WAITING_FOR_RESET`
and `RESUMING`; `Failed(s)` is `UNRECOVERABLE` alone.

Two edges exist only so a limit can be noticed in a session that is otherwise
finished with: `DISCOVERED → RESUMED` and `WAITING_FOR_RESET → RESUMED` record a
session the supervisor found alive again on its own once the window reset. They
are observations from the published state, not resumes — no attempt is spent and
no continuation is handed over. A rescan of a `RESUMED` session takes
`RESUMED → DISCOVERED → WAITING_FOR_RESET`, and `ccresu search-limit` takes the
same two on the operator's behalf. They are parks, not resumptions — neither
reaches `RESUMING` — and `RESUMED` still has no direct edge to `WAITING_FOR_RESET`,
so the observation that produced the wait is always its own `state_transitions`
row rather than an inference from the row before it.

## Concurrency and shutdown

One reconcile pass is a six-stage pipeline in fixed order — `discover`,
`detect`, `schedule`, `recover`, `complete`, `deliver`
(`internal/app/daemon.go:127-155`).
Every stage is best-effort: a failure is joined into the returned error and
logged, but never aborts the others, because a broken Telegram token must not
stop recoveries.

Two stages the old machine needed are gone with the states they served. `unpark`
existed to release sessions parked in a spend-cap state after the operator raised
a cap; a spend stop is now `DISABLED`, and the way back is `ccresu enable`, which
is a human act and therefore cannot be a stage. `markResetReached` existed to
move a waiting session into a "reset time arrived" state; a due schedule now
wakes the session straight into `RESUMING`, so there is no beat to record first.

`recovery.max_concurrent_recoveries` (default 3,
`internal/config/defaults.go:59`) bounds parallel resumes through a semaphore
(`DefaultMaxConcurrent = 3`, `manager.go:19-20`, `manager.go:214`); excess
sessions stay in `WAITING_FOR_RESET` until a slot frees up. A per-session lock
sits in front of the semaphore so a session never occupies two slots.

`Run` starts the notifier goroutine, runs the first pass immediately so a
just-started daemon does not wait a whole interval, then loops on a timer
(`daemon.go:71-122`). The interval defaults to `DefaultDaemonInterval = 30s`
(`daemon.go:20-22`); note that `NewDaemon` overrides even an explicit
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
