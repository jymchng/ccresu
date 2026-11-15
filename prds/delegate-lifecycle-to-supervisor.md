# PRD: lifecycle belongs to the supervisor, and ccresu should stop imitating it

| | |
|---|---|
| **Status** | `Implemented` in `v0.7.0` (`c603ee8`) — `claude.DaemonStatus`, `claude.DaemonLogs`, `recovery.SupervisorSource`, the interrupted attempt and the retimed systemd unit are on `main`, and every requirement in §6 has a test. This document stays the specification it was; §2 of [the index](README.md) is the record of what shipped |
| **Affected version** | `v0.6.0` (`55bf09e`) |
| **Component** | `internal/claude` → `internal/recovery` → `internal/app` (daemon), `deployments/systemd` |
| **Sources** | <https://code.claude.com/docs/en/agent-view>, <https://code.claude.com/docs/en/cli-reference> |
| **Owner** | ccresu maintainers |

## 1. Summary

`ccresu` is a watchdog with a lifecycle policy of its own. It runs as a user
systemd service with `Restart=on-failure`, `KillSignal=SIGTERM` and
`TimeoutStopSec=60s`, and the unit's own comment explains the timeout: "give an
in-flight `claude -p --resume` child a chance to finish or be killed cleanly
before systemd escalates to SIGKILL" (`deployments/systemd/ccresu.service:38-43`).
It spawns that child, kills it when the context is cancelled
(`internal/claude/exec.go:76`), records an unfinished attempt as a **failure**
when the child disappears (`internal/recovery/manager.go:802`), and re-issues the
resume on the next pass.

Claude Code now ships the supervisor that policy was imitating, and the docs
describe it doing the same three things with better information: a session that
"exited unexpectedly while the supervisor is running" is restarted; after an
auto-update "the supervisor restarts itself onto the new version and moves idle
sessions over in the background", leaving working, blocked and attached sessions
alone; and a session that has finished and stayed unattached "for about an hour"
is stopped to free resources and resumed from disk on the next attach. It also
publishes its own version and worker count through `claude daemon status`, which
"exits 1 if the supervisor isn't running" and warns when the supervisor's version
differs from the invoking CLI's.

The proposal is to consult and command that supervisor instead of racing it.
`ccresu` keeps the decision it is uniquely able to make — *when* a usage-limit
window has reset — and delegates every question about *what a process is doing*:
liveness and version from `claude daemon status`, termination from
`claude stop <id>`, evidence from `claude logs <id>`. It does not respawn, and it
does not stop the supervisor. And it stops treating "the child exited" as
"the recovery failed", because the supervisor may have kept the work running.

This PRD depends on PRD 1 and PRD 2. PRD 1 makes a recovered session the
supervisor's process rather than ccresu's; PRD 2 supplies the published state
that replaces the PID guess this PRD removes. It reconciles with what exists
rather than replacing it: `stop.go`'s eight-hex job-id path is kept exactly as
it is and changes only in *why* it is called, and `internal/process` stays
read-only and gains no lifecycle caller.

## 2. What an operator sees today

| Observation | Why it happens |
|---|---|
| A systemd restart of `ccresu` reports the in-flight recovery as failed | an attempt with no finish time is closed as `Success: false` (`manager.go:802-836`) |
| The same restart immediately retries the session | the close moves it to `RETRY_WAIT`, so the next pass resumes it again |
| A session the supervisor stopped for idleness looks like a crash to `ccresu` | ccresu has no way to ask the supervisor anything, so "no process" is the only fact it has |
| After a Claude Code auto-update, `ccresu` keeps using the version it probed at startup | the CLI version is cached once per process (`cli.go:152-172`); the supervisor's version is never read |
| `ccresu` can stop a session but cannot ask whether one is running, or restart one | `stop` is the only lifecycle verb in the adapter, and it exists to clear an obstruction |
| The systemd unit still budgets a minute for a `claude -p --resume` child | the unit predates the handoff, which returns in seconds (PRD 1) |

The first two rows are the same event seen twice: a restart that the supervisor
absorbs becomes, in ccresu's database, a failure and a retry. The last row is the
quiet one — the deployment artefact encodes an ownership model the rest of this
set removes.

## 3. Impact

