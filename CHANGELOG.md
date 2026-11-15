# Changelog

All notable changes to ccresu are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [0.7.0] - 2026-11-15

Six PRDs on one theme: a recovery is decided by what the supervisor publishes, not
by what ccresu infers from a transcript, a PID or its own working directory.

### Added

- **A recovery is now proven by what the supervisor says, not by a transcript.**
  `claude --bg --resume <session-id> --name ccresu-<first-8-hex> "<message>"`
  hands the continuation to Claude Code's background supervisor and returns as
  soon as the CLI prints its `backgrounded · <short-id> · <name>` block, so a
  resume outlives `ccresu` and is visible in agent view (PRD 1). Print mode —
  `-p --resume`, `--output-format json`, stop-then-resume — is gone rather than
  kept behind a flag, and the version floor is `2.1.257`.
- **`WAITING_FOR_INPUT`.** A session the supervisor publishes as `blocked` is
  parked instead of resumed: the reason it published (`waitingFor`) is carried
  verbatim, no continuation is sent, and the session is observed again rather
  than settled, so unblocking it is noticed. One `claude agents --json --all`
  query per pass serves every session in that pass (PRD 2).
- **The observation is on the attempt row.** `recovery_attempts` gains
  `observed_state`, `observed_status`, `observed_waiting_for`, `observed_at` and
  `observed_listed`, and `ccresu inspect` renders them — printing *not recorded*
  for attempts that predate the change, because "we did not look" and "nothing
  was happening" are different facts.
- **`ccresu hook` announces a session that needs you, when the CLI says so.**
  One `Notification` payload on stdin becomes one bounded
  `claude agents --json --all` observation, and at most one `needs_input`
  message — and nothing else: it always exits 0 and prints nothing, never writes
  a Claude Code configuration file, and leaves an unknown session alone. The new
  `telegram.notify_needs_input` key (default `false`) turns the message on, and
  the `attention_episodes` table gives one block one dedupe key, so the hook and
  the poll cannot announce the same block twice (PRD 4).
- **The supervisor's own log, on demand.** `ccresu inspect` gains
  `supervisor log` — the tail of `claude daemon logs` — and every pass reads
  `claude daemon status`, so an operator can see the daemon ccresu depends on
  rather than inferring it from a session that will not move (PRD 3).
- **Spend guardrails.** `recovery.guardrails` adds `max_cost_usd_per_session`
  and `max_cost_usd_per_attempt`, plus the four flags a `--bg` handoff accepts
  (`model`, `effort`, `fallback_model`, `permission_mode`). Each attempt records
  the CLI's own `total_cost_usd`, and a NULL there means *unknown*, never zero.
  A session stopped by either cap is parked in `SPEND_CAP_REACHED`; `history`
  gains a `COST` column and `inspect` shows the recorded spend against the caps
  (PRD 5).
- **Worktree-aware recovery.** `sessions.runs_in_path` records where a session
  actually runs, distinct from `project_path`, which now means only where it
  started. `sessions` gains a `RUNS IN` column, `inspect` a `runs in` line, the
  API a `runs_in_path` field, and the browser, the Telegram message and the hook
  payload carry it too. The handoff runs in the resolved directory — the recorded
  run directory, then the project directory — and never in the daemon's cwd
  (PRD 6).

### Changed

