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
| Resume the original session | `claude --resume <session-id> --bg "<message>"`, dispatched into a background job |
| Release a session still attached to a background agent | `claude stop <job-id>` (the eight-hex job name the refusal prints) |

The resume vector is built in one place, `claude.ResumeArgs`, and is:

```go
[]string{"--resume", sessionID, "--bg", message}
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
the resume timeout. The conversation survives it — the transcript and the project
directory are untouched — so the resume that follows is the same
`--resume <id> --bg <message>` dispatch with the same arguments.

There is still no `--fork-session`, no TTY, no keystroke injection into your
terminal, and no other undocumented subcommand. A dispatch does create a session
id of its own for the continuation — a background agent always does — but that is
the conversation continuing, recorded on the row, not a copy of it. The
`--fork-session` prohibition is the reason `stop` was chosen at all: the CLI's own
refusal offers `claude attach …`, `claude stop …` and `--fork-session`, and only
`stop` keeps the promise that the *original* conversation continues. `attach`
needs an interactive terminal ccresu deliberately never allocates, and
`--fork-session` would continue a copy instead — the one thing a recovery must
never do. The message is limited to 8 KiB, must be non-empty, the session id of
a resume is validated before anything is executed, and a `claude stop` target
must be an eight-hex job name, so neither can be read as a flag or a path.

### Why the resume no longer passes `-p`

A resume is a **dispatch**, not a synchronous turn. ccresu runs

```console
$ claude --resume 044e2a0a-b53b-471b-a24f-bf123bd7c3c7 --bg 'say OK'
backgrounded · d48ecad8
  claude agents             list sessions
  claude attach d48ecad8    open in this terminal
  claude logs d48ecad8      show recent output
  claude stop d48ecad8      stop this session
```

and the process returns at once. The only thing it prints on stdout is the
acknowledgement `backgrounded · <job-id>`, which is what `claude.Resume` parses;
the job itself keeps running, and that eight-hex name is the handle ccresu
follows.

Print mode, the form that used to run the turn synchronously, cannot dispatch a
job at all. `--bg` and `-p` are mutually exclusive (probed against Claude Code
2.1.223):

```console
$ claude -p --resume 044e2a0a-b53b-471b-a24f-bf123bd7c3c7 \
      --output-format json --bg "say OK"
