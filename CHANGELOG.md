# Changelog

All notable changes to ccresu are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [0.9.0] - 2026-11-20

The state machine shrank from sixteen states to seven. What ccresu does did not
change — it reads a transcript, waits for the reset the banner named, hands the
session a continuation and goes back to reading — but the machine now says
exactly that. The seven that remain are the beats of that lap plus the three
verdicts that stop it; the nine that went were detours *inside* a beat, each one
a step ccresu took while it was already doing something else. Existing databases
are migrated on first start, so an upgrade asks nothing of the operator but a
restart — apart from the one thing 0.7.0 told you to install: `ccresu hook` no
longer exists, so remove that hook from your Claude Code settings (see
Removed).

### Added

- **Startup migration `0013_simplify_states.sql`.** An upgraded binary rewrites
  every row that still carries a retired state name through one explicit
  many-to-one table, in `sessions`, `recovery_attempts`, `state_transitions` and
  `deleted_sessions`. Nothing is deleted: the conversation, the attempt history
  and the transition timeline all survive, because a session still waiting for
  its reset has to be able to show what was already tried. The rows are
  rewritten rather than filtered because a retired name would be a state the
  rest of the tool no longer accepts: the audit write path rejects any other
  name (`unknown transition target state`), the API's state filters accept only
  the seven, and `ccresu history` prints the stored names verbatim, so an
  unmigrated timeline would show history in a vocabulary nothing else speaks. A
  session arriving at `DISCOVERED` from `RETRY_WAIT` or `WAITING_FOR_INPUT` also
  loses its `retry_at`, because `DISCOVERED` owns no retry instant and
  `ccresu sessions` would otherwise show it in `NEXT ATTEMPT` (as would
  `ccresu inspect`) for a wait nothing would act on. The migration runs in the
  same transaction as its `schema_migrations` row, like every other one, and its
  `Down` section is a deliberate no-op: a binary rolled back to 0.8.0 reads every
  migrated row without complaint, but the distinction the collapse removed cannot
  be restored.
- **Startup migration `0014_drop_attention_episodes.sql`.** The
  `attention_episodes` table existed for one purpose: to give the `needs_input`
  message an identity both of its producers could agree on, and neither producer
  exists any more (see Removed). Keeping it would ship a table whose only writer
  is a prune that deletes everything, so the table and its index are dropped. No
  session, attempt or transition row is touched, and `ccresu inspect` never
  rendered an episode. Its `Down` section recreates the table exactly as `0010`
  created it, empty: the rows were bookkeeping for messages already delivered,
  and a stale episode would suppress the first message of a new block rather than
  restore it.

### Changed

- **Seven states, one lap, three verdicts.** The survivors are `DISCOVERED`,
  `WAITING_FOR_RESET`, `RESUMING`, `RESUMED`, `COMPLETED`, `DISABLED` and
  `UNRECOVERABLE`. `DISCOVERED → WAITING_FOR_RESET → RESUMING → RESUMED →
  DISCOVERED` is the whole loop, and it repeats for as long as the session keeps
  hitting its limit. Neither way out of a lap is a verdict about the
  conversation: a spent attempt budget returns the session to `DISCOVERED`,
  where the next discovery pass reads the transcript again and starts a fresh
  budget, and a failure worth retrying parks it in `WAITING_FOR_RESET` with the
  next try on `sessions.retry_at`. The four beats no longer have states between
  them: the two pre-flight checks happen inside `RESUMING`, and a due schedule
  wakes a parked session straight into it.
- **A blocked session is resumed, not parked.** When the supervisor publishes
  `blocked` with a handoff outstanding, the continuation landed and the session
  is `RESUMED`: the question it is waiting on is the operator's to answer,
  whenever they get to it, and it is not evidence that the resume failed. With
  no handoff outstanding the pass hands it a continuation like any other
  session, because a continuation does not have to wait for an unanswered
  question. What the supervisor said is still recorded verbatim, in the
  attempt's observation and in the transition reason — it is simply no longer a
  reason to stop.
- **A backoff is an instant on the row, not a state.** A failure worth retrying
  stays in `WAITING_FOR_RESET` with its next try on `sessions.retry_at`, and
  `ccresu sessions` shows it in the `NEXT ATTEMPT` column while `ccresu inspect`
  prints it as the next attempt, rather than a state named after it. A real
  reset stays authoritative when both exist, so a session parked on a banner is
  woken by the banner.
