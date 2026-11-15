# How a resume actually happens

This is the part of ccresu that must not be hand-waved: a "recovery" is only a
recovery if the **original conversation** continued. Everything below is what the
code does, and what the end-to-end test asserts.

## Verified interfaces only

ccresu is verified against Claude Code **2.1.257 or newer**
(`claude.MinimumVersion`); an older CLI is refused rather than guessed at. It uses
exactly six interfaces:

| Purpose | Interface |
| --- | --- |
| Discover sessions | `claude agents --json`, with `--all` and `--cwd <path>` |
| Read the published session state | `claude agents --json --all`, one read per recovery pass |
| Session transcript | `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl` |
| Per-session environment | `~/.claude/session-env/<session-id>/` |
| Continue the original session | `claude --bg --resume <session-id> --name ccresu-<first-8-hex> "<message>"` |
| Release a session still attached to a background agent | `claude stop <job-id>` (the eight-hex job name the refusal prints) |

The handoff vector is built in one place, `claude.HandoffArgs`, and is:

```go
[]string{"--bg", "--resume", sessionID, "--name", HandoffLabel(sessionID), message}
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

`claude stop` is the single exception to "`--bg --resume` is the only continuation
ccresu issues", and it is narrow by construction: it is issued only after a
handoff has already been refused because the session is still attached to a
background agent (see [When the session is still attached to a background
agent](#when-the-session-is-still-attached-to-a-background-agent)), it takes the
job name and nothing else, and it is bounded by the query timeout rather than
the handoff timeout. The conversation survives it — the transcript, the session id
and the project directory are untouched — so the handoff that follows is the same
`--bg --resume` command with the same arguments.

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
documented form is `claude --bg --resume <session-id> <prompt>` — the prompt comes
after the session it is being said to. ccresu keeps it last, after the flags that
qualify the invocation, and builds the vector in exactly one place:

```go
// internal/claude/handoff.go
func HandoffArgs(sessionID, message string) []string {
	return []string{
		"--bg",
		"--resume", sessionID,
		"--name", HandoffLabel(sessionID),
		message,
	}
}
```

`TestHandoffArgsExact` asserts that vector as a joined string, and
`TestHandoffArgsNeverCarriesPrintMode` asserts the composition the CLI enforces:
`--bg` cannot be combined with `-p`, so a vector carrying both could never create
a session. Reordering the vector is therefore a failing test rather than a
puzzling runtime refusal, and the post-`stop` repeat reuses the same slice.

The audit trail has to describe the same command. The attempt row's `command`
column is rendered by `commandPreview` in `internal/recovery/manager.go`:

```text
claude --bg --resume <session-id> --name ccresu-<first-8-hex> <N-byte continuation message>
```

The placeholder for the message sits where the real message sits — last. It used
to print the flags *after* the placeholder, which described an invocation ccresu
never ran; `ccresu history` is an audit trail, so a preview that disagrees with
`claude.HandoffArgs` is a worse bug than no preview at all. Both facts are pinned
by tests: `TestHandoffArgsExact` for the vector that is executed, and
`TestCommandPreviewAndFormattingHelpers` for the preview text and its message
summary.

The message itself is never stored on the row: the column says how many bytes
were sent, the log says what happened, and the conversation stays in the
transcript where it belongs.

## Where the resume runs

A resume that re-enters the *wrong tree* is a recovery that quietly did nothing:
the conversation continues, so every exit code says success, while the work
happens in a checkout the session never occupied. The directory is therefore part
of the resume's meaning, not an environment detail.

A session carries two directories, and they are different facts:

| Field | Meaning | Where it comes from |
| --- | --- | --- |
| `Session.ProjectPath` | where the session **started** — the launch directory, recorded once and never overwritten | the published `cwd` on first observation, or the transcript's project slug when the CLI does not list the session |
| `Session.RunsInPath` | where the session **actually runs** — refreshed on every observation | the published `cwd` of the newest `claude agents --json` read |

They disagree exactly when a background session has moved into a linked git
worktree (`.claude/worktrees/…`, or one created with `git worktree add`
elsewhere), which is the case the split exists for. `ccresu sessions` shows both,
under `PROJECT` and `RUNS IN`, and `ccresu inspect` prints `runs in` with
`(same as the project)` when the two agree — so an operator reads the difference
instead of inferring it.

### The resolution order is a rule

`claude.ResolveWorkspace` answers "which directory does this session occupy?" in a
fixed order:

1. **the recorded run directory**, if it is a directory;
2. **the project directory**, if it is a directory.

There is no third step. An empty `exec.Cmd.Dir` inherits the process's working
directory — under the shipped systemd unit, `$HOME` — so "neither is usable" is a
named failure (`claude.ErrWorkspaceUnresolved`), never an inherited cwd. That
error is deliberately **not** wrapped in `ErrUnrecoverable`: a deleted worktree
can be restored and a checkout can be re-cloned, so it is a failed attempt rather
than a verdict on the session.

Recovery resolves from the freshest source first: the observation's `RunsInPath`,
then the persisted row's, then the project directory. The observation is the only
source that knows a session moved since the last pass; the persisted row is the
only source, after a restart, for a session the CLI no longer lists.

### What happens when a worktree disappears

A recorded run directory that is no longer a directory falls back to the project
directory, logs once naming the skipped path, and the attempt proceeds. A missing
worktree is a fact to observe, not a reason to abandon the session — Claude Code
documents clearing the worktree binding, and the clear is recorded in the
transcript.

### What ccresu never does to a worktree

A recovery **re-enters** a tree; it never creates, deletes, locks or unlocks one:

- no `--worktree` / `-w` on a handoff — that would fork the work into a second
  checkout nobody asked for;
- no `git worktree` invocation at all, not even to inspect: the lock is held by
  Claude Code while a backgrounded session runs, and a recovery that waited on it
  would deadlock against the session it is trying to resume;
- no `claude rm` to tidy up: the documented refusals (unpushed commits, an
  unremovable directory) are exactly the cases where automatic removal destroys
  work.

The rule that follows: **N recovery attempts for one session leave exactly the
worktrees that existed before the first attempt.** The lifecycle belongs to Claude
Code and the supervisor, and `ccresu` reports accumulation rather than repairing
it. There is no configuration switch to disable any of this: a
`respect_worktree: false` knob would exist only to reintroduce the bug.

### What pins it

| Test | Requirement |
| --- | --- |
| `TestResolveWorkspacePrefersTheRecordedRunDirectory`, `TestResolveWorkspaceUsesALinkedGitWorktree` | the fixed order, against a real `git worktree add` |
| `TestResolveWorkspaceFallsBackToProjectPathWhenTheRunDirectoryIsGone`, `TestMissingRunDirectoryFallsBackAndProceeds` | a cleared worktree falls back, logs once and still resumes |
| `TestResolveWorkspaceFailsWithANamedErrorWhenNeitherIsUsable`, `TestEmptyWorkingDirectoryIsANamedError` | an empty directory is a named error, and no child is spawned |
| `TestRunDirectoryIsRefreshedOnEveryObservation`, `TestObservationBeatsThePersistedRow` | a mid-life move is picked up, and the newer observation wins over a stale binding |
| `TestResumeSessionInRunsTheHandoffInTheGivenDirectory` | the child's `RunOptions.Dir` is the resolved directory, asserted on the invocation a fake execer received |
| `TestResumeVectorHasNoWorktreeFlag` | the handoff vector contains no `--worktree`, `-w`, `git` or `rm` |
| `TestRecoveryIsWorktreeNeutral` | three attempts leave `git worktree list` byte-identical |
| `TestSessionsShowsTheRunDirectoryDistinctly`, `TestSessionsMarksTheRunDirectoryWhenItMatchesTheProject` | both directories are shown under distinct labels in `sessions` and `inspect` |
| `TestSessionsBrowserShowsTheRunDirectoryColumn` | the browser shows the same column with the same dash-when-identical rule |
| `TestHookCarriesTheRunDirectory`, `TestRenderShowsTheTwoDirectoriesDistinctly` | the notification payload and the message it renders both name the run directory, and omit it when it is unknown or equal to the project |
| `TestToSessionDTOKeepsTheRunDirectoryApart` | the API carries `runs_in_path`, and omits the key when nobody has observed one |
| `TestThereIsNoConfigurationSwitchForWorktrees`, `TestThereIsNoSwitchToDisableTheResolution` | R11: no YAML key or configuration field names a worktree, and `ResolveWorkspace` takes no flag — a knob would fail both |

Every test above is a real one; each was also checked by mutation, so a
`Runs in:` line removed from the template, a `RunsInPath` dropped from the
payload or the wire form, or a `respect_worktree` key added to the configuration
fails a named test rather than passing quietly.

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
15 states, and only the edges written there are legal.

| From | Allowed |
| --- | --- |
| `DISCOVERED` | `RUNNING`, `CHECKING_SESSION` (recover/retry start by checking the session), `DISABLED`, `UNRECOVERABLE`, itself |
| `RUNNING` | `USAGE_LIMIT_DETECTED`, `COMPLETED`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `USAGE_LIMIT_DETECTED` | `WAITING_FOR_RESET`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `WAITING_FOR_RESET` | itself (unknown-reset re-poll or a restart), `RESET_TIME_REACHED`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `RESET_TIME_REACHED` | `CHECKING_SESSION`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `CHECKING_SESSION` | `CHECKING_LIMIT`, `RESUMED` (the supervisor published `working` without a handoff), `WAITING_FOR_INPUT` (it published `blocked`), `RETRY_WAIT` (the status check itself failed, or an unrecognised state was published), `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `CHECKING_LIMIT` | `RESUMING`, `SPEND_CAP_REACHED` (the pre-attempt spend check refused the resume), `RETRY_WAIT`, `WAITING_FOR_RESET` (the limit is still in force), `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED`, `DISCOVERED` |
| `RESUMING` | `RESUMED`, `SPEND_CAP_REACHED` (one attempt cost more than the per-attempt cap allows), `RETRY_WAIT`, `WAITING_FOR_INPUT` (the session is blocked, so the continuation is withheld), `WAITING_FOR_RESET` (the resume was refused because the limit is still in force), `DISCOVERED`, `UNRECOVERABLE`, `USER_TERMINATED`, `DISABLED` |
| `SPEND_CAP_REACHED` | `CHECKING_SESSION` (an explicit `ccresu recover`), `DISCOVERED` (the cap was raised, or a reset), `USER_TERMINATED` — never straight to `RESUMING`, because nothing in this state may spend by itself |
| `WAITING_FOR_INPUT` | `RUNNING`, `RESUMED`, `RETRY_WAIT`, `USER_TERMINATED`, `DISABLED`, `UNRECOVERABLE`, `DISCOVERED`, itself |
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
  `CHECKING_SESSION`, `CHECKING_LIMIT`, `RESUMING`, `RETRY_WAIT`,
  `WAITING_FOR_INPUT`.

