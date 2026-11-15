# PRD: a blocked session must read as blocked, not as recovered

| | |
|---|---|
| **Status** | `Proposed` — design only. Nothing here is implemented |
| **Affected version** | `v0.6.0` (`55bf09e`) |
| **Component** | `internal/claude` → `internal/recovery` → `internal/app` (daemon) |
| **Sources** | <https://code.claude.com/docs/en/agent-view>, <https://code.claude.com/docs/en/cli-reference> |
| **Owner** | ccresu maintainers |

## 1. Summary

`ccresu` already asks the CLI what a session is doing and already receives the
answer. `SessionStatus` carries `State`, `Status` and `WaitingFor`
(`internal/claude/claude.go:275-289`), copied straight from the discovery record
(`internal/claude/provider.go:345-347`). Then the recovery manager reduces all
three to one boolean — `status.Running` — and decides success from the
transcript's line count (`internal/recovery/manager.go:423`).

That reduction is where the defect lives, and it is why this PRD is not a field
substitution. The authoritative vocabulary is in the struct; it is discarded one
layer above the struct. `sessionRunning` folds `blocked` into "running"
(`internal/claude/provider.go:367-380`), which is the right answer to "does this
session have a live process" and the wrong answer to "did the recovery work".
And `Running` is explicitly excluded from proof — "a launched process is not a
resumed conversation" (`internal/recovery/verify.go:84-87`) — so the only
evidence left is whether a file got longer, which is the shortcut the previous
PRD's RC5 recorded reading a failed attempt's own write as its success.

The proposal is to let the published state make the decision. `claude agents
--json --all` reports `working`, `blocked`, `done`, `failed` or `stopped` per
background session, plus `status` and `waitingFor`; `blocked` "always means the
session needs something from you before it can continue". `ccresu` gains a
persisted state for that — `WAITING_FOR_INPUT` — so a session waiting on a human
is never reported as recovered, and transcript growth is demoted to a diagnostic.

This PRD depends on PRD 1. A session has a published `state` only once it is a
background session, and PRD 1 is what makes a recovered session one. PRD 1's R10
was written so this PRD can be dropped in without touching the handoff; this PRD
replaces the verification PRD 1 removes and leaves the handoff alone.

## 2. What an operator sees today

A design gap with reproducible inputs rather than a filed incident, so the
observations below are stated as the inputs that produce them.

| Observation | Why it happens |
|---|---|
| A recovered session waiting on a permission prompt reads `RESUMED` | `blocked` counts as running, and the blocked session's own transcript write counts as growth |
| A session that had already finished its last turn reads `RESUMED` with no resume issued | `grew()` compares against a baseline older than the finished turn's own write |
| `ccresu inspect` shows "the transcript grew" as the proof of a recovery | that is the only proof the verifier can produce (`verify.go:172`) |
| Nothing in `ccresu` ever says "this session needs you" | there is no state, no milestone and no notification for a session a human must unblock |
| A session the supervisor no longer lists looks exactly like one it does | `Discover` merges the listing with the on-disk store and the merge does not record which half a session came from (`provider.go:161`, `:192`, `:236-238`) |

The last row is the one that makes the naive fix dangerous: swapping the
transcript read for a state read, without knowing whether the state was
published at all, turns "the supervisor has nothing to say" into "the session is
fine".

## 3. Impact

- **The one state that needs a human is the one ccresu cannot express.** A
  session blocked on a permission prompt, a sandbox request or a question is
  indistinguishable from a session that is working — and, worse, is reported as
  a success.
- **A false success is terminal.** `RESUMED` is settled
  (`internal/recovery/state.go:217-224`), so the daemon never looks at that
  session again. The operator's only signal is the absence of a signal.
- **The evidence chain is a file the attempt itself writes to.** The previous
  PRD spent a release undoing exactly this, and this PRD is where the remaining
  instance of it lives: `grew()` at `manager.go:1009` is the same shortcut as
  the one RC5 of `usage-limit-refusal-misreported.md` removed from the
  "already resumed" path.
- **Spend without progress.** A blocked session that is read as resumed is not
  resumed again, so its work stops; a blocked session read as failed is resumed
  again, and the resume queues behind a prompt nobody will answer. Both waste
  the window the operator waited for.