- **Finding a limit in a resumed session still cannot spend.** `RESUMED` has no
  edge to `RESUMING` and no direct edge to `WAITING_FOR_RESET`, so the rescan
  and `ccresu search-limit` park it through `DISCOVERED` (`RESUMED → DISCOVERED
  → WAITING_FOR_RESET`). The guard survives the collapse on purpose: without it
  a just-resumed session would be parked and resumed off its own stale banner.

### Removed

- **Nine states, and the edges that led to them.** `USAGE_LIMIT_DETECTED` and
  `RESET_TIME_REACHED` are `WAITING_FOR_RESET` (detecting a limit *is* parking
  on the instant it named); `CHECKING_SESSION` and `CHECKING_LIMIT` are
  `RESUMING`; `RUNNING` is `RESUMED` (a session that is alive after a resume,
  still scanned, which is how the next limit is noticed); `RETRY_WAIT` and
  `WAITING_FOR_INPUT` are `DISCOVERED`; `USER_TERMINATED` and
  `SPEND_CAP_REACHED` are `DISABLED` (both mean only `ccresu enable` restarts
  it). The edges went with them, so a transition that used to be legal is now
  refused with `recovery: invalid state transition`, and one that names a
  retired state is refused as `"RUNNING" is not a known state` rather than
  treated as an edge out of the machine.
- **The needs-input notification path, and the command that fed it.** A
  `blocked` session is resumed like any other, so nothing can honestly say
  "waiting for you" any more: `needs_input` is no longer an event the daemon can
  queue, `telegram.notify_needs_input` is gone with the attention-episode
  bookkeeping that paced it, and the `ccresu hook` subcommand that 0.7.0 told
  operators to install as a Claude Code `Notification` hook has been deleted: a
  settings entry that still runs it now reports an unknown command on every
  notification (`ccresu: unknown command "hook"`), so delete the hook. It
  existed to announce one thing, the needs-input message, and that message no
  longer exists. The config key is *retired*, not unknown: a `config.yaml` that still sets `telegram.notify_needs_input` loads,
  the loader drops it before the strict decode, and the load says once that it is
  ignored — every other unknown key, including a typo of this one, is still
  refused. Delete the line when you see the warning.

## [0.8.0] - 2026-11-20

A usage limit is no longer the one interruption ccresu can miss. The daemon now
re-reads the transcripts of the sessions it had stopped looking at, and an
operator who knows a session has gone idle can ask for that read by hand — from
the CLI, or with one keystroke in the sessions browser.

### Added

- **`monitoring.rescan_resumed`, on by default.** The detect stage now reads the
  transcripts of `RESUMED`, `RETRY_WAIT` and `WAITING_FOR_INPUT` sessions as well
  as `DISCOVERED` and `RUNNING`. Each of the three was invisible to every pass:
  `RESUMED` is Settled, so nothing revisits it; `RETRY_WAIT` is counting down a
  backoff that would retry into the same refusal; `WAITING_FOR_INPUT` is parked
  on a human. A limit found in one of them parks the session in
  `WAITING_FOR_RESET` on the reset the banner names, which is the only thing a
  rescan can do — it never resumes and never spends. Set the key to `false` (or
  `CCRESU_MONITORING__RESCAN_RESUMED=false`) to restore the previous behaviour.
