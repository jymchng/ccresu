# How a resume actually happens

This is the part of ccresu that must not be hand-waved: a "recovery" is only a
recovery if the **original conversation** continued. Everything below is what the
code does, and what the end-to-end test asserts.

## Verified interfaces only

ccresu is verified against Claude Code **2.1.257 or newer**
(`claude.MinimumVersion`); an older CLI is refused rather than guessed at. It uses
exactly four interfaces:

| Purpose | Interface |
| --- | --- |
| Discover sessions | `claude agents --json`, with `--all` and `--cwd <path>` |
| Session transcript | `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl` |
| Per-session environment | `~/.claude/session-env/<session-id>/` |
| Continue the original session | `claude --resume <session-id> "<continuation message>"` |

The resume vector is built in one place, `claude.ResumeArgs`, and is:

```go
[]string{"--resume", sessionID, message}
```

That is the whole command, and the short vector is the point: there is no
background flag, no session label, no model or effort flag, no guardrail flag and
no `--` separator. A resume runs the turn itself, so the invocation stays alive
while the continuation works and its **exit status is the verdict** — zero means
the conversation continued, anything else is a failed attempt, and the provider
turns that non-zero status into an error before the manager decides anything.

There is no print mode either: a resume writes no JSON block to a pipe, which is
why nothing is parsed out of one and why no missing block can be mistaken for a
missing resume.

The prohibitions are unchanged: no `--fork-session` (it would continue a copy
instead of your conversation), no new session id, no TTY, no keystroke injection
into your terminal and no other undocumented subcommand. The message is limited to
8 KiB, must be non-empty, and the session id is validated before anything is
executed, so neither can be read as a flag or a path.

### Why the message is the last argument

The prompt is a **positional** argument: no flag introduces it. Position is
therefore the only thing that says which session it is being said to, and the
documented form is `claude --resume <session-id> <prompt>` — the prompt comes after
the session. ccresu keeps it last and builds the vector in exactly one place:

```go
// internal/claude/resume.go
func ResumeArgs(sessionID, message string) []string {
	return []string{"--resume", sessionID, message}
}
```

`TestResumeArgsIsExactlyTheVerifiedOne` asserts that vector, and
`TestResumeCommandIsExactlyTheVerifiedOne` reads the flag names out of
`internal/claude/resume.go`, so a new flag has to be added deliberately rather than
drifting in. Reordering the arguments is a failing test rather than a puzzling
runtime refusal — and swapping them would hand the session id to the model as the
prompt.

The audit trail has to describe the same command. The attempt row's `command`
column is rendered by `commandPreview` in `internal/recovery/manager.go`:

```text
claude --resume <session-id> <N-byte continuation message>
```

The placeholder for the message sits where the real message sits — last. `ccresu
history` is an audit trail, so a preview that disagrees with `claude.ResumeArgs` is
a worse bug than no preview at all. Both facts are pinned by tests:
`TestResumeArgsIsExactlyTheVerifiedOne` for the vector that is executed, and
`TestCommandPreviewAndFormattingHelpers` for the preview text, its message summary
and the retired flags it must never record again.

The message itself is never stored on the row: the column says how many bytes were
sent, the log says what happened, and the conversation stays in the transcript
where it belongs.

## Where the resume runs

A resume that re-enters the *wrong tree* is a recovery that quietly did nothing:
the conversation continues, so every exit code says success, while the work
happens in a checkout the session never occupied. The directory is therefore part
of the resume's meaning, not an environment detail.

A session carries two directories, and they are different facts:

| Field | Meaning | Where it comes from |
| --- | --- | --- |
| `Session.ProjectPath` | where the session **started** — the launch directory, recorded once and never overwritten | the `cwd` of the first `claude agents --json` listing that named the session, or the transcript's project slug when the CLI does not list it |
| `Session.RunsInPath` | where the session **actually runs** — refreshed on every read of that listing | the `cwd` of the newest `claude agents --json` read |

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

