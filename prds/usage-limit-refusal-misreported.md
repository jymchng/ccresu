# PRD: a refused resume is reported as RESUMED, not as a wait

| | |
|---|---|
| **Status** | Accepted — fix in progress on `fix/usage-limit-after-failed-resume` |
| **Affected version** | v0.5.0 (`355bc44`) and everything before it |
| **Component** | `internal/claude` → `internal/recovery` → `internal/app` (daemon) |
| **Reporter's session** | `1347e5db-a9fb-42eb-b92a-82256bd5aa68` (project `/root/python_projects/agenthicc`) |
| **Owner** | ccresu maintainers |

## 1. Summary

A session paused by a Claude usage limit was resumed after its reset time, the
CLI refused because the limit was **still** in force, and ccresu recorded that
refusal as a **missing binary** and then declared the session **RESUMED** with
"already resumed" and no next attempt. Meanwhile it re-issued the same refused
`claude -p --resume` sixty times in half an hour.

Nothing about the session was broken. The bug is that the refusal — which
contains the one fact that matters, *when the limit resets* — was destroyed at
the first layer that saw it, and every layer above it then made a reasonable
decision from the wrong fact.

## 2. Reported symptom

`ccresu sessions` (and `sessions status <id>`) showed, for a session the
operator could see was still paused:

```
state:         RESUMED
last error:    claude: claude executable unavailable … exit status 1
Next attempt
  at:          none
  why:         already resumed
Latest usage limit:  (empty)
```

Three claims, all false:

1. the `claude` executable was present and running;
2. the session had resumed — it had not, the CLI had just refused;
3. there was nothing to wait for — the CLI had literally named the wait.

## 3. Impact

- **Wrong answer to the only question an operator asks.** The tool said "done"
  about a session that was still blocked, so no action was taken and the work
  silently stopped.
- **Unbounded retry pressure.** Sixty refused resumes in roughly thirty minutes,
  all recorded as failures, none recorded as a limit. Every one of those is a
  subprocess spawn and an API call against an account that is already limited.
- **The wait was lost.** The reset instant printed by the CLI was never stored,
  so nothing could schedule the next attempt — and because the session was
  marked RESUMED, nothing ever would.
- **Misleading telemetry.** `Latest usage limit` was empty, so dashboards and the
  Telegram notifier had no limit event for the window that was actually in force.

## 4. Evidence from the operator's database

Read-only inspection of `/home/ai-slave/.local/share/ccresu/ccresu.db`:

| Fact | Value |
|---|---|
| `recovery_attempts` for the session | 60, **all** `success = 0` |
| Attempts 22–60 | inside a ~30 minute window (hot retry loop) |
| Terminal transition sequence | `CHECKING_SESSION → RESUMED`, reason *"session had already resumed after the limit reset"* |
| `usage_limits` rows for the session | 11, **none** covering the window in force |
| Recorded error text | `claude: claude executable unavailable … exit status 1` |

The banner the CLI actually printed (reproduced in the fixture from the
recorded refusal) was:

```
Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin).
```

## 5. Why the error occurs — the causal chain

Each link is a reasonable local decision that is wrong once the fact above it
was lost. Line numbers refer to v0.5.0 (`355bc44`).

### RC1 — The CLI adapter threw the refusal away (`internal/claude/cli.go:379-383`)

```go
func describeRunError(what string, out Output, err error) error {
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("%w: %w", ErrCLIUnavailable, err)
	}
	return fmt.Errorf("%w: %s", ErrCLIUnavailable, describeDetail(what, out, err))
}
```

Every non-zero exit became `ErrCLIUnavailable`, and `describeDetail` rendered
only the **stderr** tail. The refusal banner is written to **stdout**, so it was
dropped before anything could read it.

`ErrCLIUnavailable` therefore meant three unrelated things at once: the binary is
missing, the call was cancelled, and *the CLI ran and answered with a refusal*.
This is the root of everything below — the layers above could not distinguish a
wait from a broken installation, because they were told it was a broken
installation.

### RC2 — The retry policy believed it (`internal/recovery/classify.go:76-84`)

```go
var nonRetryable = []error{
	claude.ErrCLIUnavailable,   // ← "the binary is gone"
	…
}
```

A refusal was classified **permanent**: the conversation was treated as
something that could not be continued, and a permanent failure routes the
session to `DISCOVERED` — the start of a fresh discovery cycle, with a fresh
attempt budget.

### RC3 — The state machine had no word for "refused" (`internal/recovery/state.go`)

`RESUMING` could go to `RESUMED`, `RETRY_WAIT`, `DISCOVERED`, `UNRECOVERABLE`,
`USER_TERMINATED` or `DISABLED` — but **not** to `WAITING_FOR_RESET`, the state
that means exactly what had just happened. So even a caller that understood the
refusal had nowhere legal to put the session, and the retry path was the only
one available.

### RC4 — Nothing else ever saw the banner (`internal/app/daemon.go`)

The detector that turns a banner into a `usage_limits` row and a schedule only
scans transcripts, and only for sessions in `DISCOVERED` or `RUNNING`:

```go
States: []storage.State{storage.StateDiscovered, storage.StateRunning},
```

The refusal never reached a transcript — it existed only in the stdout of a
process ccresu had just run. So no `usage_limits` row was written, no reset
instant was scheduled, and `Latest usage limit` stayed empty.