- **Two restart loops, opposite conclusions.** systemd restarts ccresu; ccresu
  records a failed attempt and resumes; the supervisor may be restarting the same
  session at the same moment. Neither knows the other exists, and the operator
  sees a failure for work that never stopped.
- **A destructive verb and no read-only ones.** The only lifecycle command
  ccresu has is `claude stop`, and its documented purpose in the code is to
  remove an obstruction. There is no way to ask "is a supervisor running", "what
  version is it", or "restart this one".
- **A version skew nobody can see.** The CLI's version and the supervisor's
  version are two different facts, and the docs' warning exists precisely because
  they diverge after an update. ccresu reads the first and caches it, so it will
  happily command a stale supervisor for the life of the process.
- **A stored identity that is never verified.** `PID`, `PPID` and
  `ProcessStartTime` are persisted "so a recycled PID is never mistaken for the
  original process" (`models.go:160-164`), but only `PID` is ever written, and
  the package built to verify identity has no production caller. The last-resort
  `session.PID > 0` in `sessionRunning` is therefore a guess about a PID from a
  previous boot.
- **PRD 2 cannot finish without this.** Its mapping decides from the published
  state; this PRD is what makes "the supervisor is not there" a *normal* answer
  rather than an error.

## 4. Evidence

### 4.1 From the two documentation pages

| Fact | Source |
|---|---|
| The supervisor is a background service that runs background sessions so they keep working after the terminal closes; Claude Code starts it on the first `--bg` or `claude agents` | `agent-view` §The supervisor process |
| Each session is its own Claude Code process under the supervisor | `agent-view` §The supervisor process |
| Working, paused on a permission prompt or other dialog, or attached: the process keeps running | `agent-view` §The supervisor process |
| Finished or waiting for your next message **and unattached for about an hour**: the supervisor stops the process; the conversation stays on disk and the next attach or reply resumes it. Pinning with `Ctrl+T` keeps it running | `agent-view` §The supervisor process, §A session is slow to respond after attaching |
| **Exited unexpectedly while the supervisor is running**: the supervisor restarts the process. A session backgrounded with `←`/`/background` and then killed is marked stopped instead | `agent-view` §The supervisor process |
| **After an auto-update**: the supervisor restarts itself onto the new version and moves idle sessions over; working, waiting-on-you and attached sessions are not interrupted | `agent-view` §The supervisor process |
| **The supervisor itself stops**: on macOS and Linux sessions stop unless a new supervisor connects within about a minute | `agent-view` §The supervisor process |
| The notice an exiting agent view prints: "Background sessions still running will stop in about a minute. Run 'claude agents' to keep them." | `agent-view` §Keep sessions running after the supervisor stops |
| `claude daemon status` prints the supervisor's state, version, socket directory and worker count, and **exits 1 if the supervisor isn't running** | `cli-reference` `claude daemon status` |
| `claude daemon status` warns when the running supervisor is on a different version than the `claude` invoked, shows both versions, and tells you to run `claude daemon stop --any` to pick up the new version | `agent-view` §Where state is stored |
| `claude daemon stop --any` stops the supervisor **and the background sessions it hosts**; `--keep-workers` leaves them running for the next supervisor to reconnect to | `cli-reference` `claude daemon stop`; `agent-view` §Manage sessions from the shell |
| `claude respawn <id>` restarts a session, running or stopped, with its conversation intact; `--all` restarts every running session, e.g. to pick up an updated binary | `cli-reference` `claude respawn` |
| `claude stop <id>` stops a background session; it also accepts `claude kill` | `cli-reference` `claude stop` |
| `claude logs <id\|name>` prints a session's recent output; `claude daemon logs` follows `~/.claude/daemon.log` | `cli-reference` `claude logs`, `claude daemon logs` |
| `claude rm <id>` removes a session from the list (and its worktree when safe); the conversation transcript stays on disk and remains available through `claude --resume` | `cli-reference` `claude rm` |
| If transcript cleanup removed a stopped session's saved conversation, `claude rm` deletes the row and **`claude respawn <id>` runs its original prompt again** | `agent-view` §Sessions show as failed or stopped after shutdown |
| After a shutdown, a session that last made progress within 48 hours shows as `failed` and restarts from its saved conversation on attach or reply; past 48 hours it shows as `stopped` with "ended while the background service was off" | `agent-view` §Sessions show as failed or stopped after shutdown |
| Sleep does not stop sessions; the supervisor reconnects to them on wake | `agent-view` §Sessions show as failed or stopped after shutdown |
| `CLAUDE_CONFIG_DIR` makes the supervisor use that directory instead of `~/.claude` and **run as a separate instance with its own sessions** | `agent-view` §Where state is stored |
| `~/.claude/daemon/roster.json` is the supervisor's list of running sessions, used to reconnect after a restart | `agent-view` §Where state is stored |

