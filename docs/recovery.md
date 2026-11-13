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
3. The daemon tails the newest 25 transcript lines per session
   (`defaultTranscriptTail`), joins them, and hands the text to the detector.
4. The text is normalised, then scored against the built-in patterns and any
   `detector.custom_patterns`, with guards and negatives deciding what counts
   (below).
5. The paused-goal companion message (`Goal paused · usage limit reached · send
   a message after it resets`) is detected independently and recorded, which is
   why a continuation message is sent at all.

### Pattern families

All 19 built-in patterns are written against normalised, lowercase text. They
are ordered most-specific first, and equal confidences keep the earlier rule, so
ordering is the tie-break. A rule carries the limit kind it implies:

| Rule id | Confidence | Kind |
| --- | --- | --- |
| `canonical-hit-five-hour` | 0.99 | `five_hour` |
| `canonical-hit-weekly` | 0.99 | `weekly` |
| `canonical-reached-five-hour` | 0.99 | `five_hour` |
| `canonical-reached-weekly` | 0.99 | `weekly` |
| `five-hour-limit-hit` | 0.97 | `five_hour` |
| `weekly-limit-hit` | 0.97 | `weekly` |
| `epoch-usage-limit` (legacy form, `claude ai usage limit reached` then a pipe and digits) | 0.97 | inferred |
| `canonical-hit-limit` | 0.95 | inferred |
| `canonical-reached-limit` | 0.95 | inferred |
| `canonical-exceeded-limit` | 0.95 | inferred |
| `paused-goal-usage-limit` | 0.95 | inferred |
| `usage-limit-reached` | 0.95 | inferred |
| `out-of-extra-usage` | 0.93 | `other` |
| `limit-will-reset` | 0.92 | inferred |
| `limit-resets-in` (`RequiresReset`) | 0.90 | inferred |
| `plan-limit` | 0.90 | inferred |
| `generic-limit-reached` | 0.90 | inferred |
| `limit-try-again` | 0.88 | inferred |
| `quota-exhausted` | 0.80 | `other` |

The kind is `five_hour`, `weekly` or `other` and is spelled identically to
`storage.LimitType`, so a detection persists with no translation table. When a
rule does not fix the kind, `inferKind` reads a qualifier in specificity order —
`extra usage` → `weekly`/`7-day`/`seven-day` → `5-hour` → `daily`/`24-hour`/
`per day` → `monthly` → `opus` → `sonnet` → `haiku` → `message limit` — and only
weekly and five-hour change the kind; everything else stays `other` with its
wording preserved.

A pattern with `RequiresReset` is skipped when the message carried no reset
information at all, so `limit-resets-in` cannot fire on a bare "limit reached".

### Scoring and threshold

The score is `clampConfidence(pattern.Confidence - penalty)`, clamped into
`[0,1]`. `DefaultThreshold` is `0.5`, chosen so a canonical banner (0.95–0.99)
always detects while a generic 0.9 match collapses below the line as soon as one
strong guard applies. Every occurrence of a rule is scored, not just the
leftmost, capped at `maxOccurrences = 32` per rule — so a quoted example
followed by the real banner still detects, and a pathological input cannot make
detection quadratic.

### Guards — what stops prose counting as a limit

Thirteen guards subtract from a match when the surrounding text says it is prose
*about* a limit rather than an interruption. The penalty is capped at 1, so
confidence never goes negative:

| Guard | Penalty | Fires on |
| --- | --- | --- |
| `negation` | 1.0 | `not`, `never`, `no`, `none`, `without`, `n't`, `cannot`, `yet to`, … |
| `code-fence` | 0.8 | an odd number of ``` before the match (inside a fence) |
| `described` | 0.6 | `phrase`/`regex`/`pattern`/`docs`, or a detect verb (`detects`, `matches`, …) |
| `conditional` | 0.55 | `if`, `when`, `whenever`, `once`, `unless`, `whether`, `before`, `after`, `in case`, … |
| `question` | 0.55 | a `?` before the next full stop in the 40 bytes after the match |
| `modal` | 0.5 | `can`/`could`/`may`/`might`/`would`/`should`/`will` + a limit verb |
| `named` | 0.5 | `message`/`string`/`text`/`caption`/`banner`/`wording` |
| `example` | 0.5 | `for example`, `for instance`, `such as`, `e.g.`, `i.e.` |
| `transient-retry` | 0.5 | `retrying`, `retry after`, `backing off`, `temporarily unavailable`, … |
| `quoted` | 0.5 | the span is wrapped in matching `"`, `'`, `` ` `` or `[`/`]` |
| `markdown-quote` | 0.5 | the text starts with `>` |
| `third-person` | 0.4 | `users`, `someone`, `people`, `developers`, `anyone`, `whoever` |
| `explained` | 0.3 | the following 80 bytes say `means`, `indicates`, `refers to`, `is shown`, … |