- **PRDs 3, 4 and 5 have nothing to stand on.** Lifecycle delegation, hook
  eventing and spend guardrails all need a state vocabulary that distinguishes
  "working" from "needs you". This PRD supplies it.

## 4. Evidence

### 4.1 From the two documentation pages

| Fact | Source |
|---|---|
| `claude agents --json` "is the supported way to read session state from outside Claude Code" — not the files under `~/.claude/jobs/` | `agent-view` §Read session state from a script |
| `claude agents --json` prints a JSON array and exits: every live session, plus background sessions that are still working or blocked **even when their process has exited** | `agent-view` §List sessions as JSON |
| `--all` also includes completed background sessions; `--cwd <path>` limits the list to sessions started under a directory | `agent-view` §List sessions as JSON |
| `state` is present for **background sessions** and is one of `working`, `blocked`, `done`, `failed`, `stopped` | `agent-view` §List sessions as JSON |
| `pid` and `status` are present **while the process is alive**; `status` is one of `busy`, `waiting`, `idle` | `agent-view` §List sessions as JSON |
| `waitingFor` is present **when `status` is `waiting`**: `permission prompt`, `input needed`, `sandbox request`, `worker request`, or `dialog open` | `agent-view` §List sessions as JSON |
| `working`: a turn is running, or the session is between steps of work it drives on its own, such as a `/loop` iteration or a wait on CI | `agent-view` §Read session state from a script |
| `blocked`: waiting on you — a question it asked, a permission or sandbox decision, an error only you can clear such as an expired login, or its first prompt if it was started without one | `agent-view` §Read session state from a script |
| `done`: the last turn finished what you asked for and the session is ready for your next prompt, **whether or not its process is still alive** | `agent-view` §Read session state from a script |
| `failed`, `stopped`: the task ended with an error, or the session was stopped | `agent-view` §Read session state from a script |
| "A session that finished its turn and is waiting for your next instruction reads `done`, not `blocked`. `blocked` always means the session needs something from you before it can continue." | `agent-view` §Read session state from a script |
| `stopped` covers `Ctrl+X` or `claude stop`, a process ended from outside Claude Code, **and** a session that ended while the background service was off | `agent-view` §Read session state |
| The files under `~/.claude/jobs/<id>/` are **not a stable interface**; values written to `state`, `detail`, `tempo` or `needs` are replaced on the next update | `agent-view` §Read session state from a script |
| Session state persists on disk through auto-updates and supervisor restarts, and sessions are preserved when the machine sleeps | `agent-view` §Read session state |
| `claude agents --json` accepts `--all` and `--cwd`; the `--json` form is documented as the read path for a scheduler or another program, while opening agent view requires an interactive terminal | `cli-reference` `claude agents`; `agent-view` §Read session state from a script |

### 4.2 From this repository

| Fact | Where |
|---|---|
| The authoritative fields are already carried on the status value | `internal/claude/claude.go:275-289` (`SessionStatus.State`, `.Status`, `.WaitingFor`) |
| They are populated from discovery, not synthesised | `internal/claude/provider.go:345-347` |
| They are parsed tolerantly from the listing, unknown values passed through lower-cased | `internal/claude/agents.go:32-56` (`AgentEntry`), `:143-162` (`mapAgentEntry`), `:155-157` |
| The listing already has a typed query builder that emits `--all`/`--cwd` | `internal/claude/cli.go:181-199` (`AgentsQuery`), `:201-220` (`Agents`) |
| `blocked` is mapped to "running" | `internal/claude/provider.go:367-380` (`sessionRunning`) |
| The success decision is `status.Running && grew(...)` | `internal/recovery/manager.go:423` |
| `grew` is a line-count comparison | `internal/recovery/manager.go:1009` |
| Proof is `SessionIDPreserved && TranscriptGrew` | `internal/recovery/verify.go:172`, `:213-214` |
| `Running` is recorded and explicitly not proof | `internal/recovery/verify.go:84-87` |
| The success milestone is defined in terms of the transcript growing | `internal/recovery/observe.go:19-22` |
| The state machine has no state meaning "needs a human" | `internal/recovery/state.go:130-138` |
| `RESUMED` is settled, so no later pass revisits it | `internal/recovery/state.go:217-224` |
| States are text-valued, so a new one needs no migration | `internal/storage/models.go:8-10`, `:32` (`allStates`) |
| Discovery merges the listing with the on-disk store, and the merge does not record which half a session came from | `internal/claude/provider.go:161` (the merge call), `:192` (`merge`), `:236-238` |
| A failed listing degrades to the store and logs, so an empty `State` can mean "the query failed" | `internal/claude/provider.go:148-151` |
| The per-session status read costs one listing per session | `internal/claude/provider.go:338` → `:321` (`GetSession`) → `:128` (`Discover`) |
| `--all` is a user preference, defaulted on but configurable off | `internal/config/defaults.go:20`, `internal/config/config.go:43`, `internal/app/runtime.go:132` |