### RC5 — The failed attempt's own transcript write looked like success (`internal/recovery/manager.go:383-408`)

Before resuming, the manager takes a transcript baseline and has this shortcut:

```go
if status.Running && grew(baseline, status) {
	… state.move(StateResumed, "session had already resumed after the limit reset", 0)
}
```

The refused `claude -p --resume` writes its own entries to the transcript before
failing. On the next pass, `grew()` compared against a baseline older than that
write and concluded the session had continued on its own. **The attempt's own
failure was read as its success.** This is where `RESUMED` / `already resumed` /
`at: none` came from — the three observed symptoms are one line of code.

### RC6 — Nothing bounded the retries to the window (`fail` → `RetryError`)

With the refusal classified as retryable-by-accident (`DISCOVERED` → fresh
budget → `CHECKING_SESSION` → resume), each pass issued another resume. There was
no notion of "one resume per reset window": the attempt budget is per discovery
cycle, and the cycle restarted every time.

### The resulting loop

```
pass 1: RESET_TIME_REACHED → CHECKING_SESSION → CHECKING_LIMIT → RESUMING
        claude -p --resume  → exit 1, banner on stdout
        → ErrCLIUnavailable (banner lost) → permanent → DISCOVERED
pass 2: transcript grew (because of pass 1's own failed write)
        → RESUMED "session had already resumed after the limit reset"
        → inspect: state RESUMED, at none, why already resumed
```

…and in the operator's case, 58 more resumes that took neither branch cleanly but
kept the loop alive.

## 6. Requirements for the fix

| # | Requirement |
|---|---|
| **R1** | A non-zero exit carries the CLI's own output — stdout **and** stderr — in the returned error. |
| **R2** | A usage-limit refusal is a distinct, recognisable condition (`ErrUsageLimitInForce`), never `ErrCLIUnavailable`. `ErrCLIUnavailable` means only "the process never started". |
| **R3** | Classification: a refusal is never permanent and never routes to `DISCOVERED`. It is its own class, decided from the typed sentinel before any prose marker is consulted. |
| **R4** | State machine: `RESUMING → WAITING_FOR_RESET` exists, because the refusal is the wait it was already in. |
| **R5** | The manager parks the session in `WAITING_FOR_RESET`, records the banner as a `usage_limits` row, saves a schedule at the parsed reset instant (poll delay when unknown), and sets `last_error` to the banner text. |
| **R6** | A failed attempt's own transcript growth must not satisfy the "already resumed" shortcut. A cycle in which a resume was refused can never end in `RESUMED`. |
| **R7** | Exactly one resume per reset window: five passes while limited issue one `claude -p --resume`; the first pass after the reset instant issues the second. |
| **R8** | No existing contract regresses: non-TTY and `--json` output stays byte-identical apart from the corrected values, no new module dependencies, the interactive browser is untouched. |

### Acceptance criteria

- The reproduction test `TestARefusedResumeParksTheSessionUntilTheNextReset`
  passes: `state = WAITING_FOR_RESET`, exactly one resume, `last_error` contains
  the banner, `Latest usage limit` carries `10am (Europe/Berlin)`, and the page
  reads `waiting for the usage limit to reset`.
- `ccresu sessions status` for such a session shows a real `at:` instant and a
  `why:` that names the wait — never `already resumed`, never `at: none`.
- No code path produces `last error: claude executable unavailable` for a
  command that ran.

## 7. Non-goals

- Changing how limits are detected **inside transcripts** — that path works and
  stays as it is.
- Adding provider-specific limit parsing to `internal/claude`; the CLI adapter
  recognises *that* it was refused, and the detector remains the only thing that
  interprets the banner.
- Changing the retry backoff policy for ordinary failures.
- Any new module dependency, any change to the interactive browser.

## 8. Where the fix stands on this branch

| Commit | What it does | Requirement |
|---|---|---|
| `0cace95`, `b992fb4` | Reproduce the defect, red, before any fix | — |
| `2610aeb` | `CLIError` carries exit code + both streams; `ErrUsageLimitInForce`; the prompt is no longer echoed | R1, R2 |
| `52e7c85`, `05f1b7a` | Version probe follows the same rule; the now-false characterisations of the bug are retired | R1, R2 |
| `b502f15` | `ClassLimitInForce`, decided before the prose markers; routes to `WAITING_FOR_RESET`; `RESUMING → WAITING_FOR_RESET` edge | R3, R4 |
| `771c49f` | The refusal is parked: `LimitPark` plans the wait (the detector reads the banner, the scheduler persists it), the banner becomes `last_error` and a `usage_limits` row, and the daemon stops advancing the stale schedule | R5, R7 |

Remaining: R6 (the false-progress shortcut), R8 (contract guards), documentation,
and the release.

## 9. Open questions

1. **A banner that names a past instant.** The scheduler treats a resolved
   instant in the past as "due immediately", which is correct for a schedule but
   turns a refusal into another immediate resume. Should the manager treat a
   past instant from a *refusal* as unknown-reset and poll instead? (Today it
   schedules it due-now, which is what re-arms the loop.)
2. **`usage limit reached for this account`.** This remains a permanent marker
   for untyped errors — an account-level block is not a five-hour window. Confirm
   the CLI never emits that phrasing for a window limit.
3. **Release.** The fix is behavioural, so it ships as `0.6.0` with a CHANGELOG
   entry under a dated heading, following the `v0.5.0` release commit.