- **Transcript growth is a diagnostic, never proof.** A longer transcript
  alongside a published `blocked` no longer proves anything, and `ccresu recover`
  reports the proof it actually has ("the continuation was handed to the
  background supervisor", or the published state) instead of the retired
  "session id preserved and transcript grew" sentence. The success notification
  says the conversation continues under the supervisor for the same reason.
- **A failed listing is recorded, not read as empty.** An unreadable
  `agents --json` read is `ErrObservationUnavailable` and leaves the attempt
  open, while a session missing from a successful read is `HandoffUnobserved`
  and retried — neither is a failure verdict.
- **The lifecycle belongs to the supervisor.** `ccresu` no longer spawns, kills
  or respawns `claude`, and liveness comes from the published state plus
  `claude daemon status`. `sessions` drops `ppid` and `process_start_time`,
  which nothing ever wrote; `pid` stays as evidence and no decision reads it
  (PRD 3).
- **No `claude` invocation inherits a working directory.** The version probe,
  `agents --json` and the handoff all pass one explicitly, so a recovery can no
  longer run in `$HOME` under the shipped systemd unit and report success for it
  (PRD 6).

### Fixed

- **A blocked session can no longer be reported `RESUMED`.** The mapping covers
  every documented state, both sides of the handoff boundary, an absent entry
  and an unrecognised value; nothing falls through to success.
- **An unfinished attempt is closed as interrupted** instead of being left open
  for the next recovery to rewrite, so `history` shows what actually ended the
  attempt (PRD 3).
- **A cleared worktree no longer strands a session.** A recorded run directory
  that is no longer a directory falls back to the project directory, is named
  once in the log, and the attempt proceeds; only when neither is usable does the
  attempt fail, and then with `ErrWorkspaceUnresolved` rather than an inherited
  cwd (PRD 6).

### Documentation

- `docs/architecture.md`'s schema table now matches the migrations: `sessions` no
  longer lists `ppid` and `process_start_time` (dropped in `0009`),
  `recovery_attempts` lists `cost_usd` (`0011`), and `attention_episodes`
  (`0010`) is named among the tables.

## [0.6.0] - 2026-11-13

### Fixed

- **A resume refused by a usage limit parks the session until the reset it
  names.** `claude -p --resume` that runs and exits non-zero with a usage-limit
  banner is recognised as a refusal instead of being reported as
  `claude: claude executable unavailable`. The session moves
  `RESUMING → WAITING_FOR_RESET`, the banner is recorded as a `usage_limits` row
  (the CLI's own words, the resolved reset instant, the zone and phrase it was
  written in), the schedule is saved at that instant — or at the first poll when
  the banner names none — and `last error` carries the banner. No further resume
  is issued in the meantime: five reconcile passes inside one reset window issue
  exactly one `claude -p --resume`, and the next one is issued after the reset.
- **A refused attempt is no longer reported as `RESUMED` / "already resumed".**
  The failed attempt's own transcript writes are absorbed into the stored
  baseline, so growth the refusal itself caused can never satisfy the "the
  session had already resumed after the limit reset" shortcut.
- **A refused attempt is closed with the refusal.** The attempt row the CLI
  answered is finished (`finished`, `success: false`, error = the banner) rather
  than left open for the next recovery to rewrite as "interrupted by a process
  restart" over a parked session.
- **`ErrCLIUnavailable` means the process never started.** A `claude --version`
  probe or any other invocation that ran and exited non-zero now reports its exit
  code and captured output instead of claiming the binary is missing; the
  sentinel is reserved for a missing binary and a cancelled context.
- **The daemon only leaves a due schedule alone when the refusal planned a
  wait.** The pre-resume limit check and a refusal whose parker could not plan
  still advance the poll, so an unknown-reset schedule cannot be retried on every
  pass.

## [0.5.0] - 2026-11-13

### Added

- **`ccresu sessions` is an interactive browser on a terminal.** The table gains
  a cursor and a key map: `enter` opens the session detail page, `n` creates by
  running the daemon's discovery pass, `e`/`d` enable and disable, `r` retries,
  `R` resumes now, `x` deletes after a `y/N` confirm, `/` filters, `c` clears the
  filter, `a` toggles `--all`, `?` shows the key map and `q`/`ctrl-c` quits. Every
  action calls the same store or recovery call as the command of the same name,
  and acts on the row the cursor is on.
- `--tui` opens the browser even when stdout is not a terminal; `--no-tui` prints
  the table even when it is. `--tui --no-tui` and `--tui --json` are usage errors.
- The key map lives in `tuiKeyMap` and the help screen renders it, so the on-screen
  help, `docs/cli.md` and the README cannot drift from the code.

### Changed

- `sessions` on a terminal opens the browser instead of printing the table. Piped,
  redirected and `--json` output is byte-for-byte unchanged, and `--no-tui` on a
  terminal prints exactly the bytes a pipe would.
- The browser takes the alternate screen, hides the cursor and clears `ISIG` while
  it runs, and restores all three on every exit path — quit, `ctrl-c`, end of input
  and failure — so it cannot leave a terminal unusable.

### Fixed

- `internal/recovery/state.go` is now gofmt-clean. v0.4.0 committed the transitions
  table with map-literal alignment that `gofmt` disagreed with, so `gofmt -l .`
  reported the file. The rewrite is alignment only: no token changed.

## [0.4.0] - 2026-11-06

### Changed

- **A failed resume no longer ends in a terminal state.** The
  `PERMANENTLY_FAILED` state has been removed. A resume that fails — for any
  reason, including a non-retryable classification — returns the session to
  `DISCOVERED`.
- **A spent attempt budget also returns the session to `DISCOVERED`.** The
  budget (`recovery.max_attempts`, default 10) is now measured **per discovery
  cycle** rather than over the session's whole life. Entering `DISCOVERED` starts
  a fresh budget.
- The daemon's discovery pass re-reads a `DISCOVERED` session's transcript,
  re-plans the schedule and moves it to `WAITING_FOR_RESET` with the next reset
  time. The "already recorded this exact banner" guard is deliberately bypassed
  for a `DISCOVERED` session, otherwise a rediscovered session would be skipped
  for ever.
- `DISCOVERED` now has a legal transition to `CHECKING_SESSION`, so
  `ccresu recover` and `ccresu retry` can act on a rediscovered session.
- Every state that permits automatic work now has an edge to `DISCOVERED`:
  `DISCOVERED`, `RUNNING`, `USAGE_LIMIT_DETECTED`, `WAITING_FOR_RESET`,
  `RESET_TIME_REACHED`, `CHECKING_SESSION`, `CHECKING_LIMIT`, `RESUMING` and
  `RETRY_WAIT`. `UNRECOVERABLE` remains the only terminal failure state, because
  it is a fact about the conversation rather than a count of attempts.
- The transition reason recorded when a budget is spent is now
  `maximum attempts reached (10); returned to discovery for the next reset`.
- `NextAttempt` reports `returned to discovery; ccresu will re-read the
  transcript and schedule the next reset` for a `DISCOVERED` session.
- Documentation, the website copy and the captured command output were updated
  throughout, and the end-to-end script now asserts that the retired commands are
  gone.

### Removed

- **`ccresu reset <session-id>`** — there is no failure to clear any more.
- **`ccresu sessions clear [--dry-run]`** — there is no terminal, enabled state
  for it to sweep.
- The `PERMANENTLY_FAILED` state itself, from the state machine, the storage
  layer, the state histogram, the JSON API, the CLI and every document.

### Added

- Migration `0006_rediscover_failed_sessions.sql`, which re-opens any session
  stored as `PERMANENTLY_FAILED` as `DISCOVERED`, clears its stored error and
  writes a transition row explaining the change. The attempt history, usage
  limits, transitions and transcript are all untouched.

### Why

Recovering from a usage limit is an overnight, unattended job, but a session that
hit a transient failure during a resume — a rate limit, a dropped connection, a
background agent still shutting down — landed in a state that only a human could
clear. The attempt budget was ten, and one failure was often enough to spend it
in a way no later retry could undo. Returning the session to `DISCOVERED` makes
failure routine: the daemon reads the transcript again, finds the next reset time
and tries again, and nobody has to be awake for it.

`UNRECOVERABLE` still stops the machinery, which is correct: it means ccresu has
positively established that it has no verified way to continue that conversation.

## [0.3.0] - 2026-11-05

Initial tagged release: usage-limit detection, scheduled recovery with
verification, the retry budget, Telegram notifications, the REST/SSE API, the
`ccresu` CLI and the website.