--bg and --print conflict: --print never starts the interactive session that
`claude agents` attaches to, so the job would be unattachable. The prompt is the
positional — drop --print: `claude --bg '<task>'`.
$ echo $?
1
```

Exit code 1, nothing on stdout. `-p` is therefore gone, and with it the JSON
result that used to carry the turn's outcome. Two things a dispatched resume
**cannot** report:

- **Cost, turns and duration.** Without `--output-format json` there is no
  `total_cost_usd`, no `num_turns`, no `duration_ms`, no `subtype` and no
  `is_error`. They stay empty on the attempt row, deliberately: a zero would be a
  measurement, and there was none.
- **The id that answered being the id that was asked for.** The job continues the
  conversation under an id of its own — the resume of `044e2a0a-…` above
  produced `d48ecad8-ae57-413b-ba19-f9beac9062d4` — so that comparison cannot
  even be evaluated. `ErrSessionNotPreserved` no longer applies on this path.

What replaced them is stronger than a JSON blob: the job id is a handle ccresu
can follow. `claude agents --all --json` reports the job, the session id it
continues and its `state` — `working` while it runs, something else when it
stops — and the continuation's own transcript grows as the turn produces work.
Verification rests on those three facts, not on the exit code; see
[Verification](#verification-proof-not-optimism). `TestResumeArgsExact` pins the
exact vector, so the retired flag cannot quietly come back.

### Why the message is the last argument

The prompt is a **positional** argument: no flag introduces it. Position is
therefore the only thing that says which invocation it belongs to, and the
documented form is `claude --resume <session-id> --bg <message>` — the message
comes after the session it is being said to, and after the flag that says how the
turn runs. ccresu keeps it last and builds the vector in exactly one place:

```go
// internal/claude/cli.go
func ResumeArgs(sessionID, message string) []string {
	return []string{"--resume", sessionID, "--bg", message}
}
```

`TestResumeArgsExact` asserts that vector as a joined string, so reordering it is
a failing test rather than a puzzling runtime refusal, and `Resume` hands the
same slice to both the first attempt and the post-`stop` repeat — `resumeOnce`
exists so the second invocation cannot be a subtly different command.

The audit trail has to describe the same command. The attempt row's `command`
column is rendered by `commandPreview` in `internal/recovery/manager.go`:

```text
claude --resume <session-id> --bg <N-byte continuation message>
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
                                                                 RESUMING
                                                                     ↓
                                                                  WORKING ──→ RESUMED
                                                                     ↓
                                                    RETRY_WAIT / DISCOVERED
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
| `RESUMING` | `WORKING` (the dispatch answered with a job id), `RESUMED`, `RETRY_WAIT`, `DISCOVERED`, `UNRECOVERABLE` |
| `WORKING` | `RESUMED`, `RETRY_WAIT`, `DISCOVERED`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED` — the job's outcome is only known once it stops |
| `RETRY_WAIT` | `CHECKING_SESSION`, `WAITING_FOR_RESET`, `DISCOVERED` |
| `RESUMED` | `RUNNING`, `COMPLETED`, `USER_TERMINATED`, `DISABLED` — never straight back to `RESUMING` |

`COMPLETED`, `USER_TERMINATED` and `UNRECOVERABLE` are terminal. `WORKING`,
`RESUMED`, `COMPLETED`, `USER_TERMINATED`, `DISABLED` and `UNRECOVERABLE` are
**settled**, in the sense recovery refuses to touch them: a finished
conversation is never resumed again by a stray tick. For `WORKING` the reason is
different — the job is already working on the conversation, so a second resume
would duplicate the work; it is the follow stage that polls that row, not the
recovery engine, and a restarted daemon picks it back up from the database.

`RETRY_WAIT` is "this might work on the same schedule": retryable failures wait
with exponential backoff plus jitter (`initial_delay` 30s, ×2, capped at
`max_delay` 15m, ±`jitter` 15%). Anything else — a non-retryable failure, or an
exhausted budget — sends the session back to `DISCOVERED`.

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

`claude --resume <session-id> --bg "<message>"` refuses outright when the same
session is still held by the interactive CLI's background agent:

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
   `claude --resume <id> --bg …` dispatch that was run, not a command ccresu
   issued for its own bookkeeping.
3. The *identical* resume is issued once more — same argument vector, same
   session id, same continuation message — and its result is the attempt's result.

This is not a retry in the budget sense: the attempt keeps its number, and no new
attempt row is written. It is one attempt that has to clear the way first.

The sequence, from the outside:

| Step | What the CLI sees | What ccresu records |
| --- | --- | --- |
| 1 | `claude --resume <id> --bg "<message>"` — refused, session in the background | the attempt, with its single dispatch command as `command` |
| 2 | `claude stop <job-id>` — eight hex characters, never the session UUID | a log line only: the attempt row still shows one resume |
| 3 | the same dispatch, same arguments — acknowledged with `backgrounded · <job-id>` | the attempt's result, once the job is followed and the continuation proven |

If the stop itself fails, or the second resume refuses again (or fails any other
way), the **original** failure is returned untouched. Nothing loops, the
classification is unchanged, and the session is dealt with exactly as any other
failed resume: it returns to `DISCOVERED`, where the next discovery pass reads the
transcript again and schedules the next reset. `ccresu retry <session-id>` and
`ccresu recover <session-id> --force` are there if you want it tried right now.

Once a session has been stopped this way, the next resume is a normal resume
again: the background agent no longer holds it, so there is no stop to issue.

### There is no dead end to exit

A session that could not be resumed used to end in a state that only an operator
could clear. It no longer does. `DISCOVERED` is not a failure: it is the queue the
daemon reads from, and every failed resume and every spent budget puts the
session back in it with its attempt history intact.

If a session really is finished, `ccresu disable <session-id>` is the honest way
to say so, and `ccresu sessions delete <session-id>` stops tracking it — see
[cli.md](cli.md). Nothing about a failed attempt destroys the conversation: the
transcript, the session id and the audit trail are all untouched.

## Proving it end to end

This is not only asserted against fakes. `make e2e` runs
`tests/scripts/background-resume-e2e.sh`, which builds the real binary and puts a
stub `claude` on `PATH`. The stub models the CLI as it behaves now:
`--resume <id> --bg <message>` prints `backgrounded · <job-id>`, the job appears
in `claude agents --all --json`, reporting `working` on its first read and `done`
afterwards, and the job grows a transcript of its own — under a session id of its
own, never the one that was resumed. The stub refuses the retired print-mode
flags outright, so an argv drift fails the run instead of quietly taking a
fallback.

The stop fallback is still proven, because it still happens: a session attached
to a background agent is refused at dispatch, ccresu stops that job by its
eight-hex name — never the session UUID, which the stub answers with
`No job matching` and exit 1, exactly as the real CLI does — and repeats a
byte-identical dispatch.

The run then asserts what an operator cares about: `ccresu recover <id>` exits 0
and ends `RESUMED`; `ccresu inspect` shows the job and the continuation the job
reported, with the continuation's transcript path; `ccresu history` records the
dispatch command, the continuation and the proof; and a second limit written into
the continuation's own transcript is detected, planned and dispatched **at the
continuation's id**, so the abandoned original id is not resumed again and the
loop comes round.

The second session in the same run is the one that cannot be resumed. It ends in
`DISCOVERED` with its attempt row, its reason and its transcript intact, and the
script asserts that `ccresu reset` and `ccresu sessions clear` no longer exist
(both exit 2 as unknown commands).

## Verification: proof, not optimism

A dispatch that prints `backgrounded · <job-id>` has not necessarily continued
the conversation. The verifier therefore waits up to
`recovery.verification_timeout` (default 120s) for three facts, in order:

1. **The job leaves `working`.** `claude agents --all --json` is polled until the
   job reports another state; a job that is no longer listed at all counts as
   finished. The last state seen is kept for the log and for `ccresu inspect`.
2. **The job names its continuation.** The agent entry carries the session id the
   job continues. That id is recorded on the session *and* on the attempt as the
   continuation, together with the continuation transcript's path, and it is what
   the next cycle resumes.
3. **That continuation's transcript grows.** The file for the continuation id is
   measured against a baseline taken when the job first reported it, so growth
   means the turn produced work rather than merely starting.

Only then is the attempt recorded as a success, with `continuation_confirmed` and
`transcript_grew` set, the state moves to `RESUMED`, and `Recovery succeeded` can
be notified. Failing to prove any of them is reported as an unverified attempt
rather than a silent success — a job that merely started is not a recovery.

The id that was resumed is not required to answer, because it no longer does: the
conversation lives under the continuation, and the transcript metrics recorded on
the attempt are the continuation transcript's own numbers. `ccresu inspect` shows
both ids, and `ccresu sessions` shows the name the agents listing reports.

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
