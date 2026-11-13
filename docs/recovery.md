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
                                                    RETRY_WAIT / DISCOVERED

RESUMING ──→ WAITING_FOR_RESET    (the resume was refused: the limit is still in force)
```

`DISCOVERED` is both the start and the retry state: a failed attempt, and a spent
attempt budget, return the session there, and the next discovery pass reads its
transcript again and schedules the next reset.

Legal edges are declared once, in `internal/recovery/state.go`, and any other
edge is rejected with `ErrInvalidTransition`. The interesting ones:

| From | Allowed |
| --- | --- |
| `DISCOVERED` | `RUNNING`, `CHECKING_SESSION` (recover/retry), `DISABLED`, `UNRECOVERABLE`, itself |
| `RUNNING` | `USAGE_LIMIT_DETECTED`, `COMPLETED`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `WAITING_FOR_RESET` | itself (unknown-reset re-poll or a restart), `RESET_TIME_REACHED`, `DISCOVERED`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED` |
| `CHECKING_SESSION` | `CHECKING_LIMIT`, `RESUMED` (it resumed on its own), `RETRY_WAIT` (the status check itself failed), `DISCOVERED` |
| `CHECKING_LIMIT` | `RESUMING`, `RETRY_WAIT`, `WAITING_FOR_RESET` (the limit is still in force), `DISCOVERED` |
| `RESUMING` | `RESUMED`, `WAITING_FOR_RESET` (the resume was refused because the limit is still in force), `RETRY_WAIT`, `DISCOVERED` |
| `RETRY_WAIT` | `CHECKING_SESSION`, `WAITING_FOR_RESET`, `DISCOVERED` |
| `RESUMED` | `RUNNING`, `COMPLETED`, `USER_TERMINATED`, `DISABLED` — never straight back to `RESUMING` |

`COMPLETED`, `USER_TERMINATED` and `UNRECOVERABLE` are terminal. `RESUMED`,
`COMPLETED`, `USER_TERMINATED`, `DISABLED` and `UNRECOVERABLE` are **settled**:
recovery refuses to touch them, so a finished conversation is never resumed
again by a stray tick.

`RETRY_WAIT` is "this might work on the same schedule": retryable failures wait
with exponential backoff plus jitter (`initial_delay` 30s, ×2, capped at
`max_delay` 15m, ±`jitter` 15%). Anything else — a non-retryable failure, or an
exhausted budget — sends the session back to `DISCOVERED`.

