# Changelog

All notable changes to ccresu are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

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
