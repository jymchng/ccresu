# How a resume actually happens

This is the part of ccresu that must not be hand-waved: a "recovery" is only a
recovery if the **original conversation** continued. Everything below is what the
code does, and what the end-to-end test asserts.

## Verified interfaces only

ccresu is verified against Claude Code **2.0.0 or newer**
(`claude.MinimumVersion`); an older CLI is refused rather than guessed at. It uses
exactly five interfaces:

| Purpose | Interface |
| --- | --- |
| Discover sessions | `claude agents --json`, with `--all` and `--cwd <path>` |
| Session transcript | `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl` |
| Per-session environment | `~/.claude/session-env/<session-id>/` |
| Resume the original session | `claude -p --resume <session-id> --output-format json "<message>"` |
| Release a session still attached to a background agent | `claude stop <job-id>` (the eight-hex job name the refusal prints) |

The resume vector is built in one place, `claude.ResumeArgs`, and is:

```go
[]string{"-p", "--resume", sessionID, "--output-format", "json", message}
```

The stop vector is built in one place too, `claude.StopArgs`, and is:

```go
[]string{"stop", jobID}
```

`jobID` is not the session UUID. `claude stop` resolves a background job name —
exactly eight lower-case hex characters, matched by prefix against the job
directories the CLI keeps — so a UUID is answered with

```console
No job matching '837eaaea-ee9a-4ef0-ae47-add73a107369'. Run 'claude agents' to
list running sessions.
```

and exit 1. The job name comes from the refusal message itself, which prints the
working command (`claude stop 837eaaea`); `claude.BackgroundJobID` extracts it,
`claude.ValidBackgroundJobID` pins its shape, and `Stop` refuses anything else
before a process starts. If the refusal names no job, no stop is attempted and
the original refusal is reported: guessing a job name could stop another
session's agent.