Recovery resolves from the freshest source first: the directory reported by the
status read taken immediately before the resume, then the persisted row's, then
the project directory. The status read is the only source that knows a session
moved since the last pass, because it re-reads the listing for that one session;
the persisted row is the only source, after a restart, for a session the CLI no
longer lists.

### What happens when a worktree disappears

A recorded run directory that is no longer a directory falls back to the project
directory, logs once naming the skipped path, and the attempt proceeds. A missing
worktree is a fact to observe, not a reason to abandon the session — Claude Code
documents clearing the worktree binding, and the clear is recorded in the
transcript.

### What ccresu never does to a worktree

A recovery **re-enters** a tree; it never creates, deletes, locks or unlocks one:

- no `--worktree` / `-w` on a resume — that would fork the work into a second
  checkout nobody asked for;
- no `git worktree` invocation at all, not even to inspect: the lock is held by
  Claude Code while a background session runs, and a recovery that waited on it
  would deadlock against the session it is trying to resume;
- no `claude rm` to tidy up: the documented refusals (unpushed commits, an
  unremovable directory) are exactly the cases where automatic removal destroys
  work.

The rule that follows: **N recovery attempts for one session leave exactly the
worktrees that existed before the first attempt.** The lifecycle belongs to Claude
Code, and `ccresu` reports accumulation rather than repairing it. There is no
configuration switch to disable any of this: a
`respect_worktree: false` knob would exist only to reintroduce the bug.

### What pins it