## 5. Why it happens — the causal chain

### RC1 — The state is read and then reduced to one bit (`internal/claude/claude.go:275-289`)

```go
type SessionStatus struct {
	Session Session
	Running bool
	Status  string
	State   string
	WaitingFor string
	...
}
```

`provider.go:345-347` fills all three strings. The manager then consults
`status.Running` and `status.Transcript.Lines` and nothing else. This is not a
visibility problem — the answer arrives and is dropped one layer above where it
was parsed. That is why the fix is a decision change rather than a new
integration.

### RC2 — `blocked` is folded into "running" (`internal/claude/provider.go:367-380`)

```go
func sessionRunning(session Session) bool {
	switch strings.ToLower(session.State) {
	case "done", "failed", "stopped":
		return false
	case "working", "blocked":
		return true
	}
	...
}
```

The function answers a question worth asking — does this session still have a
process — and the answer is correct. It is then used to answer a different
question, "did the recovery work", for which `working` and `blocked` are not the
same thing at all: one is progress, the other is a stop that only a human can
clear.

### RC3 — Growth is the proof, and the one authoritative signal is excluded (`internal/recovery/verify.go:172`)

```go
verification.Proven = verification.SessionIDPreserved && verification.TranscriptGrew
```

`Verification.Running` carries the live-process fact and its own comment forbids
using it as proof (`verify.go:84-87`). The prohibition is right. The consequence
is that the only admissible evidence is a line count, and the precedent PRD's RC5
is the recorded incident of that evidence being produced by the attempt's own
failure. `observe.go:19-22` goes further and writes the heuristic into the
vocabulary: `recovery_success` *means* "the transcript grew". A definition that
lives in the milestone name is a definition no later layer can correct.

### RC4 — The state machine has no word for "needs a human" (`internal/recovery/state.go:130-138`)

`RESUMING` can go to `RESUMED`, `RETRY_WAIT`, `WAITING_FOR_RESET`, `DISCOVERED`,
`UNRECOVERABLE`, `USER_TERMINATED` or `DISABLED`. There is no `WAITING_FOR_INPUT`
and no edge to one. So a manager that correctly observed `blocked` would still
have nowhere legal to put the session, and the two available destinations are
both lies: `RESUMED` says the work continues, `RETRY_WAIT` says ccresu will try
again, and neither is true while a permission prompt is on screen. This is the
same shape as RC3 of the previous PRD — a correct observation with no state to
land in.

### RC5 — `RESUMED` is settled, so the wrong answer is final (`internal/recovery/state.go:217-224`)

```go
case StateResumed, StateCompleted, StateUserTerminated, StateDisabled, StateUnrecoverable:
	return true
```

`Settled` is what stops a completed conversation from being resumed again, and
it is right to be conservative. It also means a session misclassified as
`RESUMED` while blocked is never re-observed: the daemon will not check it, no
milestone will fire for it, and the operator's queue is empty. The defect is
silent by construction, which is why it survived a release that removed its twin.

### RC6 — The merge erases whether anything was published (`internal/claude/provider.go:161`, `:192`)

`Discover` joins the CLI listing with the on-disk store and returns one list.
A session that the supervisor has never heard of and a session the supervisor
just reported `working` are the same shape afterwards: `State` is `""` for the
former and set for the latter, but nothing records *which* it is, and a listing
that failed outright also leaves `State` empty (`provider.go:148-151`). Empty
therefore means three different things — interactive, unknown to the supervisor,
and query failed — and a state read that treats them as one would convert an
outage into a verdict. Separately, `GetSessionStatus` reaches the listing through
`GetSession` → `Discover`, so a pass over N sessions issues N listings; the
supported read is a list, and using it as a per-session lookup is a design
change, not a field swap.