### 4.2 From this repository

| Fact | Where |
|---|---|
| ccresu is deployed as a supervisor-shaped service: `Restart=on-failure`, `RestartSec=10s`, `KillSignal=SIGTERM`, `TimeoutStopSec=60s` | `deployments/systemd/ccresu.service:38-43` |
| The unit's comment says the timeout exists to let an in-flight `claude -p --resume` child finish or be killed | `deployments/systemd/ccresu.service:40-41` |
| The child is killed when the context is cancelled | `internal/claude/exec.go:76` (`exec.CommandContext`) |
| An attempt with no finish time is closed as `Success: false`, moved to `RETRY_WAIT`, reason "the attempt was interrupted by a process restart" | `internal/recovery/manager.go:802-836` (`closeStaleAttempt`) |
| The only lifecycle command in the adapter is `claude stop`, and its file comment explains it exists to clear the background-session refusal | `internal/claude/stop.go:1-30`, `:104-117`; the refusal path at `internal/claude/cli.go:260-299` |
| The job-id form is verified: eight lower-case hex, read from the CLI's own refusal | `internal/claude/stop.go:44` (`backgroundJobIDPattern`), `:58` (`ValidBackgroundJobID`), `:67` (`BackgroundJobID`), `:87` (`StopArgs`) |
| `Stop` deliberately has no version probe | `internal/claude/stop.go:99-103` |
| The CLI version is probed once and cached for the process lifetime | `internal/claude/cli.go:152-172` |
| The supported-version floor is a single constant | `internal/claude/version.go:106` |
| Process identity is persisted "so a recycled PID is never mistaken for the original process" | `internal/storage/models.go:160-164` |
| Only `PID` is ever written; `PPID` and `ProcessStartTime` are never populated | `internal/app/daemon.go:202`; no other production assignment |
| `internal/process` — the package that would verify identity — has **no production caller** | `grep -rn "internal/process" --include=*.go .` → only `internal/process/adversarial_test.go` |
| `internal/process` states its own scope: "ccresu never signals or kills processes; it only needs to answer 'is this still the same process?'" | `internal/process/identity.go:1-6` |
| `sessionRunning` falls back to `PID > 0` when the CLI publishes no state or status | `internal/claude/provider.go:367-380` |
| The daemon is a 30-second reconcile loop with no supervision of its own | `internal/app/daemon.go:20-27`, `:71-110` |
| PRD 2 already removes the PID fallback from the recovery decision | `prds/authoritative-session-state.md` §6.1 |

## 5. Why it happens — the causal chain

### RC1 — The deployment encodes an ownership model the supervisor has replaced (`deployments/systemd/ccresu.service:38-43`)

```
Restart=on-failure
RestartSec=10s
KillSignal=SIGTERM
TimeoutStopSec=60s
```

Every one of those lines is a lifecycle decision. `Restart=on-failure` says
ccresu's death is an accident to be corrected; `TimeoutStopSec=60s` says a child
may legitimately still be running when the service stops. After PRD 1 the only
child is a handoff that returns in seconds, and after this PRD the session that
matters is not a child at all. The unit is not wrong so much as obsolete: it is
the artefact where the old model is written down most plainly, and it will keep
restarting ccresu into a world where the supervisor also restarts things.

### RC2 — A dead child is recorded as a failed recovery (`internal/recovery/manager.go:802-836`)

```go
reason := "the attempt was interrupted by a process restart"
if _, err := m.store.FinishAttempt(ctx, latest.ID, storage.AttemptResult{
	State:             StateRetryWait,
	Success:           false,
	Error:             reason,
	SessionIDVerified: boolPtr(false),
	TranscriptGrew:    boolPtr(false),
	FinishedAt:        m.now(),
}); err != nil {
	return false, fmt.Errorf("close interrupted attempt: %w", err)
}
```