| Test | Requirement |
| --- | --- |
| `TestResolveWorkspacePrefersTheRecordedRunDirectory`, `TestResolveWorkspaceUsesALinkedGitWorktree` | the fixed order, against a real `git worktree add` |
| `TestResolveWorkspaceFallsBackToProjectPathWhenTheRunDirectoryIsGone`, `TestMissingRunDirectoryFallsBackAndProceeds` | a cleared worktree falls back, logs once and still resumes |
| `TestResolveWorkspaceFailsWithANamedErrorWhenNeitherIsUsable`, `TestEmptyWorkingDirectoryIsANamedError` | an empty directory is a named error, and no child is spawned |
| `TestRunDirectoryIsRefreshedOnEveryObservation`, `TestObservationBeatsThePersistedRow` | a mid-life move is picked up, and the fresher read wins over a stale binding |
| `TestResumeSessionInRunsTheResumeInTheGivenDirectory` | the child's `RunOptions.Dir` is the resolved directory, asserted on the invocation a fake execer received |
| `TestResumeVectorHasNoWorktreeFlag` | the resume vector contains no `--worktree`, `-w`, `git` or `rm` |
| `TestRecoveryIsWorktreeNeutral` | three attempts leave `git worktree list` byte-identical |
| `TestSessionsShowsTheRunDirectoryDistinctly`, `TestSessionsMarksTheRunDirectoryWhenItMatchesTheProject` | both directories are shown under distinct labels in `sessions` and `inspect` |
| `TestSessionsBrowserShowsTheRunDirectoryColumn` | the browser shows the same column with the same dash-when-identical rule |
| `TestHookCarriesTheRunDirectory`, `TestRenderShowsTheTwoDirectoriesDistinctly` | the notification payload and the message it renders both name the run directory, and omit it when it is unknown or equal to the project |
| `TestToSessionDTOKeepsTheRunDirectoryApart` | the API carries `runs_in_path`, and omits the key when none has been recorded |
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
   (`defaultTranscriptTail`), joins them, and hands the text to the detector. It
   reads `DISCOVERED` sessions always, and — when `monitoring.rescan_resumed` is
   on, which is the default — also `RESUMED`, which is Settled and which no other
   pass revisits, so a limit landing after a resume would otherwise never be seen.
   A rescan can only park the session in `WAITING_FOR_RESET`; it can never resume
   or spend (see [the state machine](#the-state-machine)). An operator who knows a
   `RESUMED` session has gone idle asks for the same read with
   `ccresu search-limit <session-id>`, which takes the identical walk
   `RESUMED → DISCOVERED → WAITING_FOR_RESET` and writes the same rows.
   The search itself is not restricted to idle sessions: it reads the transcript
   in *any* state, because parsing a banner is how an operator asks what reset a
   session's limit names — and the state where that question matters most is
   `WAITING_FOR_RESET`, whose stored instant may be stale, parsed from an older
   banner, or a poll of a reset the transcript has since named. What the state
   restricts is the *write*: a session already in `WAITING_FOR_RESET` has its wait
   re-planned from the banner that is in the transcript now, and the
   `WAITING_FOR_RESET → WAITING_FOR_RESET` self-edge records the change;
   `RESUMING` has a pass that reads the transcript anyway, and `COMPLETED`,
   `DISABLED` and `UNRECOVERABLE` are verdicts a read must not walk back, so those
   four are read, reported and left untouched.
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

States are persisted; every change writes a `state_transitions` row. There are
seven of them, because ccresu does one thing: it reads a session's transcript,
waits for the usage limit the banner named to reset, hands the session a
continuation, and goes back to reading. Those four beats are the whole of it, and
three verdicts stop it.

```text
DISCOVERED → WAITING_FOR_RESET → RESUMING → RESUMED → DISCOVERED      one lap, and it repeats
                    ↑                  │
                    └──────────────────┘                              the resume was refused

DISCOVERED ──→ COMPLETED / DISABLED / UNRECOVERABLE                   the three verdicts
DISABLED   ──→ DISCOVERED                                             ccresu enable
```

`DISCOVERED` is both the start of a lap and the place a lap that found nothing
stays: it is the transcript read, and a failed attempt or a spent attempt budget
returns the session there so the next discovery pass reads its transcript again
and schedules the next reset. `WAITING_FOR_RESET` is the durable wait on the
instant the banner named — a row in `schedules`, not a timer. `RESUMING` is the
one beat that spends: an attempt row is recorded and the continuation is run, as
one blocking `claude --resume <session-id> "<continuation message>"` in the
session's own directory, and the beat is left by that command's exit status —
zero means `RESUMED`, anything else is a failed attempt. `RESUMED` is the landing
beat, and its edge back to `DISCOVERED` is what makes the loop a loop — the next
scan notices the next limit and a new lap starts.

`RESUMED` exists as a state, rather than an immediate return to `DISCOVERED`, for
one reason: the banner that parked the session is still inside the transcript tail
the detector reads, so a session that went straight back to `DISCOVERED` would be
re-parked and re-resumed off its own stale banner.

Legal edges are declared once, in `internal/recovery/state.go`, and any other
edge is rejected with `ErrInvalidTransition`. The map is the authoritative table:
seven states, and only the edges written there are legal.

| From | Allowed |
| --- | --- |
| `DISCOVERED` | itself (the scan found nothing), `WAITING_FOR_RESET` (a limit was parsed out of the transcript), `RESUMING` (`ccresu recover`/`retry` asks for a resume now), `COMPLETED`, `DISABLED`, `UNRECOVERABLE` |
| `WAITING_FOR_RESET` | itself (an unknown-reset poll, or a restart re-observing the wait), `RESUMING` (the parsed instant arrived), `DISCOVERED` (the wait no longer applies, a permanent failure, or a spent budget), `COMPLETED`, `DISABLED`, `UNRECOVERABLE` |
| `RESUMING` | `RESUMED` (the resume exited zero), `WAITING_FOR_RESET` (refused, because the limit is still in force — or a retryable failure waiting out its backoff), `DISCOVERED` (a permanent failure, or a spent budget), `DISABLED`, `UNRECOVERABLE` |
| `RESUMED` | `DISCOVERED` (the next limit, via the rescan or `ccresu search-limit`), `COMPLETED`, `DISABLED`, `UNRECOVERABLE` — never `RESUMING`, and never straight back to `WAITING_FOR_RESET` |
| `COMPLETED` | — (terminal) |
| `DISABLED` | `DISCOVERED` — the only edge out, and only `ccresu enable` takes it |
| `UNRECOVERABLE` | — (terminal) |

A repeat `WAITING_FOR_RESET → WAITING_FOR_RESET` is legal because an
unknown-reset session re-enters the waiting state after every poll and a restart
re-observes the wait; moving to the state it is already in still writes the audit
row, because "the limit was checked again and was still in force" is worth
keeping. `RESUMED` is not strictly terminal — a resumed session can hit its limit
again — but nothing resumes it from there, and the only way back towards a wait is
through `DISCOVERED`, which the rescan and `ccresu search-limit` write before the
park. The edge goes through `DISCOVERED` rather than straight to
`WAITING_FOR_RESET`, so the audit trail records the read that produced the wait;
there is no edge from `RESUMED` to `RESUMING` at all, because a session that was
just resumed must be read again before it can be resumed a second time.

Every beat a pass can begin in — `DISCOVERED`, `WAITING_FOR_RESET` and
`RESUMED` — has the same four exits, so no beat can strand a session:
`DISCOVERED` (the way back into the loop), `COMPLETED`, `DISABLED` and
`UNRECOVERABLE`. `RESUMING` is the exception, and it is a matter of timing rather
than policy: it is entered immediately before the resume runs and left by that
resume's exit status, so no pass is ever looking at a session in it. It keeps
three of the four exits and has no `COMPLETED` edge, because an edge no pass can
take is a promise the machine does not keep. `RESUMED` has exactly one way in —
from `RESUMING`, when a resume finished its turn. There is no edge into `RESUMED`
from `DISCOVERED` or from `WAITING_FOR_RESET`, because a state a pass cannot reach
is a state an operator reads in `ccresu history` and cannot explain.

### Which states are terminal, settled and pending

Three predicates classify a state, and they are what every caller consults:

- **Terminal** — nothing automatic runs from here: `COMPLETED`, `DISABLED`,
  `UNRECOVERABLE`. `COMPLETED` and `UNRECOVERABLE` have no edge out at all;
  `DISABLED` is terminal to every pass and every command except `ccresu enable`,
  which is the human decision to turn the loop back on.
- **Settled** — must be left alone: `RESUMED`, `COMPLETED`, `DISABLED`,
  `UNRECOVERABLE`. Recovery refuses to touch a settled session, which is what
  stops a completed conversation from being resumed again and what stops a
  just-resumed session from being resumed off its own stale banner.
- **Pending** — still wants automatic work, and what a restarted daemon must pick
  up: `WAITING_FOR_RESET` and `RESUMING`. `DISCOVERED` is deliberately **not**
  pending: the scan reads every discovered session on its own schedule, and
  treating it as pending would make the recovery pass resume sessions that never
  showed a limit.

There is no "waiting for a human" state and no park for a blocked session: a
session whose transcript tail shows an unanswered question is handed a continuation
like any other, because a continuation does not have to wait for an answer. What
the CLI says about a blocked session is a fact about the conversation, not a verdict
about the resume — the only verdict ccresu takes is the resume's exit status.

`DISABLED` is where a spend stop lands. A session that ccresu refuses to spend on
— because the attempt would cross `recovery.guardrails.max_cost_usd_per_attempt`,
or because the session has recorded more than `...max_cost_usd_per_session` — is
disabled with a reason that names the knob and both numbers. No reconcile pass
resumes a disabled session, so the cap cannot be defeated by rediscovery; the way
back is `ccresu enable <id>`, and a manual `ccresu recover <id>` is the one thing
the cap does not bind (PRD 5 R11).

There is no terminal *failure* state: the only state ccresu declares failed is
`UNRECOVERABLE`, and only because it has positively established it has no verified
way to continue the conversation. A spent attempt budget returns the session to
`DISCOVERED`, so it is a pause, not a failure.

A retryable failure is a *wait*, not a state of its own. It parks the session in
`WAITING_FOR_RESET` with a `retry_at` instant on the row, so the session keeps the
attempt budget it has already spent, and the daemon's `recover` stage wakes it when
that instant is due. The delay is exponential with jitter — `initial_delay` 30s,
×2, capped at `max_delay` 15m, ±`jitter` 15% — so the sequence is roughly 30s, 1m,
2m, 4m, 8m, then 15m. A session parked on a usage-limit reset has no `retry_at`:
that wait is the scheduler's, and the two are told apart by the column, not by a
state name. The attempt budget is `DefaultMaxAttempts = 10` per discovery cycle.

A resume the CLI **refused** because the limit is still in force is neither: it is
not a failure to retry and not a success to celebrate, so it parks the session in
`WAITING_FOR_RESET` until the reset the banner named — see
[When the resume is refused because the limit is still in force](#when-the-resume-is-refused-because-the-limit-is-still-in-force).

A failure **before** the resume is issued — the status read or the spend check,
both of which run in whatever state the pass found the session in — is different:
no attempt row exists, so no budget was spent, and the session rests in
`WAITING_FOR_RESET` with a retry instant, whatever the error is classified as (an
`UNRECOVERABLE` verdict still wins, and a spend stop is `DISABLED`). Its
transition reason says so explicitly, e.g. `the pre-resume check failed
(permanent); no attempt was spent, next check in 1m0s: …`, so `ccresu history`
explains the wait. Nothing before the resume asks whether the usage window has
cleared: that question is answered by the resume itself, and a banner in its
output is what parks the session.

The **attempt budget** is measured per discovery cycle, not per session. The guard
runs before any check and counts the attempts started since the session last
entered `DISCOVERED`; when ten of them have been spent it records `maximum
attempts reached (10); returned to discovery for the next reset`, moves the
session to `DISCOVERED`, and returns `errors.Is(err, ErrMaxAttempts)`.

Entering `DISCOVERED` therefore starts a fresh budget — which is the whole point:
the daemon rediscovers the session, reads its transcript, plans the next reset and
tries again, without anyone running a command. The budget is a safety valve
against a tight loop inside one cycle, not a verdict about the session. Because
the guard can fire wherever a session is parked — `WAITING_FOR_RESET` after an
unknown-reset poll, `RESUMING` after a crash mid-check — every beat that still
permits automatic work has an edge to `DISCOVERED`.

### Coming from 0.8.0: nine states are gone

Version 0.9.0 collapsed the machine from sixteen states to these seven. Each
retired state was a detour off the loop, and each is now part of the beat it
belonged to:

| Retired state | Where it went | Why |
| --- | --- | --- |
| `RUNNING` | `RESUMED` | a session that is alive after a resume; still scanned, which is how the next limit is noticed |
| `USAGE_LIMIT_DETECTED` | `WAITING_FOR_RESET` | detecting a limit *is* parking on the instant it named |
| `RESET_TIME_REACHED` | `WAITING_FOR_RESET` | a due schedule now wakes the session straight into `RESUMING`, so there is no beat to record in between |
| `CHECKING_SESSION` | `RESUMING` | the pre-flight read is part of one resume |
| `CHECKING_LIMIT` | `RESUMING` | the limit check is part of the same resume |
| `RETRY_WAIT` | `DISCOVERED` | a backoff was never a condition, only a wait; the wait is `sessions.retry_at`, and the beat that reads the wait is discovery |
| `WAITING_FOR_INPUT` | `DISCOVERED` | a blocked park is rediscovered like any other; `blocked` is a CLI-side status, not a state, and a session handed a continuation is `RESUMED` |
| `USER_TERMINATED` | `DISABLED` | both are "the operator stopped it; only `ccresu enable` restarts it" |
| `SPEND_CAP_REACHED` | `DISABLED` | a spend stop is a decision that nothing more may be spent until an operator acts |

Nothing is lost on upgrade. The seven survivors are a subset of the sixteen, so
migration `0013_simplify_states.sql` rewrites every row carrying a retired name —
in `sessions`, `recovery_attempts`, `state_transitions` and `deleted_sessions` —
through exactly the table above, at the next startup. A session arriving at
`DISCOVERED` from
`RETRY_WAIT` or `WAITING_FOR_INPUT` also has its `retry_at` cleared, because
`DISCOVERED` owns no retry instant and `ccresu sessions` would otherwise report a
wait nothing would act on. A binary rolled back to 0.8.0 reads every migrated row
without complaint; what cannot be restored is the distinction the collapse
removed, and nothing tries to.

## When the session is still attached to a background agent

`claude --resume <session-id>` fails when the same session is still held by another
background agent:

```console
Error: That session is running in the background (837eaaea). Run …
```

Nothing about the conversation is lost here, so the refusal must not end the
session's life — and it does not. ccresu reads it as what it is: a failed resume
whose exit status was non-zero, with the CLI's own words as its error.

- **It is not a limit.** The message carries no reset banner, so nothing is parked
  on a reset instant.
- **It is not an authentication failure.** No permanent marker matches it, so
  `recovery.Classify` falls through to the retryable case and the attempt waits out
  its backoff — `initial_delay` 30s, ×2, capped at `max_delay` 15m, ±15% jitter —
  instead of ending the session.
- **It is not ccresu's problem to solve.** ccresu runs one command, the resume, and
  issues no lifecycle command at all: it never attaches to the agent, never stops it
  and never reads its log. Releasing the session is your decision, because stopping
  an agent that might be doing work is a destructive act taken on your behalf.

The refused attempt's own transcript writes are absorbed into the stored baseline.
The CLI appends its refusal to the conversation, so the file has grown by the time
the next attempt runs; that growth is the attempt's own work, and reading it as
progress would describe a refused resume as a success. The verdict is the exit
status, and the growth beside it is only a measurement.

If the refusal keeps repeating, the choices are the documented ones: attach to that
agent, stop it, or let the session go with `ccresu disable <session-id>`.
`ccresu retry <session-id>` and `ccresu recover <session-id> --force` are there if
you want the question asked again right now.

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
a reset instant. A refusal whose parker could not plan writes no schedule at all,
so the row this pass loaded stays the only wait there is and is advanced as a poll
instead; otherwise an unknown-reset schedule would stay due and every pass would
ask again. The bound is asserted end to end: five reconcile passes inside one
window issue exactly one resume, and the next is issued after the
reset instant arrives.

The refused attempt's own transcript writes are absorbed into the stored
baseline: the CLI appends its refusal to the conversation, so the file has grown by
the time the reset comes, and that growth is the attempt's own work. A refused cycle
is a wait, and a wait is not a resume.

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
command:       claude --resume 1347e5db-… <201-byte continuation message>
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
stub `claude` on `PATH`. The stub is the point: it answers `claude agents --json`
for discovery, answers the resume with an exit status of its choosing, appends a
line to the session's transcript when the turn "ran", and records every call with
its argv.

```bash
make e2e
```

| Session | Stub behaviour | What is asserted |
| --- | --- | --- |
| continues | the resume exits 0 and appends the turn to the transcript | one `--resume` call with the exact vector, `RESUMED`, `proven: the resume finished its turn and the transcript grew`, and `TranscriptGrew: true` on the attempt row |
| refused | the resume exits 1 with an authentication error | exactly one resume, `DISCOVERED`, the exit status named in the report (`failed (exit 1)`) and the stub's stderr kept verbatim as the attempt's error |
| quiet | the resume exits 0 and writes nothing to the transcript | still `RESUMED`, reported as `proven: the resume finished its turn` with no claim of growth and `TranscriptGrew: false` on the attempt row |

The quiet session is the diagnostic being a diagnostic: its turn finished, the
transcript ccresu watches did not change shape, and the session is resumed anyway,
because the exit status is what decided it. The refused session is the mirror
image: a real attempt, a non-zero exit, and a conversation nobody declared broken.

The call log is asserted as an absence as well. Every resume must be exactly three
arguments — the id and the prompt, with no `--` separator and no fourth word — and
no call may carry a flag other than `--resume`: not the retired background flag,
not a session label, not a print-mode or output-format flag, and not a model,
turn, budget or permission override. Nor may any call be a lifecycle command or a
second resume for the same session. A regression that reintroduced the
print-mode path, a stop-then-resume loop or a lookup of the retired background
process would appear there rather than passing quietly, and the invocation vector
is compared field by field against `claude.ResumeArgs`, so a reordered argument
fails the run too.

The same harness feeds the documentation site: `scripts/web-captures.sh` captures
its log verbatim as the end-to-end figure, and `scripts/web-captures.sh --check`
fails when the committed figure no longer matches the binary.

The Go suite remains the finer-grained proof of the same mechanism, and `go test
./...` plus `go test -race ./...` are the gates.


## Verification: a record, not a second opinion

The verdict is the resume's exit status, taken when the turn returned: a non-zero
exit becomes an error in the provider, so a resume that produced a
`recovery.Verification` at all ran its turn to the end. Everything after that is a
record of what the turn left behind.

`Verification.Proven` restates the status the manager has already branched on
(`ExitCode == 0`). `ExitCode` itself is kept, and the transcript is measured around
the turn — snapshotted before the resume, read once after it — and reported as
`TranscriptGrew`, with `LinesBefore`/`LinesAfter` and `SizeBefore`/`SizeAfter`
behind it.

That measurement is a **diagnostic and never evidence**:

- a session can be continued without the transcript ccresu is watching changing
  shape — a compaction, a different file, a turn that answered nothing — so missing
  growth cannot be allowed to call a finished turn a failure;
- a transcript can grow without the original conversation being the one that
  advanced, so growth cannot be allowed to call a failed resume a success.

It is read **once**, and nothing waits for it: the resume ran the turn itself and
has already returned. The old verifier polled because it was watching a background
dispatch whose end it could not see; that poll, and the
`recovery.verification_timeout` and `recovery.health_check_interval` knobs that
bounded it, are gone with it.
The one bound left is `recovery.turn_timeout` (`claude.DefaultTurnTimeout`, `30m`),
which kills a turn that outlives it and records the attempt as a deadline.

A measurement that cannot be taken is recorded as a note — "the transcript could
not be measured: …", "the transcript line count before the resume was unknown, so
growth could not be measured" — and the diagnosis is simply absent. A read that
failed is missing evidence, never a second verdict.

### Closing the attempt

A resume that finished its turn closes the attempt: `FinishAttempt` writes
`state = RESUMED`, `success = true`, `transcript_grew` from the measurement and the
cost the CLI reported. The manager stores the new transcript metrics when there are
any to store, clears `last_error`, and moves the session to `RESUMED` with the
reason `verification.Reason()` — `the resume finished its turn and the transcript
grew`, or `the resume finished its turn`.

A failed resume's own writes are absorbed into the baseline by
`absorbAttemptWrites` before the next decision, by re-reading the transcript —
otherwise a later status check would see growth the failed attempt caused itself and
describe a refused resume as a success.

The attempt row is written *before* the resume is issued (`StartAttempt`), and its
`UNIQUE (session_id, attempt_number)` index is the arbiter, so a duplicate
allocation loses in the database rather than in a race. `StartAttempt` retries
allocation up to `maxAttemptAllocationRetries = 8` and treats the unique index as
the final word.

## What is not recoverable

| Situation | Reported as |
| --- | --- |
| Session type ccresu does not know (from `claude agents --json`) | `UNRECOVERABLE`, with the reported type in the reason |
| Claude Code older than 2.1.257, or missing from `PATH` | refused (`ErrCLIUnavailable`), never guessed |
| Authentication failure or invalid API key | classified as not a usage limit; never retried after a reset |
| Network error, crashed tool, user cancellation | not a usage limit; not resumed |
| Normal completion | recognised as completion; not resumed |
| Limit still in force at resume time | the attempt is finished with the banner, the banner is recorded as a usage limit, and the session returns to `WAITING_FOR_RESET`; no further resume until the reset it named |
| Session still attached to a background agent | a failed resume with the CLI's refusal recorded verbatim, classified retryable and retried under the backoff; releasing the agent is the operator's decision |
| Session blocked on an unanswered question | handed a continuation like any other session — a continuation does not have to wait for a human |
| Session missing from `claude agents --json` | no transition — the listing is discovery input, never a verdict on a resume |
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