An apostrophe inside a word never counts as an opening quote, so "you've" is
safe. Guards look only at the clause containing the match, cut at `. ! ? : ;`,
and `e.g`/`i.e`/`etc`/`vs`/`approx` are not treated as sentence ends — so an
unrelated previous sentence cannot suppress a real banner. The window is 80 bytes
before and 80 after, widened to rune boundaries.

### Negative rules — explicit non-limit verdicts

When no pattern reaches the threshold, the detector consults the negative rules
before giving up. Each yields a classification, and only `usage_limit` means a
limit was found:

| Classification | Rules | Confidence |
| --- | --- | --- |
| `invalid_key` | rejected/missing credential, credential described as invalid, credential variable named in an error | 0.95, 0.95, 0.90 |
| `network_error` | transport failure; upstream HTTP 5xx | 0.92, 0.85 |
| `auth_error` | unauthorised/unauthenticated/permission denied/401 | 0.90 |
| `user_cancelled` | cancelled/aborted/interrupted/context canceled/esc pressed | 0.90 |
| `completed` | goal/session/task complete; all-work-complete summary; session ended normally | 0.85 ×3 |

These are first-class because the wrong action is destructive: an auth failure
must not be retried after a reset, an invalid key must never be retried, and a
completed session must never be resumed. Rules are ordered most-specific first so
an invalid key is reported as an invalid key rather than a generic auth failure,
and the highest-confidence match wins.

### Normalisation

`Normalize` is stable and idempotent — `Normalize(Normalize(s)) == Normalize(s)`
— and returns lowercase, single-spaced, ANSI-free text with ASCII punctuation:

- ANSI CSI/OSC escapes are stripped; control characters and box-drawing become
  spaces; fullwidth ASCII is folded.
- Unicode spaces fold to a single space; zero-width joiners, soft hyphens and DEL
  are dropped.
- Curly quotes and primes become `'` and `"`; dashes become `-`; bullets become
  `:`; `…` becomes `...`.
- Fields collapse to single spaces, and a numeric qualifier wrapped after its
  hyphen is rejoined — `5-\nhour` → `5-hour` — restricted to digits so ordinary
  hyphenated prose is untouched.

Two views are kept: a `spaced` view that preserves case for IANA zone names such
as `Europe/Berlin`, and a `folded` view that every pattern is written against.
They are deliberately not index-aligned.

## Reset-time resolution

The detector only *decomposes* the phrase into a `ResetPhrase` (`Present`, `Raw`,
`Expression`, `Timezone`, `Relative`, `EpochUnix`); resolving "12pm" against a
calendar, zone and DST is the scheduler's job, so the same phrase can be
re-resolved later without re-reading the message.

`resets 12pm (Europe/Berlin)` resolves to 12:00 in **Europe/Berlin**, not in the
host's timezone. On 2026-03-02 (Berlin = UTC+1) that is 11:00Z, and the stored
row keeps both the UTC instant and the zone name so `ccresu history` can explain
the wait. `Resolve` always returns `At` in UTC; `Kind` is one of `unknown`,
`epoch`, `relative`, `time_of_day`, `weekday` or `date`.

### The branches

