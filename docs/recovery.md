# How a resume actually happens

This is the part of ccresu that must not be hand-waved: a "recovery" is only a
recovery if the **original conversation** continued. Everything below is what the
code does, and what the end-to-end test asserts.

## Verified interfaces only

ccresu is verified against Claude Code **2.0.0 or newer**
(`claude.MinimumVersion`); an older CLI is refused rather than guessed at. It uses
exactly four interfaces:

| Purpose | Interface |
| --- | --- |
| Discover sessions | `claude agents --json`, with `--all` and `--cwd <path>` |
| Session transcript | `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl` |
| Per-session environment | `~/.claude/session-env/<session-id>/` |
| Resume the original session | `claude -p --resume <session-id> "<message>" --output-format json` |

The resume vector is built in one place, `claude.ResumeArgs`, and is:

```go
[]string{"-p", "--resume", sessionID, message, "--output-format", "json"}
```

There is no `--fork-session`, no new session id, no TTY, no keystroke injection
into your terminal, and no invented subcommand. The message is limited to 8 KiB,
must be non-empty, and the session id is validated before anything is executed.

## Discovery and detection

1. `claude agents --json` is parsed defensively: unknown field names, extra
   fields and missing optional fields are tolerated, and each session records
   which source found it (`agents`, `transcript` or `session-env`).
2. Unsupported session types are reported as `UNRECOVERABLE` with a reason such
   as `unsupported session type "..." reported by claude agents --json`. ccresu
   never invents a command for a session it does not understand.
3. The session's output is normalised — lowercased, whitespace collapsed, curly
   quotes and dashes folded to ASCII — so patterns survive wrapping and
   typography.
4. Built-in rules (ids such as `canonical-hit-five-hour`,
   `canonical-reached-weekly`, `paused-goal-usage-limit`, `limit-resets-in`)
   plus any `detector.custom_patterns` are matched. A rule match carries a
   confidence; guard rules lower it when the text looks like prose *about* a
   limit ("you will hit the limit if…") rather than an interruption, and the
   configured threshold decides.
5. Explicit non-classifications exist so the wrong thing never happens: auth
   errors, network errors, invalid API keys, user cancellation and normal
   completion are recognised as *not* usage limits. A cancellation is never
   resumed; a completed session is never resumed.
6. The paused-goal companion message (`Goal paused · usage limit reached · send
   a message after it resets`) is detected independently and recorded, which is
   why a continuation message is sent at all.

## Reset-time resolution

`resets 12pm (Europe/Berlin)` resolves to 12:00 in **Europe/Berlin**, not in the
host's timezone. On 2026-03-02 (Berlin = UTC+1) that is 11:00Z, and the stored
row keeps both the UTC instant and the zone name so `ccresu history` can explain
the wait.

The parser accepts AM/PM and 24-hour times, noon and midnight, relative
durations, `tomorrow`, explicit dates, and IANA zone names, and it handles DST
transitions and ambiguous local times. When no reset time is present, the
schedule re-polls with exponential backoff (5m, doubling to the 30m ceiling by
default) instead of guessing.

## The state machine

States are persisted; every change writes a `state_transitions` row.

```text
DISCOVERED → RUNNING → USAGE_LIMIT_DETECTED → WAITING_FOR_RESET → RESET_TIME_REACHED
                                                                    ↓
                                                          CHECKING_SESSION → CHECKING_LIMIT
                                                                    ↓                ↓
                                                                 RESUMING ──────→ RESUMED
                                                                    ↓
                                                        RETRY_WAIT / PERMANENTLY_FAILED
```

Legal edges are declared once, in `internal/recovery/state.go`, and any other
edge is rejected with `ErrInvalidTransition`. The interesting ones:

| From | Allowed |
| --- | --- |
| `RUNNING` | `USAGE_LIMIT_DETECTED`, `COMPLETED`, `USER_TERMINATED`, `DISABLED` |
| `WAITING_FOR_RESET` | itself (unknown-reset re-poll or a restart), `RESET_TIME_REACHED`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED` |
| `CHECKING_SESSION` | `CHECKING_LIMIT`, `RESUMED` (it resumed on its own), `RETRY_WAIT` (the status check itself failed) |
| `CHECKING_LIMIT` | `RESUMING`, `RETRY_WAIT`, `WAITING_FOR_RESET` (the limit is still in force) |
| `RESUMING` | `RESUMED`, `RETRY_WAIT`, `PERMANENTLY_FAILED` |
| `RETRY_WAIT` | `CHECKING_SESSION`, `WAITING_FOR_RESET`, `PERMANENTLY_FAILED` |
| `RESUMED` | `RUNNING`, `COMPLETED`, `USER_TERMINATED`, `DISABLED` — never straight back to `RESUMING` |
| `PERMANENTLY_FAILED` | `CHECKING_SESSION` (an operator asked for another go), `USER_TERMINATED`, `DISABLED` |

`COMPLETED`, `USER_TERMINATED` and `UNRECOVERABLE` are terminal. `RESUMED`,
`COMPLETED`, `USER_TERMINATED`, `DISABLED` and `UNRECOVERABLE` are **settled**:
recovery refuses to touch them, so a finished conversation is never resumed
again by a stray tick.

`RETRY_WAIT` vs `PERMANENTLY_FAILED` is the difference between "this might work
later" and "stop asking": retryable failures wait with exponential backoff plus
jitter (`initial_delay` 30s, ×2, capped at `max_delay` 15m, ±`jitter` 15%),
non-retryable failures and an exhausted budget go to `PERMANENTLY_FAILED`.
`ccresu retry <session-id>` resets the budget, and `ccresu recover <session-id>
--force` tries once more anyway.

## Verification: proof, not optimism

A resume that returns exit code 0 has not necessarily continued the
conversation. The verifier therefore requires **both**:

1. **Session id preserved** — the id reported by `claude -p --resume` equals the
   id that was asked for (`ErrSessionNotPreserved` otherwise), and
2. **Transcript growth** — the transcript file gained lines within
   `recovery.verification_timeout` (default 120s).

Only then is the attempt recorded as a success, with `session_id_verified` and
`transcript_grew` set, the state moves to `RESUMED`, and
`Recovery succeeded` can be notified. Failure to prove either one is reported as
an unverified attempt rather than a silent success — a process that merely
started is not a recovery.

## What is not recoverable

| Situation | Reported as |
| --- | --- |
| Session type ccresu does not know (from `claude agents --json`) | `UNRECOVERABLE`, with the reported type in the reason |
| Claude Code older than 2.0.0, or missing from `PATH` | refused (`ErrCLIUnavailable`), never guessed |
| Authentication failure or invalid API key | classified as not a usage limit; never retried after a reset |
| Network error, crashed tool, user cancellation | not a usage limit; not resumed |
| Normal completion | recognised as completion; not resumed |
| Limit still in force at resume time | attempt recorded, back to `WAITING_FOR_RESET` |
| Attempt budget spent | `PERMANENTLY_FAILED` until `ccresu retry` |

## Inspecting what happened

```console
$ ccresu status
$ ccresu sessions --all
$ ccresu inspect <session-id>
$ ccresu history <session-id>
```
`history` shows the attempts and the ordered state transitions, including the
reason recorded for each transition, which is the audit trail for a wait.