`WAITING_FOR_INPUT` is the one pending state ccresu cannot act on: the supervisor
published `blocked`, so a continuation would queue behind a prompt nobody is there
to answer. It is pending because the conversation is not finished — the pass that
re-observes it is what notices the operator unblocked it, and dropping it here
would strand a parked session across a restart. Being pending is not the same as
being resumable: a session in this state is **observed, never handed off**.

`SPEND_CAP_REACHED` is deliberately **not** pending: it is the one state a
reconcile pass must not act on, because rediscovery is how a spend cap would be
defeated. It leaves only by an explicit operator action — raising the cap (the
next pass returns the session to `DISCOVERED`), `ccresu recover <id>`, or
`ccresu retry <id>` — and the state offers no edge to `RESUMING`. Both commands
are manual requests, and a manual request is the one thing the cap does not bind
(PRD 5 R11); the state itself never spends, which is what stops a reconcile pass
from defeating the cap by rediscovering the session.

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

`claude --bg --resume <session-id>` refuses outright when the same session is still
held by another background agent:

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
   `claude --bg --resume …` command that was run, not a command ccresu issued for
   its own bookkeeping.
3. The *identical* resume is issued once more — same argument vector, same
   session id, same continuation message — and its result is the attempt's result.

This is not a retry in the budget sense: the attempt keeps its number, and no new
attempt row is written. It is one attempt that has to clear the way first.