The comment above it is honest — "close it truthfully rather than leaving a
phantom 'running' attempt" — and the close is still the wrong verdict. An attempt
whose child vanished is *unknown*, not failed: the supervisor may have restarted
the session, kept it working, or stopped it for idleness, and the docs describe
all three as normal. Because the close also writes `RETRY_WAIT`, ccresu then
re-issues the resume, so a restart of the recoverer manufactures work for the
thing it is supposed to be watching.

### RC3 — The adapter has one lifecycle verb and it is destructive (`internal/claude/stop.go:1-30`)

`stop.go` opens by explaining that `claude stop` exists for exactly one
situation: a resume refused because the session is attached to a background
agent. The file is careful, well-tested and correct — the eight-hex form is
verified before a process is started, and the id is read from the CLI's own
refusal rather than guessed. But the shape of the interface is the problem:
ccresu can stop a session and cannot ask whether one is running, what version
the supervisor is, or how to restart a session that the supervisor has stopped.
A lifecycle client with only a kill verb will use it.

### RC4 — Stored identity that nothing verifies (`internal/storage/models.go:160-164`)

```go
// PID, PPID and ProcessStartTime capture process identity so a recycled PID
// is never mistaken for the original process.
PID              int
PPID             int
ProcessStartTime uint64
```

Only `PID` is written (`daemon.go:202`). `PPID` and `ProcessStartTime` are
persisted as zero, scanned back as zero (`internal/storage/sessions.go:466`), and
exported to the local API as zero. The package that would compute them exists,
documents its purpose precisely, and has no production caller — its only import
is its own adversarial test. So the schema promises a check nobody performs, and
`sessionRunning`'s last resort (`PID > 0`) trusts a number that may belong to a
process from a previous boot.

### RC5 — The version ccresu trusts is not the version that runs the work (`internal/claude/cli.go:152-172`)

`Version` probes `claude --version` once and caches the result for the life of
the process. That is right for the CLI floor and wrong for the supervisor: after
an auto-update the supervisor can be on a different version from the binary
ccresu invokes, which is exactly the skew `claude daemon status` warns about. The
documented remedy is `claude daemon stop --any` — which stops every session the
supervisor hosts — so the one piece of information ccresu most needs comes
attached to the most destructive command in the set.

### RC6 — The supervisor's behaviours are strictly better than the copies (`internal/app/daemon.go:20-27`)

The daemon reconciles every 30 seconds and holds no roster. The supervisor holds
a roster (`roster.json`), reconnects after its own restart, survives sleep,
restarts unexpected exits, moves sessions across its own upgrades, and applies a
48-hour rule to sessions interrupted by a shutdown. ccresu's copies of those
behaviours are a retry loop and a failure row. Duplicating a supervisor is not
neutral: the copy is the one that produces the operator-facing verdict, so the
worse implementation wins the argument.

### The resulting shape

```
ccresu restart (systemd)          supervisor (unseen)
  ├─ attempt open ────────────────► session still working
  ├─ closeStaleAttempt → attempt closed as a failure (result `RETRY_WAIT`)
  ├─ RETRY_WAIT
  └─ next pass: hand off again ───► session already running
                                    (or: idle-stopped, will resume on attach)
```

The left column is a complete lifecycle implementation. The right column is the
one that is actually running the work. Nothing connects them.

## 6. Requirements