### The resulting shape

```
pass N:  RESET_TIME_REACHED → CHECKING_SESSION
         status := GetSessionStatus(...)        // State="blocked" arrives here
         if status.Running && grew(...)         // blocked ⇒ Running; any write ⇒ grew
             → RESUMED "session had already resumed after the limit reset"
         Settled(RESUMED) = true
         the session is never observed again
```

Nothing in that sequence reads the one field that answers the question. The
third line is the whole bug; the fourth is why nobody finds out.

## 6. Requirements

| # | Requirement |
|---|---|
| **R1** | The recovery state read is one `claude agents --json --all` query per pass, machine-wide (no `--cwd`), reusing `claude.AgentsQuery`, `claude.Agents` and `claude.ParseAgentsJSON`. No second parser is written and no listing is parsed by hand. The observation is keyed by `sessionId`; `id` is the state ID and is never used to identify a conversation. |
| **R2** | No file under `~/.claude/jobs/` is read for state, at any point, for any reason. The docs call those files unstable; the supported read is the JSON listing. |
| **R3** | The published `state` decides the outcome. Transcript growth is demoted to a diagnostic: it is recorded, never sufficient, and `Verification.Proven` must not be computable from it alone. |
| **R4** | A `blocked` observation maps to `WAITING_FOR_INPUT`. No code path may report `RESUMED` from a `blocked` observation, and the mapping is a total function over every documented value plus "absent" and "unknown", so there is no fall-through case that could produce success. |
| **R5** | The mapping takes "is a handoff outstanding for this attempt" as an explicit argument. The same `state` means different things before and after a handoff, and the function is not allowed to infer which case it is in. |
| **R6** | `done` with no handoff outstanding is `COMPLETED`, not `RESUMED`: a conversation that has finished its last turn must not be handed a "continue" message. `done` with a handoff outstanding is `RESUMED`. |
| **R7** | `stopped` is never success. It is `USER_TERMINATED` only when ccresu itself issued the stop; otherwise it is a retryable failure, because the docs include "ended while the background service was off" in that state. |
| **R8** | Absence is not an observation. A session missing from the listing is `HandoffUnobserved` when a handoff is outstanding, and "not observed" otherwise — never `failed` and never `stopped`. The read must record whether a session was in the listing at all; the merged `Session` does not carry that today (`provider.go:236-238`), so the observation type must. |
| **R9** | The recovery read always passes `--all`, regardless of `discover_all_sessions`. That setting is a discovery and UI preference (`defaults.go:20`); with it off, a finished background session is not listed and absence would become a false negative. |
| **R10** | An unrecognised `state`, `status` or `waitingFor` value is recorded verbatim and treated as unknown: the attempt stays open and is retried. It is never coerced into a known value and never read as success. |
| **R11** | `WAITING_FOR_INPUT` is a new persisted state, text-valued like the other fourteen (`models.go:8-10`), with edges `CHECKING_SESSION → WAITING_FOR_INPUT`, `RESUMING → WAITING_FOR_INPUT`, and `WAITING_FOR_INPUT → {RUNNING, RESUMED, RETRY_WAIT, USER_TERMINATED, DISABLED, UNRECOVERABLE, DISCOVERED, WAITING_FOR_INPUT}`. It is `Pending` (a restarted daemon re-observes it) and not `Settled` (it is not finished), but a session in it is **observed, never handed off**. |
| **R12** | A `blocked` observation announces exactly one distinct `MilestoneNeedsInput`, and it never announces `MilestoneSucceeded`. The milestone is raised whether or not the observation carried a `waitingFor`: when the field is present its value is carried verbatim, and when it is absent (the live payload recorded in §6.1 shows entries without it) the payload records that no reason was reported, rather than suppressing the milestone or inventing a reason. `waitingFor` selects the payload, not whether the operator is told. The mapping from milestone to `storage.NotificationEvent` and transport is PRD 4's, which names that event `needs_input` — the milestone kind and its payload are defined here, the event value, subject, body and dedupe key are defined there. |
| **R13** | The attempt row records the observation: raw `state`, `status`, `waitingFor`, the instant, and whether the session was in the listing. `ccresu inspect` must be able to say what was seen rather than what was inferred from a file size. |
| **R14** | The verification timeout becomes a *watch* bound, not a proof deadline. A session still `working` when it expires is left running — PRD 1's handoff owns it — and the attempt stays open, instead of being failed for not having finished in time. |