The sequence, from the outside:

| Step | What the CLI sees | What ccresu records |
| --- | --- | --- |
| 1 | `claude --bg --resume <id> --name ccresu-<first-8-hex> "<message>"` — refused, session in the background | the attempt, with its single handoff command as `command` |
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
window issue exactly one `--bg --resume` handoff, and the next is issued after the
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
command:       claude --bg --resume 1347e5db-… --name ccresu-1347e5db <201-byte continuation message>
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
stub `claude` on `PATH`. The stub is the point: it answers `claude agents --json
--all` from a published-state file it maintains, so the real published-state read
is what ccresu decides on, and it records every call with its argv.

What the run asserts is the handoff, one invocation at a time:

```bash
make e2e
```

| Session | Stub behaviour | What is asserted |
| --- | --- | --- |
| handed off | prints `backgrounded · <short-id> · <name>`, publishes `working` | one `--bg --resume` call with the exact vector, `RESUMED`, and an audit row carrying the state id and the `ccresu-<hex>` label |
| refused | the handoff fails with an authentication error | `DISCOVERED` with the refusal recorded verbatim |
| silent | the handoff exits 0 but prints no block | `DISCOVERED` with `claude.ErrHandoffNotAcknowledged`, and no retry |

The call log is asserted as an absence as well: no `-p`, no `--print`, no
`--output-format`, no `claude stop` and no second resume for the same session.
A regression that reintroduced the print-mode path or the stop-then-resume loop
would appear there rather than passing quietly, and the invocation vector is
compared field by field against `claude.HandoffArgs`, so a reordered flag fails
the run too.