| Input | Resolution |
| --- | --- |
| No phrase at all | `Known=false`, note "the message carried no reset phrase" |
| Empty expression | `Known=false`, note "the reset phrase was empty" |
| Legacy epoch form (`usage limit reached`, pipe, digits) | `time.Unix(...).UTC()`, kind `epoch`, zone `UTC`; 19 digits are ns, 16 µs, 13 ms |
| A duration (`in 2 hours`) | `now.Add(d)`, kind `relative`, measured from the detection time; a duration has no wall clock, so no DST problem |
| A phrase naming a zone | resolved in **that** zone |
| A zone ccresu does not recognise | treated as **unknown** rather than guessed, because guessing could shift the wake-up by hours |
| No zone named | the configured default (`app.timezone`, otherwise UTC), never the host's `TZ` |
| An unparseable expression | `Known=false`, with a note naming the phrase |
| Instant already in the past | rolled forward on the **civil date** — one day for a clock time, one week for a weekday, one year for a month/day — at most 8 iterations, and the wall clock is resolved again; never by adding 24 hours, because on a DST day those differ |
| A calendar date already gone | next year's; but a date "today" with an explicit time is taken as written and is simply due — rolling it a year forward would strand a session for twelve months |
| A resolved instant still in the past | reported as due immediately |

The civil forms recognised are `noon`/`midday`, `midnight`,
`today`/`tomorrow`/`tonight`, a weekday, a month-name date, an ISO date, and a
clock time with or without a day word. A missing time of day is defaulted and
said out loud — midnight, or 23:59 for "tonight". A bare clock hour with no
meridiem is read as 24-hour, so "12" is noon.

Zone abbreviations are **fixed offsets**, not rules: `EST` is UTC−5 even in July.
The IANA database is embedded, so resolution does not need the host's zoneinfo.

For DST, a repeated wall clock chooses the **later** instant and a missing one the
instant after the gap, because waiting is cheap while a wasted attempt costs a
retry and a notification (`PreferEarlierOnAmbiguous` inverts the first rule).
Every assumption is recorded in `Resolution.Notes` rather than applied silently,
so `ccresu inspect` can explain a wait.

### Unknown → poll with backoff

When no reset time is present — or the phrase names a zone ccresu will not guess
at — the schedule is marked `Unknown` with `NextPollAt = now + initial` and
re-polls instead of guessing. The gaps are 5m, 10m, 20m, 30m… (`DefaultInitialInterval`
5m, `DefaultMaxInterval` 30m, doubling), overridable via
`scheduler.unknown_reset_initial_interval` and
`scheduler.unknown_reset_max_interval`. Each due poll that fails to recover calls
`Scheduler.Advance`, which increments `attempts` and sets the next
`next_poll_at`. The count persists in the row, so a restart continues at the same
interval instead of snapping back to five minutes.

This is a **different policy** from the attempt backoff (30s → 15m): one asks
"is the window over yet", the other asks "might this resume work now".

## How a usage limit is tracked

Every detected limit — whether read from a transcript or learned from a refusal —
is one row in `usage_limits`, with the same shape in both cases:

| Column | Meaning |
| --- | --- |
| `id` | row id |
| `session_id` | the session, cascading with it |
| `limit_type` | `five_hour`, `weekly` or `other` (validated) |
| `raw_message` | the banner, verbatim, bounded to 2000 bytes |
| `reset_at` | the resolved instant, or NULL when none could be parsed |
| `reset_timezone` | the zone name the phrase named, if any |
| `reset_raw` | the phrase exactly as the CLI wrote it |
| `confidence` | the detector's final score after guards, in `[0,1]` |
| `paused_goal` | whether the CLI reported the goal paused |
| `detected_at` | when it was seen |

`reset_at` being NULL is what triggers the polling strategy above. `confidence`
is the post-guard score, and the database enforces its range with a CHECK, so a
bug cannot store a probability outside `[0,1]`.

Detection is idempotent by comparison, not by a counter: the newest row is the
fingerprint, and the same `reset_raw` with the same truncated `raw_message`
records nothing and sends nothing. A session in `DISCOVERED` is exempt so it can
be re-planned.

**A later-resolved reset is written as a new row, not an in-place rewrite.** At
HEAD the daemon never calls `SetUsageLimitResetAt` (only tests do); a reset
learned later — from a refusal, or from a fresh banner — is persisted as a new
`usage_limits` row plus an upserted `schedules` row. The schedule is an upsert on
`session_id` because a session can hit its limit more than once and the new wait
replaces the stale one. A schedule must carry either a reset instant or the
unknown flag, never neither and never both; `LoadSchedules` ignores rows with no
deadline at all, because such a row could never become due.

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
edge is rejected with `ErrInvalidTransition`. The map is the authoritative table:
14 states, and only the edges written there are legal.