### 6.1 The mapping

The first table is the decision. Every documented `state` value appears, plus the
two cases that are not values: a session with no `state` because it is not a
background session, and a session that is not in the listing at all.

| Observed `state` | No handoff outstanding | Handoff outstanding | Why |
|---|---|---|---|
| `working` | `RESUMED` — it continued on its own once the window reset | no transition; the attempt stays open | a turn is running, so the conversation is alive without ccresu |
| `blocked` | `WAITING_FOR_INPUT` | `WAITING_FOR_INPUT` | "`blocked` always means the session needs something from you before it can continue" |
| `done` | `COMPLETED` — nothing to continue | `RESUMED` | "the last turn finished what you asked for and the session is ready for your next prompt" |
| `failed` | `RETRY_WAIT` — retryable failure | `RETRY_WAIT` — retryable failure | "the task ended with an error" |
| `stopped` | `RETRY_WAIT` — retryable failure | `RETRY_WAIT`, or `USER_TERMINATED` when ccresu issued the stop | the docs include "ended while the background service was off" in `stopped`, so a stop is not necessarily the operator's decision |
| absent, session **in** the listing | not observed → `RETRY_WAIT` | `HandoffUnobserved` → `RETRY_WAIT` | `state` is documented for background sessions only |
| absent, session **not** in the listing | not observed; expected before a handoff | `HandoffUnobserved` → `RETRY_WAIT` | absence is not evidence (R8) |
| any other value | `RETRY_WAIT`, value recorded | `RETRY_WAIT`, value recorded | a newer CLI's vocabulary is not ccresu's to guess (R10) |

`status` is corroboration only. It is present only while the process is alive, so
it cannot be the decision — a `done` session whose process exited has no
`status` at all, and treating that absence as a state would undo the one thing
`done` is documented to survive.

| Observed `status` | Effect on the decision | Why |
|---|---|---|
| `busy` | none; recorded | it records activity, not health. It is documented alongside `working`, but a live supervisor also reports `busy` alongside `blocked`, so it is never read as agreement with the state |
| `waiting` | none; `waitingFor` is carried through | the reason is the useful part, not the word |
| `idle` | none; recorded | an alive process with nothing to do is not a finished turn — and it is reported alongside `blocked` as well as alongside `done` |
| absent | none | the process is not alive, which says nothing about the conversation; an entry may carry no `pid` either |

`waitingFor` never changes the ccresu state; every value means the same thing to
ccresu, which is that it cannot help. Each is preserved verbatim for the
operator, because the remedy differs even though the state does not.

| Observed `waitingFor` | ccresu state | Why ccresu cannot act |
|---|---|---|
| `permission prompt` | `WAITING_FOR_INPUT` | answering it is the operator's decision, and an unattended resume has nobody to ask |
| `input needed` | `WAITING_FOR_INPUT` | a question from Claude or an MCP server's input request |
| `sandbox request` | `WAITING_FOR_INPUT` | a network host or sandbox decision only a human can make |
| `worker request` | `WAITING_FOR_INPUT` | a worker or teammate asking the session for something |
| `dialog open` | `WAITING_FOR_INPUT` | an interactive dialog in an attached terminal |
| absent | no change | `waitingFor` is documented as present only when `status` is `waiting` |

**Observed on a live supervisor, not documented.** One `claude agents --json`
payload from this machine — five background sessions, read once — sharpened three
of the rows above, and it is recorded here because a real payload is evidence the
docs do not give:

- **All five sessions were `state: "blocked"`, and not one carried `waitingFor`.**
  So `blocked` arrives without a reason, and R4 must not require one: the state
  decides and the reason is optional, exactly as the *absent* row already says.
  The operator-facing message says no reason was reported rather than guessing
  `input needed`.