The same harness feeds the documentation site: `scripts/web-captures.sh` captures
its log verbatim as the end-to-end figure, and `scripts/web-captures.sh --check`
fails when the committed figure no longer matches the binary.

The Go suite remains the finer-grained proof of the same mechanism: the PRD 2
acceptance tests live in `internal/recovery/published_test.go` and
`internal/claude/published_test.go`, and `go test ./...` plus `go test -race
./...` are the gates.

## The supervisor is a dependency, not a child

`claude`'s own supervisor owns every process a recovery produces, so ccresu asks
it questions instead of doing its work for it. Four rules follow from that.

**One read per pass, and exit 1 is an answer.** `claude daemon status` is run once
per recovery pass, not once per session: the supervisor's state, version and
worker count describe the machine, so answering two sessions from two instants
would be two different facts wearing one label. Its documented exit status is 1
for "there is no supervisor" — normal on a machine that has never run `claude
--bg`, because the next handoff starts one. Any other failure (a timeout, a crash, a
`claude` without the `daemon` subcommand) is `recovery: the supervisor's status
could not be read`, and nothing is handed off that pass: a handoff issued while
the supervisor cannot answer could start a second one. A session that cannot be
asked about is *unknown*, which is not the same as *absent*.

**A version mismatch is named, never silent and never acted on.** When the running
supervisor is a different version from the `claude` ccresu invokes, or below this
build's floor, the pass raises `claude: the running supervisor is a different
version`, carrying all three versions:

```
the running supervisor is 2.0.999 but the claude ccresu invokes is 2.1.300
(floor 2.1.0): run `claude daemon stop --any --keep-workers`, which keeps the
sessions it hosts and lets the next `claude --bg` start a supervisor on the new
version
```

ccresu runs no part of that remedy. It stops the sessions the supervisor hosts,
so it is the operator's decision, and a retry loop that issued it would be a
retry loop that kills work. An unparseable version on either side is recorded,
not turned into a mismatch: the remedy has a real cost, so it is raised only on
evidence.

**ccresu never runs `claude respawn`, and never stops the supervisor on its own
initiative.** The supervisor already restarts unexpected exits and migrates
sessions across its own updates; and on a session whose transcript was cleaned
up, `respawn` re-runs the original prompt, which a recovery must never do. Both
commands are *reported*, in the mismatch above and in
`claude daemon stop --any --keep-workers` as the remedy, and the operator runs
them. `claude stop <job-id>` stays the only lifecycle command ccresu issues, and
only for a user termination.

**A stopped session is not a verdict about the work.** The published state cannot
tell an idle stop from a kill from a session that ended while the supervisor was
off, so `stopped` is never success, is a retryable outcome unless ccresu issued
the stop itself, and is never `permanent_failure`. An idle-stopped session is
continued from its saved conversation, never respawned and never re-prompted.

Nothing here reads a PID. A recovery's process belongs to the supervisor, so
"is it running?" is answered by the published state and by `claude daemon status`,
never by `os.FindProcess` or a stored `PPID` — a check that looks at a number
nothing updates is worse than no check at all.

The whole set talks to one Claude Code instance because one config directory is
resolved once (`CLAUDE_CONFIG_DIR` when set, `~/.claude` otherwise) and used for
both the read and the handoff: two directories are two supervisors, and reading
one while handing off to the other is how a recovery disappears.

## What the supervisor published decides the outcome

A session's outcome is read from `claude agents --json --all`, which is the
supported way to read session state from outside Claude Code. The files under
`~/.claude/jobs/<id>/` are **never** read for state: the docs call them unstable
and say their values are replaced on the next update, so a stale value there would
become a verdict.

One query serves a whole pass — `managers` reset the read at the start of a pass —
because answering different sessions from different instants while claiming they
were observed together is not an observation. `--all` is always passed, regardless
of `discover_all_sessions`: that setting is a discovery and UI preference, and with
it off a finished background session is simply not listed, which a reader could
mistake for a missing one.

The published `state` is the decision; `status` and `waitingFor` are corroboration
and a reason respectively.

| Observed `state` | No handoff outstanding | Handoff outstanding |
| --- | --- | --- |
| `working` | `RESUMED` — it continued on its own once the window reset | no transition; the supervisor owns it |
| `blocked` | `WAITING_FOR_INPUT` | `WAITING_FOR_INPUT` |
| `done` | `COMPLETED` — nothing to continue | `RESUMED` |
| `failed` | `RETRY_WAIT` | `RETRY_WAIT` |
| `stopped` | `RETRY_WAIT` (or `USER_TERMINATED` if ccresu issued the stop) | same |
| absent, session in the listing | no transition — `state` is for background sessions only | `HandoffUnobserved` → `RETRY_WAIT` |
| absent, session not in the listing | no transition — expected before a handoff | `HandoffUnobserved` → `RETRY_WAIT` |
| any other value | `RETRY_WAIT`, value recorded verbatim | `RETRY_WAIT`, value recorded verbatim |

Two properties of that table are load-bearing:

- **It is total.** Every documented value, both "no state" cases and any newer
  value the CLI introduces are matched explicitly, so there is no fall-through
  that could produce success. An unrecognised value becomes a retry with the raw
  value recorded, never a guess.
- **Absence is not an observation.** A session missing from the listing is
  `HandoffUnobserved`, which is neither `failed` nor `stopped` nor a recovery. A
  failed listing is likewise recorded as a failure rather than read as an empty
  listing — that conversion is how an outage becomes a verdict.

A `blocked` observation raises exactly one `needs_input` milestone and never a
success milestone. The milestone is raised whether or not the CLI published a
`waitingFor`: a live payload shows `blocked` entries with no reason at all, so a
missing reason is a fact worth reporting rather than a reason to stay silent.

## Verification: proof, not optimism

A resume that returns exit code 0 has not necessarily continued the
conversation. Which evidence proves it depends on how the continuation was made:

- **A handoff** is proven by the supervisor accepting it — the CLI prints a
  `backgrounded · <short-id> · <name>` block, and `HandoffAccepted` is what sets
  `Proven`. There is nothing left for ccresu to measure, because the work is no
  longer ccresu's process.
- **A continuation ccresu ran itself** is proven by the published state, read
  again after the resume: `Proven` requires the decision to be `RESUMED` **and**
  the session id to have been preserved.

Transcript growth is a **diagnostic**, never proof. It is measured, stored on the
verification and reported in the notes, and it can no longer make `Proven` true on
its own — the transcript is a file the attempt itself writes to, so reading growth
as success records a failed attempt's own write as its achievement. A live process
is likewise reported but never used as proof: a launched process is not a resumed
conversation.

The transcript-growth poll is bounded by `recovery.verification_timeout`
(`DefaultVerificationTimeout = 120s`) and repeats every
`recovery.health_check_interval` (`DefaultHealthCheckInterval = 10s`); a zero
means the default, and a negative value means "check once, never wait". A handoff
never reaches that poll — it is the only resume ccresu issues, and `Verify` returns
through `acceptHandoff` as soon as the `backgrounded` block is parsed — and the
published-state observation has no timeout of its own, so no configured value
bounds how long a handed-off session takes to finish. The bound is a *watch* bound
rather than a proof deadline: a session still `working` when it expires is left
running — the supervisor owns it — and the attempt stays open instead of being
failed for not having finished in time.

"We could not check" and "the check failed" are kept apart: an unreadable listing
returns `ErrObservationUnavailable` and the attempt stays open, while an
observation that positively says `blocked` or `done` settles the outcome.

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
| Session still attached to a background agent | nothing special: the continuation is a handoff to the supervisor that holds it, not a print-mode resume that would collide with it |
| Supervisor published `blocked` | `WAITING_FOR_INPUT`, with the reason carried verbatim; no continuation is issued, because it would queue behind a prompt nobody is there to answer |
| Session missing from the supervisor's listing | `HandoffUnobserved` when a handoff is outstanding, otherwise no transition — never `failed`, never `stopped` |
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