`claude stop` is the single exception to "`--resume` is the only command ccresu
runs", and it is narrow by construction: it is issued only after a resume has
already been refused because the session is still attached to a background agent
(see [When the session is still attached to a background
agent](#when-the-session-is-still-attached-to-a-background-agent)), it takes the
job name and nothing else, and it is bounded by the query timeout rather than
the resume timeout. The conversation survives it — the transcript, the session id
and the project directory are untouched — so the resume that follows is the same
`-p --resume` command with the same arguments.

There is still no `--fork-session`, no new session id, no TTY, no keystroke
injection into your terminal, and no other undocumented subcommand. The
`--fork-session` prohibition is the reason `stop` was chosen at all: the CLI's own
refusal offers `claude attach …`, `claude stop …` and `--fork-session`, and only
`stop` keeps the promise that the *original* conversation continues. `attach`
needs an interactive terminal ccresu deliberately never allocates, and
`--fork-session` would continue a copy instead — the one thing a recovery must
never do. The message is limited to 8 KiB, must be non-empty, the session id of
a resume is validated before anything is executed, and a `claude stop` target
must be an eight-hex job name, so neither can be read as a flag or a path.

### Why the message is the last argument

The prompt is a **positional** argument: no flag introduces it. Position is
therefore the only thing that says which invocation it belongs to, and the
documented form is `claude -p --resume <session-id> <prompt>` — the prompt comes
after the session it is being said to. ccresu keeps it last, after the two flags
that qualify the invocation, and builds the vector in exactly one place:

```go
// internal/claude/cli.go
func ResumeArgs(sessionID, message string) []string {
	return []string{"-p", "--resume", sessionID, "--output-format", "json", message}
}
```

`TestResumeArgsExact` asserts that vector as a joined string, so reordering it is
a failing test rather than a puzzling runtime refusal, and `Resume` hands the
same slice to both the first attempt and the post-`stop` repeat — `resumeOnce`
exists so the second invocation cannot be a subtly different command.

The audit trail has to describe the same command. The attempt row's `command`
column is rendered by `commandPreview` in `internal/recovery/manager.go`:

```text
claude -p --resume <session-id> --output-format json <N-byte continuation message>
```

The placeholder `<message>` sits where the real message sits — last. It used to
print the flags *after* the placeholder, which described an invocation ccresu
never ran; `ccresu history` is an audit trail, so a preview that disagrees with
`claude.ResumeArgs` is a worse bug than no preview at all. Both facts are pinned
by tests: `TestResumeArgsExact` for the vector that is executed, and
`TestCommandPreviewAndFormattingHelpers` for the preview text and its message
summary.

The message itself is never stored on the row: the column says how many bytes
were sent, the log says what happened, and the conversation stays in the
transcript where it belongs.

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
| `RUNNING` | `USAGE_LIMIT_DETECTED`, `COMPLETED`, `USER_TERMINATED`, `DISABLED`, `PERMANENTLY_FAILED` (budget spent) |
| `WAITING_FOR_RESET` | itself (unknown-reset re-poll or a restart), `RESET_TIME_REACHED`, `PERMANENTLY_FAILED` (budget spent), `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED` |
| `CHECKING_SESSION` | `CHECKING_LIMIT`, `RESUMED` (it resumed on its own), `RETRY_WAIT` (the status check itself failed), `PERMANENTLY_FAILED` (budget spent) |
| `CHECKING_LIMIT` | `RESUMING`, `RETRY_WAIT`, `WAITING_FOR_RESET` (the limit is still in force), `PERMANENTLY_FAILED` (budget spent) |
| `RESUMING` | `RESUMED`, `RETRY_WAIT`, `PERMANENTLY_FAILED` |
| `RETRY_WAIT` | `CHECKING_SESSION`, `WAITING_FOR_RESET`, `PERMANENTLY_FAILED` |
| `RESUMED` | `RUNNING`, `COMPLETED`, `USER_TERMINATED`, `DISABLED` — never straight back to `RESUMING` |
| `PERMANENTLY_FAILED` | `CHECKING_SESSION` (an operator asked for another go), `USER_TERMINATED`, `DISABLED` (via `ccresu reset`) |

`COMPLETED`, `USER_TERMINATED` and `UNRECOVERABLE` are terminal. `RESUMED`,
`COMPLETED`, `USER_TERMINATED`, `DISABLED` and `UNRECOVERABLE` are **settled**:
recovery refuses to touch them, so a finished conversation is never resumed
again by a stray tick.

`RETRY_WAIT` vs `PERMANENTLY_FAILED` is the difference between "this might work
later" and "stop asking": retryable failures wait with exponential backoff plus
jitter (`initial_delay` 30s, ×2, capped at `max_delay` 15m, ±`jitter` 15%),
non-retryable failures and an exhausted budget go to `PERMANENTLY_FAILED`.
`ccresu retry <session-id>` resets the budget, `ccresu recover <session-id>
--force` tries once more anyway, and `ccresu reset <session-id>` clears the stored
failure and leaves the session `DISABLED` — see [cli.md](cli.md).

A failure **before** the resume is issued — the status check in
`CHECKING_SESSION` or the limit check in `CHECKING_LIMIT` — is different: no
attempt row exists, so no budget was spent, and the session rests in
`RETRY_WAIT` whatever the error is classified as (an `UNRECOVERABLE` verdict
still wins). Calling such a session `PERMANENTLY_FAILED` would claim a budget it
never used. Its transition reason says so explicitly, e.g. `the pre-resume check
failed (permanent); no attempt was spent, next check in 1m0s: …`, so
`ccresu history` explains the wait.

The **attempt budget** is the mirror image. It is a property of the session, not
of one path through the machine, so `PERMANENTLY_FAILED` is legal from every
state that still permits automatic work — including `WAITING_FOR_RESET`, where
the scheduler parks a session whose limit is still in force, and
`CHECKING_SESSION`, where a crash mid-check leaves one. The guard runs before any
check, records `maximum attempts reached (10)`, and returns
`errors.Is(err, ErrMaxAttempts)` as a terminal error; `ccresu retry` (fresh
budget), `ccresu reset` (clear the failure, leave it disabled) or `ccresu recover
--force` (one more try now) is then the only way forward.

## When the session is still attached to a background agent

`claude -p --resume <session-id>` refuses outright when the same session is still
held by the interactive CLI's background agent:

```console
Error: That session is running in the background (837eaaea). Run `claude attach
837eaaea` to open it, or `claude stop 837eaaea` first to resume it here. Add
--fork-session to branch off a copy instead.
```

Nothing about the conversation is lost here, so the refusal must not end the
session's life. One attempt handles it, in three steps:

1. The refusal is recognised by `claude.IsSessionRunningInBackground`, after the
   message has been normalised — ANSI colour codes stripped, indentation and line
   wrapping collapsed — so a restyled or wrapped message still matches. A nil
   error never matches, and the marker is narrow enough that an unrelated failure
   is not mistaken for this one.
2. `claude stop <job-id>` releases the background agent — `claude.BackgroundJobID`
   reads the eight-hex name out of this very message, preferring the `claude stop`
   clause and falling back to the parenthesised name. A refusal that names no job
   is not stopped at all: the original error is reported instead of a guess. The
   stop is recorded
   in the log, never in the attempt row: `ccresu history` still shows the single
   `claude -p --resume …` command that was run, not a command ccresu issued for
   its own bookkeeping.
3. The *identical* resume is issued once more — same argument vector, same
   session id, same continuation message — and its result is the attempt's result.

This is not a retry in the budget sense: the attempt keeps its number, and no new
attempt row is written. It is one attempt that has to clear the way first.

The sequence, from the outside:

| Step | What the CLI sees | What ccresu records |
| --- | --- | --- |
| 1 | `claude -p --resume <id> --output-format json "<message>"` — refused, session in the background | the attempt, with its single resume command as `command` |
| 2 | `claude stop <job-id>` — eight hex characters, never the session UUID | a log line only: the attempt row still shows one resume |
| 3 | the same resume, same arguments | the attempt's result, verification included |

If the stop itself fails, or the second resume refuses again (or fails any other
way), the **original** failure is returned untouched. Nothing loops, the
classification is unchanged, and the session is dealt with exactly as any other
permanent resume failure: `ccresu retry <session-id>` for a fresh budget,
`ccresu recover <session-id> --force` for one more try now, or `ccresu reset
<session-id>` to clear the failure and park the session in `DISABLED`.

Once a session has been stopped this way, the next resume is a normal resume
again: the background agent no longer holds it, so there is no stop to issue.

### The operator exit from `PERMANENTLY_FAILED`

A session that reached `PERMANENTLY_FAILED` this way (or any other way) is not
lost, and `ccresu reset <session-id>` is how an operator says so. It is
deliberately narrow:

- It moves `PERMANENTLY_FAILED` → `DISABLED` and **nothing else**; any other
  state is refused with exit code `4` and a message naming the state.
- It clears the stored error, so the session no longer looks broken.
- It keeps the attempt row, the attempt reason and the counter: the history of
  what happened stays readable in `ccresu history`.
- It never resumes anything and never refunds an attempt. `ccresu retry <id>`
  refills the budget, `ccresu recover <id> --force` tries once more now, and
  `ccresu enable <id>` makes the session recoverable again with whatever budget
  it has left.

## Proving it end to end

This is not only asserted against fakes. `make e2e` runs
`tests/scripts/background-resume-e2e.sh`, which builds the real binary and puts a
stub `claude` on `PATH`. The stub keeps a background-agent marker, refuses the
first `-p --resume` with the CLI's text above, clears the marker on `claude stop
<job-id>`, and then returns a JSON result with the same `session_id` after growing
the transcript. The stub resolves its argument the way the real CLI does — only
an eight-hex job name it holds, anything else answered with `No job matching` and
exit 1 — and the run asserts that the stop carried the job name and never the
session UUID. Run against the pre-fix binary, that assertion is what fails. The run asserts that `ccresu recover <id>` exits 0 and ends `RESUMED`
with the verification "proven: session id preserved and transcript grew", that
the stub saw exactly *resume, stop, resume* with byte-identical resume arguments,
and that `ccresu history` still records the single resume command.

The same script with `--pre-fix` is the contrast, and it is the reason the fix
exists:

```bash
make e2e                              # the fixed binary: every check passes

# the contrast, against the pre-fix commit (the script needs that binary):
git worktree add /tmp/ccresu-prefix 00692e3
(cd /tmp/ccresu-prefix && go build -o /tmp/ccresu-prefix/ccresu ./cmd/ccresu)
tests/scripts/background-resume-e2e.sh --pre-fix /tmp/ccresu-prefix/ccresu
```

| Binary | `ccresu recover <id>` | Stub saw | Final state |
| --- | --- | --- | --- |
| before the fix (`00692e3`) | exits 1, reporting the refusal | one resume, no stop | `PERMANENTLY_FAILED` |
| with the fix | exits 0, verification proven | resume, stop, resume | `RESUMED` |

The failing session in the same run is also the fixture for `ccresu reset`:
`PERMANENTLY_FAILED` becomes `DISABLED`, the session's stored error is cleared,
the attempt row and its reason are kept, a second `reset` is refused with exit 4,
and `ccresu enable` leaves the session recoverable again.

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
| Session still attached to a background agent | `claude stop <job-id>` then the identical resume, once per attempt; if the stop fails the original refusal is reported |
| Attempt budget spent | `PERMANENTLY_FAILED` until `ccresu retry` (fresh budget) or `ccresu reset` (failure cleared, session left `DISABLED`) |

## Inspecting what happened

```console
$ ccresu status
$ ccresu sessions --all
$ ccresu inspect <session-id>
$ ccresu history <session-id>
```
`history` shows the attempts and the ordered state transitions, including the
reason recorded for each transition, which is the audit trail for a wait.