| # | Requirement |
|---|---|
| **R1** | Supervisor liveness and version are read with one `claude daemon status` per pass. Exit 1 means "no supervisor" and is a normal answer, not an error; any other non-zero exit is a distinct, named failure. |
| **R2** | The supervisor's version is recorded and compared with the invoked CLI's version and with `MinimumVersion`. A mismatch is a named condition carrying the documented remedy, never a generic failure and never a silent success. |
| **R3** | `ccresu` never runs `claude daemon stop` on its own initiative. If an operator asks it to, it passes `--keep-workers` unless the operator explicitly asked to stop the hosted sessions too. |
| **R4** | `ccresu` never runs `claude respawn`. The supervisor already restarts unexpected exits and migrates sessions across its own updates; and on a session whose transcript was cleaned up, `respawn` re-runs the original prompt, which a recovery must never do. ccresu reports the command; the operator runs it. |
| **R5** | `claude stop <job-id>` stays ccresu's only lifecycle command, and only for user termination. `StopArgs`, `ValidBackgroundJobID` and `BackgroundJobID` are unchanged, and PRD 1's R10 still keeps the stop path off a session ccresu backgrounded itself. |
| **R6** | A child that disappears is **not** a failed recovery. An unfinished attempt is closed as *interrupted* — a distinct outcome, not `Success: false` — it spends no attempt budget, and its session is re-observed (PRD 2) rather than immediately re-handed-off. The outcome is representable without a schema change: `Success` is already a nullable pointer that is nil while unresolved (`models.go:224-225`). |
| **R7** | An idle stop is expected, and it is not a verdict about the session's work. The published state cannot distinguish an idle stop from a kill or from a session that ended while the supervisor was off, so the mapping stays PRD 2's — `stopped` is never success, and is a retryable outcome unless ccresu issued the stop — and this PRD changes only what follows it: the retry re-observes before handing off, an idle-stopped session is continued from its saved conversation, and nothing about a stop is ever reported as `permanent_failure` or resolved with `respawn` (R4). |
| **R8** | No lifecycle decision depends on a PID. `sessionRunning`'s `PID > 0` last resort goes, and the stored `PPID`/`ProcessStartTime` are either populated from a source that exists or dropped — a schema that promises a check nothing performs is worse than no field. `internal/process` gains no kill method and no lifecycle caller. |
| **R9** | One Claude Code config directory is resolved once and used for both the state read and the handoff, so ccresu cannot read one supervisor's sessions and hand off to another (`CLAUDE_CONFIG_DIR` makes them separate instances). |
| **R10** | Evidence comes from the documented commands: `claude logs <id>` for a session's recent output and `claude daemon logs` for the supervisor's, read on demand for `ccresu inspect`, not polled. |
| **R11** | Every supervisor command is bounded by the existing query timeout, built by an exported argv function in the `ResumeArgs`/`StopArgs` style so tests can assert the exact vector, and recorded with the argv actually used. |
| **R12** | The systemd unit stops being sized around an owned child: `TimeoutStopSec` no longer needs to accommodate a `claude -p --resume` child, and its comment stops describing an ownership model the set removes. |

### 6.1 What ccresu delegates, and what it keeps

| Lifecycle action | Today | After this PRD | Why |
|---|---|---|---|
| Deciding whether a session is alive | `sessionRunning`, ending in `PID > 0` | delegated to the published state (PRD 2) and `daemon status` for the supervisor | the supervisor knows; a PID from a previous boot does not |
| Restarting a session that exited unexpectedly | nothing — the supervisor's job, done invisibly | delegated, and explicitly not duplicated | the docs say the supervisor restarts it; a second restarter turns a normal event into a failure |
| Moving sessions onto a new version after an auto-update | nothing | delegated to the supervisor's self-restart; `respawn --all` remains the operator's bulk action | the supervisor does this without interrupting working, blocked or attached sessions |
| Stopping a session for idleness after about an hour | invisible to ccresu | delegated, and read as PRD 2's retryable outcome rather than as a crash (R7) | the conversation stays on disk and the next attach or handoff resumes it |
| Terminating a session on the operator's instruction | `claude stop <job-id>` | **kept**, same argv, same eight-hex validation | the operator's intent is ccresu's to record even though the supervisor performs the stop |
| Stopping the supervisor | nothing | **never automatic**; the documented remedy is reported, with `--keep-workers` if ever run on instruction | `daemon stop --any` stops every session it hosts |
| Restarting a specific session | nothing | **never**; the command is reported | `respawn` on a session whose transcript was cleaned up re-runs the original prompt |
| Reading a session's recent output | nothing | delegated to `claude logs <id>` on demand | the supervisor holds the output; ccresu should not tail files |
| Removing a session | nothing | **kept out of recovery entirely** | `rm` deletes worktrees and is not a recovery action |
| Deciding *when* to resume | transcript detection, limit windows, one resume per window | **kept** | the supervisor has no concept of a usage-limit window |
| Retry policy | attempt budget, backoff, `RETRY_WAIT` | **kept** | same reason; but a restart no longer spends the budget (R6) |
| Process identity verification | `internal/process`, unused; `PPID`/`ProcessStartTime` stored as zero | **kept read-only and unwired**, or removed with the fields | the published read path carries no start time, so identity is the supervisor's business |
| Killing a child on timeout | `exec.CommandContext` on the resume | **kept for the handoff only**, which is bounded in seconds | after PRD 1 the only child is the handoff command, not a session |

