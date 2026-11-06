# Changelog

All notable changes to ccresu are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [0.5.0] - 2026-11-06

### Added

- **`ccresu sessions` has an interactive browser.** On a terminal it opens a
  full-screen list with filtering (`/`), inspection (`i`), `enable`/`disable`
  (`e`/`d`), `retry` (`r`), `recover` (`R`) and `delete` (`x`, after a `y`/`N`
  prompt), so an operator no longer has to copy a session id out of the table
  first. Piped or redirected output, and `--json`, are unchanged byte for byte —
  the browser is only selected when stdout is a terminal — and `--tui`/`--no-tui`
  override the choice. The key map is in
  [docs/cli.md](docs/cli.md#browsing-the-sessions) and
  [README.md](README.md); the browser is a view over ccresu's own rows, so it
  never creates a session and its `delete` forgets the row only, leaving the
  conversation under `~/.claude` untouched.

### Changed

- **A resumed session starts the next cycle instead of ending the last one.** A
  verified resume left the row `RESUMED` with nothing to move it again, so a
  recovered conversation was reported as `none (already resumed)` and the loop ran
  exactly once. `RESUMED` now has an edge back to `DISCOVERED`, the daemon's
  rediscovery stage moves every `RESUMED` row there on its next pass with a
  recorded reason, and detection re-reads the **continuation** transcript for the
  next usage limit. `ccresu recover <id>` on a `RESUMED` session still refuses the
  dispatch with exit code 4 — there is nothing to resume until the continuation
  hits a limit — but it returns the row to `DISCOVERED` instead of leaving it
  parked, and `NextAttempt` reports `due now` rather than nothing scheduled.
  Nothing is migrated: no schema change is involved and rows already stored as
  `RESUMED` are picked up by the first pass after an upgrade, with no manual step.

### Changed — breaking

A resume is no longer a synchronous print-mode call. Old attempt rows keep their
`session_id_verified` column and their recorded command; nothing is migrated.

- **A resume is a background dispatch.** `claude.ResumeArgs` is now
  `claude --resume <session-id> --bg "<message>"` — no `-p`, no
  `--output-format`. Print mode refuses `--bg` outright — "`--bg` and `--print`
  conflict: `--print` never starts the interactive session that `claude agents`
  attaches to" — and exits 1 with nothing on stdout, so the two forms are
  mutually exclusive and only this one produces a job ccresu can follow. The
  dispatch returns at once and prints `backgrounded · <job-id>`; that eight-hex
  name is what `claude agents --all --json` and `claude stop` accept.
- **The continuation is what gets verified, not the id that was asked for.** A
  background job continues the conversation under its own session id, so the
  attempt row records that id as the *continuation* and verification requires the
  continuation's own transcript to grow before it records `RESUMED`. The API key
  `session_id_preserved` is gone; the same field is `continuation_confirmed`, and
  `ccresu inspect` prints "proven: the job named its continuation and that
  transcript grew".
- **A dispatched turn has its own state.** While the job runs the session is
  `WORKING`, taken from the agents listing, and it stays there until the job
  reports something else.
- Cost, turns and duration are no longer recorded. `--output-format json` only
  exists in print mode, and they are left empty rather than reported as zero.

### Documented

- [docs/recovery.md](docs/recovery.md#why-the-resume-no-longer-passes--p) carries
  the probe that settled this, the dispatch that replaced the print-mode call,
  and what a dispatched resume cannot report. `README.md`, `docs/cli.md`,
  `docs/architecture.md`, `docs/security.md`, `docs/tutorial.md`,
  `docs/deployment.md`, `docs/troubleshooting.md`, `docs/delivery-report.md` and
  the systemd unit say the same thing.
- The website says it too, and its command-output figures were recaptured from
  the binary: `web/lib/docs/*`, the landing page and the hero now name the
  dispatch, and `make web-captures` regenerates the figures a reader sees.

## [0.4.1] - 2026-11-06

### Documented

- **A resume is never dispatched with `--bg`**, and the documentation now says so
  with the evidence. Claude Code refuses `--bg` alongside the `-p` every resume
  carries — `--bg and --print conflict: --print never starts the interactive
  session that `claude agents` attaches to` — and exits 1 with nothing on stdout,
  so adding the flag would fail every resume as a non-retryable
  `ErrCLIUnavailable`. The form that does work drops `-p`, and with it
  `--output-format json`, the JSON result, the cost, and the proof that the same
  session id continued: the background job reports a **new** session id. Both
  transcripts, probed against Claude Code 2.1.223, are in
  [docs/recovery.md](docs/recovery.md#why-the-resume-no-longer-passes--p), and the
  refusal is also a row in [docs/troubleshooting.md](docs/troubleshooting.md).
- `TestResumeArgsNeverPassBackground` pins the decision, so a background flag
  added to `claude.ResumeArgs` fails a test instead of failing the field, and the
  comment on `ResumeArgs` carries the CLI's own refusal.
- The website's recovery page and the README's "Verified Claude Code interfaces"
  section carry the same explanation.

No behaviour change: `claude.ResumeArgs` is byte-for-byte the vector 0.4.0 ran.

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