- **`status` is not agreement.** One entry was `blocked` with `status: "busy"` and
  three were `blocked` with `status: "idle"`, so the table above cannot describe
  `busy` as agreeing with `working`. The decision is unchanged — `status` is
  corroboration only — but the reason is now the observed one rather than the
  intuitive one.
- **`pid` is optional.** One entry carried no `pid` and no `status` at all, four
  carried both. Presence of a PID is therefore not a signal, which is the same
  conclusion PRD 3 R8 reaches from the other direction.

The payload also confirms two identifiers the set depends on: `id` is the
eight-hex short ID (`1347e5db`) and `sessionId` the conversation UUID
(`1347e5db-a9fb-42eb-b92a-82256bd5aa68`), so README §3's *State ID vs session ID*
row is describing what the CLI actually prints. And every `cwd` equalled the
launch project with no `.claude/worktrees/` path present — consistent with a
session moving into a worktree only *before it edits files*, and not a test of
whether `cwd` follows that move, which PRD 6 §9.1 still has to settle.

This also answers PRD 1's open question 3 — what a handoff to an
already-`blocked` session does. It is not issued: the attempt parks in
`WAITING_FOR_INPUT` instead, because a continuation message would queue behind a
prompt nobody is there to answer.

### Acceptance criteria

- `TestABlockedObservationIsNeverResumed` feeds
  `{"state":"blocked","status":"waiting","waitingFor":"permission prompt"}`
  through the parser and the mapping and asserts `WAITING_FOR_INPUT`, no
  `RESUMED` transition, no `MilestoneSucceeded`, and the verbatim `waitingFor`.
- `TestTheSameStateMeansDifferentThingsWithAnOpenHandoff` asserts `done` with no
  handoff yields `COMPLETED` and issues no resume command, while `done` with a
  handoff yields `RESUMED`.
- `TestTranscriptGrowthCannotProveSuccess` supplies a grown transcript together
  with `state: blocked` and asserts the attempt is not proven and not resumed.
- `TestASessionMissingFromTheListingIsHandoffUnobserved` asserts the named
  outcome and a scheduled retry — not a failure, not `RESUMED`.
- `TestTheRecoveryReadAlwaysPassesAll` asserts the recovery query's argv contains
  `--all` while `discover_all_sessions` is `false`.
- `TestAnUnknownStateValueIsRecordedAndNotGuessed` feeds `state: "paused"` and
  asserts `RETRY_WAIT` with the raw value on the attempt row.
- `TestNoJobsFileIsRead` builds a home directory whose
  `~/.claude/jobs/<id>/state.json` says `blocked` while the listing says
  `working`, and asserts the outcome follows the listing. Falsifiable without a
  live supervisor.
- `TestOneAgentsQueryServesEverySessionInAPass` recovers three sessions and
  asserts exactly one `agents --json --all` invocation.

## 7. Non-goals

- **The handoff.** PRD 1 owns the invocation, the copy-or-in-place rule and the
  working directory. This PRD replaces the verification that PRD 1 removes.
- **Lifecycle commands.** `daemon status`, `respawn`, `stop`, `logs` and `rm` are
  PRD 3. This PRD observes; it does not command.
- **The notification transport and the hook.** PRD 4. This PRD adds one
  milestone kind and stops there.
- **Where a recovery runs.** Worktree resolution is PRD 6.
- **Spend guardrails.** PRD 5.
- **Reading `~/.claude/jobs/<id>/`.** Never, under any circumstances, including
  as a fallback when the listing fails.
- **Detecting usage limits inside transcripts.** That path works and the previous
  PRD left it alone; so does this one.
- **A second parser for `claude agents --json`.** The tolerant one exists
  (`agents.go`); duplicating it is how the two copies drift.

## 8. Proposed shape

| Step | Change | Satisfies |
|---|---|---|
| 1 | Add a `StateObservation` value — raw `state`, `status`, `waitingFor`, observation instant, and whether the session was in the listing — filled from one `agents --json --all` query per pass | R1, R8, R9 |
| 2 | Add the total mapping function `Observe(observation, handoffOutstanding)` in `internal/recovery`, returning a state and an outcome | R3–R7, R10 |
| 3 | Add `WAITING_FOR_INPUT` to `storage` and its edges to the transition table | R11 |
| 4 | Replace the growth shortcut at `manager.go:423` and the post-resume verifier with the observation loop | R3, R14 |
| 5 | Retire `grew` and `Verification.TranscriptGrew` from the success decision, keeping the numbers as diagnostics | R3 |
| 6 | Record the observation on the attempt row and surface it in `ccresu inspect` | R13 |
| 7 | Announce `MilestoneNeedsInput` on a blocked observation | R12 |