### 6.2 Failure modes

| Failure | What ccresu observes | What it must do |
|---|---|---|
| **Supervisor absent** | `daemon status` exits 1 | record "no supervisor" as normal, not as a recovery failure. No special recovery is needed: the next `--bg` handoff starts a supervisor, which is the documented behaviour of both `--bg` and `claude agents` |
| **Supervisor version-stale** | `daemon status` reports a version different from the invoked CLI's | record both versions, raise the named mismatch condition with the documented remedy, and run nothing. The remedy stops the sessions it hosts, so it is the operator's decision, not a retry step |
| **Supervisor stopped mid-recovery, session absent** | the handoff's session is not in the `agents --json` listing at all | `HandoffUnobserved` (PRD 2 R8) with a scheduled retry. Do not treat the ~1-minute window before sessions stop as a recovery failure, and do not try to keep the supervisor alive |
| **Supervisor stopped mid-recovery, session reports `stopped`** | `state: stopped` in the listing | PRD 2 R7's retryable outcome: never success, never `permanent_failure`, and no `respawn` (R4) — the handoff continues the saved conversation |
| **Supervisor unresponsive** | `daemon status` or `claude logs` reports that the background service did not respond, or times out | report it with the documented remedy (`daemon stop --any --keep-workers`, then the next `--bg` or `claude agents` starts a fresh one). Do not run it |
| **Two supervisors (different `CLAUDE_CONFIG_DIR`)** | a listing that does not contain a session ccresu just handed off | resolve the config directory once and use it for both the read and the handoff (R9); never fall back to `~/.claude` when one was configured |
| **`respawn` would re-run the original prompt** | the session's transcript is gone | never respawn (R4); report that the saved conversation is no longer on disk, which is a fact about the session, not a lifecycle problem |

### Acceptance criteria

- `TestAProcessExitIsNotAFailedRecovery` leaves an attempt unfinished, simulates a
  restart, and asserts the attempt is closed as interrupted — not `Success:
  false` — and that the attempt budget is unchanged.
- `TestDaemonStatusExitOneMeansNoSupervisor` asserts exit 1 yields the "no
  supervisor" outcome rather than an error, while exit 2 yields a distinct named
  failure.
- `TestAVersionMismatchIsNamedAndRunsNothing` asserts the mismatch outcome carries
  both versions and the documented remedy, and that no `daemon stop` command was
  executed.
- `TestCcresuNeverRespawns` asserts no command constructor in `internal/claude`
  emits `respawn`, and that the lifecycle command set is exactly `stop` plus the
  read-only `daemon status`/`logs`.
- `TestStopStillTakesTheEightHexJobID` keeps the existing contract green: a UUID
  is rejected before a process starts and an eight-hex id is accepted.
- `TestOneConfigDirIsUsedForReadAndHandoff` asserts the state read and the handoff
  resolve the same config directory when `CLAUDE_CONFIG_DIR` is set.
- `TestSupervisorStoppedMidHandoffIsRetryable` asserts a handoff whose session
  vanishes is `HandoffUnobserved` with a scheduled retry, not a failure
  milestone.

## 7. Non-goals

- **Becoming the supervisor.** No roster, no socket, no daemon of our own. This
  PRD removes an imitation; it does not improve it.
- **The handoff.** PRD 1 owns the invocation and its timeout.
- **The state mapping.** PRD 2 owns it; this PRD consumes it.
- **Reading `roster.json` or `daemon.log` directly.** `daemon status` and
  `daemon logs` are the documented reads. The roster file is an implementation
  detail the docs do not promise.
- **Applying the version remedy automatically.** `daemon stop --any` stops hosted
  sessions; running it because a version differs would turn a warning into an
  outage.
- **Respawn, `rm`, `--exec`.** Not recovery actions.
- **Worktrees.** PRD 6.
- **Notifications.** PRD 4.
- **Spend guardrails.** PRD 5.

## 8. Proposed shape