| From | Allowed |
| --- | --- |
| `DISCOVERED` | `RUNNING`, `CHECKING_SESSION` (recover/retry start by checking the session), `DISABLED`, `UNRECOVERABLE`, itself |
| `RUNNING` | `USAGE_LIMIT_DETECTED`, `COMPLETED`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `USAGE_LIMIT_DETECTED` | `WAITING_FOR_RESET`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `WAITING_FOR_RESET` | itself (unknown-reset re-poll or a restart), `RESET_TIME_REACHED`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `RESET_TIME_REACHED` | `CHECKING_SESSION`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `CHECKING_SESSION` | `CHECKING_LIMIT`, `RESUMED` (it resumed on its own), `RETRY_WAIT` (the status check itself failed), `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `CHECKING_LIMIT` | `RESUMING`, `RETRY_WAIT`, `WAITING_FOR_RESET` (the limit is still in force), `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `RESUMING` | `RESUMED`, `RETRY_WAIT`, `WAITING_FOR_RESET` (the resume was refused because the limit is still in force), `DISCOVERED`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED` |
| `RETRY_WAIT` | `CHECKING_SESSION`, `WAITING_FOR_RESET`, `DISCOVERED`, `USER_TERMINATED`, `DISABLED` |
| `RESUMED` | `RUNNING`, `COMPLETED`, `USER_TERMINATED`, `DISABLED` — never straight back to `RESUMING` |
| `COMPLETED` | — (terminal) |
| `USER_TERMINATED` | — (terminal) |
| `DISABLED` | `RUNNING`, `DISCOVERED`, `USER_TERMINATED` |
| `UNRECOVERABLE` | — (terminal) |

A repeat `WAITING_FOR_RESET → WAITING_FOR_RESET` is legal because an
unknown-reset session re-enters the waiting state after every poll and a restart
re-observes the wait; moving to the state it is already in still writes the audit
row, because "the limit was checked again and was still in force" is worth
keeping. `RESUMED` is not strictly terminal — a resumed session can hit its limit
again — but a second recovery is only legal after the session has been observed
running and hit a new limit, never straight from `RESUMED`.

### Which states are terminal, settled and pending

Three predicates classify a state, and they are what every caller consults:

- **Terminal** — no edge leaves the state: `COMPLETED`, `USER_TERMINATED`,
  `UNRECOVERABLE`. The session will not move again without an operator.
- **Settled** — must be left alone: `RESUMED`, `COMPLETED`, `USER_TERMINATED`,
  `DISABLED`, `UNRECOVERABLE`. Recovery refuses to touch a settled session, which
  is what stops a completed conversation from being resumed again.
- **Pending** — still wants automatic work, and what a restarted daemon must pick
  up: `USAGE_LIMIT_DETECTED`, `WAITING_FOR_RESET`, `RESET_TIME_REACHED`,
  `CHECKING_SESSION`, `CHECKING_LIMIT`, `RESUMING`, `RETRY_WAIT`.

There is no terminal *failure* state: the only state ccresu declares failed is
`UNRECOVERABLE`, and only because it has positively established it has no
verified way to continue the conversation. A spent attempt budget returns the
session to `DISCOVERED`, so it is a pause, not a failure.

`RETRY_WAIT` is "this might work on the same schedule": retryable failures wait
with exponential backoff plus jitter — `initial_delay` 30s, ×2, capped at
`max_delay` 15m, ±`jitter` 15% — so the sequence is roughly 30s, 1m, 2m, 4m, 8m,
then 15m. Anything else — a non-retryable failure, or an exhausted budget — sends
the session back to `DISCOVERED`. The budget is `DefaultMaxAttempts = 10` per
discovery cycle.

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

## The refusal parking path

This section describes the *mechanics* of parking a refused resume. The
narrative — the banner, what an operator sees in `ccresu inspect`, and why the
old behaviour looped — is in [When the resume is refused because the limit is
still in force](#when-the-resume-is-refused-because-the-limit-is-still-in-force)
below.

Classification comes first. The classes are `retryable`, `permanent`,
`limit-in-force`, `unrecoverable` and `canceled`. `ClassLimitInForce` is
deliberately neither retryable nor permanent: the refusal names when work may
continue, so the only useful answer is to park. Calling it retryable is what
resumed the same paused session every pass; calling it permanent is what made it
look like a broken binary. The decision order is fixed, and
`claude.ErrUsageLimitInForce` is tested **before any text marker**, because the
banner is prose and "usage limit reached for this account" is otherwise a
permanent marker. `ErrCLIUnavailable` keeps its own meaning: the process never
started.

The park path is a small, injected seam:

- `recovery.LimitPark` is `func(ctx, sessionID, banner string) (Wait, error)`.
  The banner is the CLI's own output verbatim.
- `Wait` carries `At` (the instant ccresu will next act), `Known` (a named reset
  versus the first poll), `Delay`, and `LimitID`.
- `LimitInForceError` renders the refusal as the wait it is —
  `<session>: usage limit in force, waiting until <RFC3339>|<for <duration>>|<until the limit clears>: <banner>`
  — reports `ErrLimitNotCleared` too, and unwraps to the adapter error carrying
  `claude.ErrUsageLimitInForce`.
- `refusalBanner` prefers the captured stdout, then stderr, then the error text.

In the manager, `fail` checks `ClassLimitInForce` **before** consulting the retry
policy, so no backoff can turn a refusal into another resume. `park` then:

1. finishes the refused attempt first, with `State=WAITING_FOR_RESET`,
   `Success=false`, `Error=<banner>`, so the next `Recover` cannot close it as
   "interrupted by a process restart";
2. asks the parker to plan the wait — a nil parker or a parker that errors is
   logged and the session still waits, because resuming again would ask the same
   question;
3. moves to `WAITING_FOR_RESET` with reason
   `the usage limit has not reset yet: <banner>` and records the banner as
   `last_error`.

No attempt budget is consulted on the park path: what ends the wait is the reset,
not a count of tries.

The app-side parker is `limitParker`. It analyses the banner, plans the wait,
records the `usage_limits` row (banner bounded to 2000 bytes, with the plan's
reset fields), links the row into the plan, saves the schedule, and returns the
wait. It is deliberately the same seam as transcript detection, so a limit
learned from a refusal and one learned from a transcript leave the database in
the same shape — one kind of wait, not two.

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
   "limit reached" is a wait, never `ClassPermanent` and never `DISCOVERED`.
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
conversation. `Verifier.Verify` performs two independent checks and sets `Proven`
only when **both** hold:

1. **Session id preserved** — the requested id must match the baseline, the CLI
   must report one, it must equal the baseline, and the provider must have set
   `Verified`. A status whose reported `Session.ID` differs also fails the check,
   with a note naming both ids. An empty report is `ErrNoSessionIDReported`; a
   different one is `ErrSessionNotPreserved`.
2. **Transcript growth against the stored baseline** — a line-count increase
   proves growth; an unchanged line count with a larger file size also proves it
   when the baseline size is non-zero; a baseline with no line count cannot prove
   anything and says so.

The wait for growth is bounded by `DefaultVerificationTimeout = 120s` and polled
every `DefaultHealthCheckInterval = 10s`; a zero means the default, and a
negative value means "check once, never wait". A live process is reported but
never used as proof — a launched process is not a resumed conversation.

"We could not check" and "the check failed" are kept apart: `Verify` returns an
error only when the checks could not be completed, and an unprovable resume is a
`Verification` with `Proven=false` rather than a silent success.

### Closing the attempt

Only a proven resume closes the attempt as a success: `FinishAttempt` writes
`state = RESUMED`, `success = true`, `session_id_verified = true`,
`transcript_grew = true`, the manager stores the new transcript metrics, clears
`last_error`, and moves the session to `RESUMED` with the reason
`the original session resumed and the conversation grew`.

A failed resume's own writes are absorbed into the baseline by
`absorbAttemptWrites` before any decision, by re-reading the transcript —
otherwise the next status check would see growth it caused itself and report a
refused resume as a success.

The attempt row is written *before* the resume is issued (`StartAttempt`), and its
`UNIQUE (session_id, attempt_number)` index is the arbiter, so a duplicate
allocation loses in the database rather than in a race. `StartAttempt` retries
allocation up to `maxAttemptAllocationRetries = 8` and treats the unique index as
the final word.

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