- **`ccresu search-limit <session-id> [--tail N]`**, and the same verb as
  `ccresu sessions search-limit <session-id>` for an operator who is already in
  that namespace. It reads the newest `N` transcript lines (default 25, the same
  window the daemon's detector reads) and, when it recognises a banner, records
  it and parks the session — the same `USAGE_LIMIT_DETECTED →
  WAITING_FOR_RESET` walk the daemon takes, so the audit trail cannot tell the
  two apart. A search that finds nothing is an answer, not a failure: exit 0,
  nothing written, and a line saying the session was left alone. An unknown id is
  exit 3; a session that is neither `RESUMED` nor `RUNNING` is refused with exit
  4, because every other state either already has a pass that will read the
  transcript or is a decision ccresu must not walk back.
- **`L` in the sessions browser** runs that search on the selected session and
  says which of the two outcomes happened — `usage limit found, state
  WAITING_FOR_RESET` with the reason, which names the reset time when the banner
  gave one, or `no usage limit found, state unchanged`. It is in the key map, the
  list footer and the `?` screen.

### Changed

- **Three new state edges, and all three of them park.** `RESUMED`,
  `RETRY_WAIT` and `WAITING_FOR_INPUT` each gain an edge to
  `USAGE_LIMIT_DETECTED`, which reaches `WAITING_FOR_RESET` as before. None of
  the three reaches `RESUMING`: finding a limit parks a session, it never spends
  on one. `RESUMED` still has no direct edge to `WAITING_FOR_RESET`, so the
  observation that produced the wait is always in the transition history rather
  than being implied by it.

### Fixed

- **A limit that landed in a resumed session no longer strands it.** The session
  sat in `RESUMED` for ever while the daemon ran happily next to it. Not looking
  was deliberate — it is what stops a finished conversation being resumed twice —
  so the read is either automatic behind the new switch or an explicit operator
  act, never a silent second resume.

## [0.7.0] - 2026-11-15

Six PRDs on one theme: a recovery is decided by what the supervisor publishes, not
by what ccresu infers from a transcript, a PID or its own working directory.

A seventh document rides with them, and it is a defect rather than an
enhancement: the sessions browser ran `claude` on the goroutine that reads the
keyboard, so `enter` painted nothing until the CLI answered, the keys pressed
meanwhile were applied to the screen that answer produced, and `ctrl-c` could not
cancel anything. The browser now answers the keyboard first and the CLI second
(PRD 7).

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

- **The sessions browser answers the keyboard, not `claude`.** Four of its keys
  run a command that invokes the CLI, and every one of them ran on the goroutine
  that reads the keyboard: `enter` was silent for as long as
  `claude daemon status` took (0.9–2.4 s measured, 30 s per call when the
  supervisor was wedged, and unbounded when a descendant held the output pipes),
  and the keys pressed meanwhile were buffered by the terminal and then applied
  to the screen the command produced — a second `enter` opened the detail page
  and closed it before it could be read. The work now runs on its own goroutine,
  the browser paints `busy: discovering… (ctrl-c cancels)` while it does, the
  keys that start work are refused rather than queued, and `ctrl-c` cancels the
  command without waiting for it and leaves the browser open (PRD 7).
- **The detail page is painted before the supervisor is asked.** `enter` renders
  the session's facts from ccresu's own store from the keystroke, and the
  supervisor block — `claude daemon status`, `claude daemon logs`,
  `claude logs <id>` — is read behind the page and filled in when it answers.
  A read that misses its five-second budget says so in one line, and the rest of
  the page is the page `sessions status` prints, byte for byte, when it answers.
- **`esc` works on its own.** A lone escape was decided only when the next
  keystroke arrived, because decoding an escape sequence means looking ahead and
  a terminal sends ESC and then waits. The decoder keeps the escape pending and a
  50 ms timer decides it, so the key the detail screen advertises is the key the
  detail screen answers. `os.Stdin` on a Linux terminal cannot carry a read
  deadline (“file type does not support deadline”), which is why the bound is a
  timer rather than a deadline.
- **A status line belongs to the screen that wrote it.** Leaving the detail
  screen no longer paints its `enter, q or esc` footer on the list, which is how
  a key that had worked came to look like a key that had not.
- **A call that misses its deadline returns.** `exec.CommandContext` kills the
  child, but `Wait` also waits for the output pipes, and a descendant that
  inherited them keeps them open: a stand-in that slept in a child left the
  browser with no frame after 45 s where the same stand-in sleeping in its own
  process produced one at exactly the 30 s query timeout. `OSExecer` now sets
  `cmd.WaitDelay`, so the bound holds whatever the child leaves behind.
- **The terminal is restored when the browser is signalled.** `SIGTERM`,
  `SIGINT` and `SIGHUP` now end the browser's loop so its deferred restore runs,
  instead of killing the process mid-paint. `SIGKILL` cannot be caught, so
  `docs/cli.md` names `reset` as the remedy for the one exit path that needs the
  operator — and the command in flight is cancellable, so killing the browser is
  never the right way to stop one.
- **`recovery.startup_timeout` has a reader.** The key was accepted, validated,
  defaulted to `60s` and environment-overridable, and consumed by nothing: every
  handoff ran under the fixed `claude.DefaultHandoffTimeout` whatever the
  configuration said. It now bounds the `claude --bg --resume` handoff call
  itself (and still not the resumed work, which is the supervisor's), and its
  default moved to `2m` — the value that call already used — so an existing
  configuration behaves as before.
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
- `docs/cli.md` describes the browser as it now behaves: the busy line, `ctrl-c`
  as the cancel key, the two-part detail page, the detail screen's own key map,
  `esc`, and `reset` as the remedy for a `SIGKILL`ed browser.
- `prds/unresponsive-sessions-browser.md` is the defect PRD for the browser, and
  `prds/README.md` §8 indexes it beside the six it does not belong to.

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