| Step | Change | Satisfies |
|---|---|---|
| 1 | Add `DaemonStatusArgs`, `LogsArgs` and a tolerant parser for `daemon status` in `internal/claude`, in the `StopArgs` style | R1, R2, R11 |
| 2 | Record the supervisor's state, version and worker count on each pass; raise the named mismatch condition | R1, R2 |
| 3 | Close an unfinished attempt as *interrupted* instead of failed, and stop spending budget on it | R6 |
| 4 | Remove the `PID > 0` last resort; populate or drop `PPID`/`ProcessStartTime` | R8 |
| 5 | Resolve the Claude Code config directory once and thread it through both the read and the handoff | R9 |
| 6 | Add the documented commands to `ccresu inspect`: `claude logs <id>` for a session, `claude daemon logs` for the supervisor | R10 |
| 7 | Rewrite the systemd unit's timeout and comment for a world where the session is not a child | R12 |

Step 3 is the one that changes an operator's history: an interrupted attempt stops
being a failure and becomes a fact about the recoverer, not about the session.
That is the same correction the previous PRD made for a refusal — a true fact
recorded in the wrong layer — and it is worth stating in the same terms, because
the failure it removes is a failure the operator was told about and could do
nothing with.

### Migration

- **First run: there may be no supervisor at all.** A supervisor exists only after
  the first `claude --bg` or `claude agents`, so on many installations `claude
  daemon status` exits 1 on the first pass after the change. R1 makes that a normal
  answer, and §6.2's first row says what ccresu does with it: record "no
  supervisor" and carry on. The first handoff of PRD 1 starts one, which is the
  documented behaviour of `--bg`.
- **First run: a version mismatch is expected, once.** `ccresu` and the supervisor
  are updated independently, so the first pass after an upgrade can legitimately
  see two versions. R2 records both and raises the named condition with the
  documented remedy, and runs nothing. That remedy (`daemon stop --any`) stops the
  sessions the supervisor hosts, so it stays the operator's decision — §7's
  non-goal, not a retry step.
- **First run: history is not rewritten.** Attempts `v0.6.0` recorded as failed
  because a child exited stay failed. R6 changes what happens next — the same
  event becomes *interrupted*, spends no attempt budget, and leaves the session to
  be re-observed — and it does not reclassify rows already written. An operator
  reading `ccresu history` sees the old verdicts and the new ones side by side,
  which is the honest presentation of a change of policy.
- **A session mid-attempt when the change lands.** Unaffected. R6 applies when the
  session itself is observed to have ended, not when `ccresu` restarts: under PRD 1
  the attempt's work belongs to the supervisor, and PRD 1's R13 keeps an attempt
  with a recorded handoff open across a restart. Only the reporting changes, on the
  next pass that sees the session.
- **A session parked in `WAITING_FOR_RESET`.** Unaffected; the scheduler owns it,
  and nothing here changes when or whether an attempt starts.
- **A `claude` without the `daemon` subcommand.** It exits non-zero, which R1 reads
  as "no supervisor reported" — the same answer as an absent one — and the handoff
  does not depend on it. PRD 1's floor (`2.1.257`) is newer than every `daemon`
  command used here, so this is a guard rather than a supported configuration, and
  no capability table is needed for it.

## 9. Open questions

1. **Is `daemon status` output machine-readable?** The docs promise "state,
   version, socket directory, and worker count" but not a format. If it is prose,
   ccresu must parse it tolerantly — the way the version probe already does — or
   treat it as display-only and never branch on it. Settle it by running the
   command against a live supervisor before implementing.
2. **What does `daemon status` exit with when `CLAUDE_CONFIG_DIR` names a
   directory that has never hosted a supervisor?** Exit 1 is documented for "the
   supervisor isn't running"; the config-directory interaction is not.
3. **Does `claude stop` succeed on a session the supervisor has already
   idle-stopped?** `stop` is ccresu's only lifecycle command, and PRD 1's R10
   relies on it being harmless when there is nothing to stop.
4. **Populate `PPID`/`ProcessStartTime` from procfs, or drop the fields?**
   Populating them buys a Linux-only verification for a fact no decision uses;
   dropping them makes the schema honest. The recommendation is to drop them with
   the `PID` fallback, but that is a schema change and should be a deliberate one.