A resume the CLI **refused** because the limit is still in force is neither: it is
not a failure to retry and not a success to celebrate, so it parks the session in
`WAITING_FOR_RESET` until the reset the banner named — see [When the resume is
refused because the limit is still in force](#when-the-resume-is-refused-because-the-limit-is-still-in-force).

There is no terminal failure state. A session that cannot be resumed is not
broken for ever: it is waiting for its limit to clear, or for a condition that
has since changed. Returning it to `DISCOVERED` lets the daemon read its
transcript again, find the next reset time and schedule another attempt, so an
operator never has to clear a failure by hand. `ccresu retry <session-id>` and
`ccresu recover <session-id> --force` still exist for a session an operator wants
to push right now — see [cli.md](cli.md).

A failure **before** the resume is issued — the status check in
`CHECKING_SESSION` or the limit check in `CHECKING_LIMIT` — is different: no
attempt row exists, so no budget was spent, and the session rests in
`RETRY_WAIT` whatever the error is classified as (an `UNRECOVERABLE` verdict
still wins), because a rediscovery would claim a budget this failure never spent.
Its transition reason says so explicitly, e.g. `the pre-resume check failed
(permanent); no attempt was spent, next check in 1m0s: …`, so `ccresu history`
explains the wait.

The **attempt budget** is measured per discovery cycle, not per session. The
guard runs before any check and counts the attempts started since the session
last entered `DISCOVERED`; when ten of them have been spent it records
`maximum attempts reached (10); returned to discovery for the next reset`, moves
the session to `DISCOVERED`, and returns `errors.Is(err, ErrMaxAttempts)`.

Entering `DISCOVERED` therefore starts a fresh budget — which is the whole point:
the daemon rediscovers the session, reads its transcript, plans the next reset and
tries again, without anyone running a command. The budget is a safety valve
against a tight loop inside one cycle, not a verdict about the session. Because
the guard can fire wherever a session is parked — `WAITING_FOR_RESET` after an
unknown-reset poll, `CHECKING_SESSION` after a crash mid-check — every state that
still permits automatic work has an edge to `DISCOVERED`.

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
failed resume: it returns to `DISCOVERED`, where the next discovery pass reads the
transcript again and schedules the next reset — unless the failure is the usage
limit itself, which parks the session in `WAITING_FOR_RESET` instead (see [When the
resume is refused because the limit is still in
force](#when-the-resume-is-refused-because-the-limit-is-still-in-force)).
`ccresu retry <session-id>` and `ccresu recover <session-id> --force` are there if
you want it tried right now.

Once a session has been stopped this way, the next resume is a normal resume
again: the background agent no longer holds it, so there is no stop to issue.

### There is no dead end to exit

A session that could not be resumed used to end in a state that only an operator
could clear. It no longer does. `DISCOVERED` is not a failure: it is the queue the
daemon reads from, and every failed resume and every spent budget puts the
session back in it with its attempt history intact. A usage-limit refusal is the
one failure that does not go there: it is not a failure at all, so it waits in
`WAITING_FOR_RESET` for the reset the CLI named.

If a session really is finished, `ccresu disable <session-id>` is the honest way
to say so, and `ccresu sessions delete <session-id>` stops tracking it — see
[cli.md](cli.md). Nothing about a failed attempt destroys the conversation: the
transcript, the session id and the audit trail are all untouched.

## When the resume is refused because the limit is still in force

The other refusal is the plain one: the five-hour or weekly window has not reset
yet, so the CLI answers the resume with the banner and exit code 1.

```console
Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin).
```

This is not a failed resume, and it must not be retried. The conversation is
intact, nothing about the session is wrong, and asking again before 10am can only
produce the same sentence — which is exactly what used to happen: the refusal was
reported as `claude: claude executable unavailable`, classified as a permanent
failure, and the daemon resumed the session roughly forty times in half an hour.
Now the attempt ends in a wait:

1. The refusal is recognised from the CLI's own output — stdout as well as the
   stderr tail, carried on the adapter's `*CLIError` — and classified
   `limit-in-force` (`recovery.ClassLimitInForce`). The class is decided before
   any permanent or retryable marker, so a banner that also contains words like
   "limit reached" is a wait, never `PERMANENT` and never `DISCOVERED`.
   `ErrCLIUnavailable` keeps its own meaning: the process never started.
2. The attempt the CLI answered is **finished with the refusal** — `success:
   false`, error = the banner, state `WAITING_FOR_RESET`. Leaving it open would
   let the next recovery close it as "interrupted by a process restart", which is
   not what happened.
3. The banner is recorded as a **usage limit**: the row keeps the CLI's own words
   verbatim (bounded to 2000 bytes), the resolved reset instant, the zone and
   phrase the CLI wrote (`raw`), and the detector's confidence — the same shape a
   limit detected from the transcript produces, so `ccresu inspect` describes one
   kind of wait, not two.
4. The **wait is planned**: the schedule is saved at the reset instant the banner
   named, or at the first poll (with the usual backoff) when it named none.
5. The session moves `RESUMING → WAITING_FOR_RESET` with the reason
   `the usage limit has not reset yet: <banner>`, and `last error` is set to the
   banner itself.

No further resume is issued in the meantime. The daemon leaves a due schedule
alone only when the refusal actually planned a wait — a `*LimitInForceError` with
a reset instant. The pre-resume limit check and a refusal whose parker could not
plan still advance the poll, so an unknown-reset schedule cannot be retried on
every pass. The bound is asserted end to end: five reconcile passes inside one
window issue exactly one `claude -p --resume`, and the second is issued after the
reset instant arrives.

The refused attempt's own transcript writes are absorbed into the stored
baseline. The CLI appends its refusal to the conversation, so the file has grown
by the time the reset comes; that growth is the attempt's own work and cannot
satisfy the "the session had already resumed after the limit reset" shortcut, so
a refused cycle never ends in `RESUMED` or "already resumed".

What an operator sees:

```console
$ ccresu inspect 1347e5db-a9fb-42eb-b92a-82256bd5aa68
session:       1347e5db-a9fb-42eb-b92a-82256bd5aa68
state:         WAITING_FOR_RESET
enabled:       yes
recoverable:   yes
last error:    Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin).
project:       /home/you/projects/demo
transcript:    /home/you/.claude/projects/-home-you-projects-demo/1347e5db-….jsonl
type:          headless
last seen:     2026-01-06 14:58:11 +08

Next attempt
at:            2026-01-06 09:00:00 UTC (in 2h1m)
why:           waiting for the usage limit to reset

Latest usage limit
type:          other
detected:      2026-01-06 14:58:11 +08
resets:        2026-01-06 17:00:00 +08
raw:           10am (Europe/Berlin)
confidence:    0.95

Latest attempt
number:        1
state:         WAITING_FOR_RESET
started:       2026-01-06 14:58:11 +08
finished:      2026-01-06 14:58:14 +08
command:       claude -p --resume 1347e5db-… --output-format json <201-byte continuation message>
error:         Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin).
…
```

`at` is the reset instant the banner named, not a backoff: the session is waiting
for the window, not for a retry, and `why` says so. `ccresu recover <id>` still
works if you want the question asked right now — if the limit has not cleared the
CLI refuses again and the session is parked again with the same banner.

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
| before the fix (`00692e3`) | exits 1, reporting the refusal | one resume, no stop | `PERMANENTLY_FAILED` (that binary predates the state's removal) |
| with the fix | exits 0, verification proven | resume, stop, resume | `RESUMED` |

The second session in the same run is the one that cannot be resumed. It now ends
in `DISCOVERED` with its attempt row, its reason and its transcript intact, and
the script asserts that `ccresu reset` and `ccresu sessions clear` no longer
exist (both exit 2 as unknown commands).

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
| Limit still in force at resume time | the attempt is finished with the banner, the banner is recorded as a usage limit, and the session returns to `WAITING_FOR_RESET`; no further resume until the reset it named |
| Session still attached to a background agent | `claude stop <job-id>` then the identical resume, once per attempt; if the stop fails the original refusal is reported |
| Attempt budget spent | back to `DISCOVERED`; the next discovery pass re-reads the transcript, schedules the next reset and starts a fresh budget |

## Inspecting what happened

```console
$ ccresu status
$ ccresu sessions --all
$ ccresu inspect <session-id>
$ ccresu history <session-id>
```
`history` shows the attempts and the ordered state transitions, including the
reason recorded for each transition, which is the audit trail for a wait.