The audit trail is the point of step 6. The previous PRD's lesson was that a
refusal's most valuable fact — when the limit resets — was destroyed at the first
layer that saw it. Here the equivalent fact is *which state was observed, and
whether it was observed at all*, and the failure mode is worse than losing it:
an unobserved session and a healthy one look identical. Writing the observation
down is what makes the difference visible after the fact, when the transcript has
already been rewritten by the next attempt.

### Migration

- **First run: nothing to back-fill.** `WAITING_FOR_INPUT` is text-valued like the
  other fourteen (`models.go:8-10`), so existing rows stay valid, and the
  observation columns (R13) are empty on every attempt recorded before the change.
  The first observation writes what was seen; an old attempt keeps an empty
  observation, and `ccresu inspect` must render that as *not recorded* rather than
  as *nothing was happening*, because the two are different facts (R8).
- **First run: the loop replaces the verifier.** The first pass issues one
  `claude agents --json --all` (R1) and observes the sessions it is recovering.
  Sessions that were not in flight are untouched: their state is re-derived at the
  next limit, not during the upgrade.
- **First run: `RESUMED` rows stay `RESUMED`.** This PRD corrects the rule, not the
  history. `Settled` (`internal/recovery/state.go:217-224`) is what stops a
  finished conversation from being resumed again, no command in `v0.6.0` moves a
  session out of `RESUMED`, and this PRD adds none. A session the old heuristic
  wrongly marked recovered therefore stays that way; the visible trace is R13's,
  because its attempts carry no observation at all.
- **A session mid-attempt when the change lands.** If the attempt carries a
  recorded handoff, it stays open and the first observation fills it in — R14 makes
  the timeout a watch bound, so an attempt is no longer failed for not having
  finished inside a fixed window. If it carries none, PRD 1's R13 has already
  closed it as interrupted, and this PRD observes the session on the pass that
  re-issues it.
- **A session parked in `WAITING_FOR_RESET`.** Unaffected; the scheduler owns it
  and the observation happens when the attempt would start.
- **A `claude` that publishes no `state`.** Not reachable through this PRD's own
  door: `state` is documented for background sessions, and PRD 1's floor
  (`2.1.257`) is far newer than `agents --json`, which `v0.6.0` already requires.
  If it is absent anyway, the mapping's *absent, session in the listing* row
  applies — `RETRY_WAIT` with the absence recorded (R8, R10) — which is a retry,
  never a success. That is the answer `prds/README.md` §4 asks for, and it is
  deliberately the failure direction this PRD chooses.

## 9. Open questions

1. **Where `done` ends and `failed` begins for a turn that hit a tool error the
   model recovered from.** The docs define the states by what they mean to the
   operator, not by an exit path, and do not say which one a turn that recovered
   mid-way reports. R6 makes `done` mean "finished", so the boundary matters:
   settle it by experiment against a session that recovers from a failed tool
   call.
2. **How long `--all` keeps listing a finished session.** Retention is not
   documented. R8 keeps absence from being evidence, so ccresu is safe either
   way, but a session resumed days later may simply not be listed, and the
   operator should be told that is what happened rather than seeing a retry with
   no explanation.
3. **Whether answering a prompt from `claude attach` in another terminal clears
   `blocked` immediately, or only when the next turn ends.** If it clears only at
   the end of the turn, ccresu will report `WAITING_FOR_INPUT` for a session the
   operator has already unblocked, and the delay should be documented rather than
   discovered.
4. **A fifteenth persisted state, or a reason on `WAITING_FOR_RESET`.** This PRD
   chooses the state, because states are text-valued and need no migration
   (`models.go:8-10`) and because "waiting for a reset" and "waiting for a human"
   have different remedies. The cost is one more case in every switch over
   `State`, and that cost should be accepted deliberately rather than by
   accident.
