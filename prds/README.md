# Enhancement PRDs

| | |
|---|---|
| **Scope** | Six proposed enhancements to `ccresu`, derived from two Claude Code documentation pages |
| **Status** | All six are **`Proposed`**. Nothing in this directory is implemented, accepted, or released |
| **Baseline** | `v0.6.0` (`55bf09e`) — the published version every PRD measures against |
| **Sources** | <https://code.claude.com/docs/en/agent-view> and <https://code.claude.com/docs/en/cli-reference>, both read in full |
| **Precedent** | [`usage-limit-refusal-misreported.md`](usage-limit-refusal-misreported.md), the accepted PRD that produced `v0.6.0` |
| **Owner** | ccresu maintainers |

## 1. The thesis

`ccresu` `v0.6.0` resumes a paused session with one headless invocation —
`claude -p --resume <session-id> --output-format json <message>` — and then
decides whether the recovery worked by watching the session's transcript grow.
Both halves of that design are now the weak point. The invocation makes `ccresu`
the owner of a process it did not write: when `ccresu` exits, the resume dies
with it; the work never appears in agent view; and no Claude Code tool can
inspect or restart it. The verification is a heuristic over a file that the
attempt itself writes to, which is precisely how the previous PRD's incident
came about — a refused resume's own transcript write was read as its success
(RC5 of [`usage-limit-refusal-misreported.md`](usage-limit-refusal-misreported.md)).
Claude Code now ships a background-supervisor surface that removes the need for
both: agent view and its scripted form `claude agents --json` publish an
authoritative per-session state; `claude daemon status`, `claude respawn`,
`claude stop`, `claude logs` and `claude rm` expose lifecycle as commands
rather than as signals to guess at; `--bg` hands a continuation to that
supervisor instead of to `ccresu`; `--exec` covers the non-Claude half of the
same problem; and the invocation `ccresu` already builds carries guardrail flags —
`--model`, `--effort`, `--fallback-model` and `--permission-mode` ride a handoff,
while `--max-budget-usd`, `--max-turns` and `--json-schema` are documented
print-mode-only, which is why PRD 5 replaces the first two with a bound `ccresu`
enforces itself rather than pretending the flag survives. The six PRDs below adopt
that surface in dependency order, each one shippable on its own. None of them
preserves today's print-mode path: the supervisor path replaces it outright, and
no PRD in this set carries a compatibility shim for the old one.

## 2. The six PRDs

Read in this order. Each depends only on the ones above it.

| # | PRD | Problem it settles, in one line | Depends on | Status |
|---|---|---|---|---|
| 1 | [`background-supervisor-recovery.md`](background-supervisor-recovery.md) | Recovery runs a process `ccresu` owns, so a resume dies with `ccresu` and is invisible to every Claude Code tool | — | `Proposed` |
| 2 | [`authoritative-session-state.md`](authoritative-session-state.md) | Progress is inferred from transcript growth when the CLI publishes `state`, `status` and `waitingFor` | PRD 1 | `Proposed` |
| 3 | [`delegate-lifecycle-to-supervisor.md`](delegate-lifecycle-to-supervisor.md) | `ccresu` spawns, kills and re-spawns `claude` itself, racing a supervisor that already does all three | PRDs 1, 2 | `Proposed` |
| 4 | [`hook-driven-eventing.md`](hook-driven-eventing.md) | "This session needs you" is discovered by polling transcripts, minutes after the CLI announced it | PRD 2 | `Proposed` |
| 5 | [`resume-spend-guardrails.md`](resume-spend-guardrails.md) | "One resume per limit window" bounds how often a resume happens, never what it costs | PRDs 1, 2 | `Proposed` |
| 6 | [`worktree-aware-recovery.md`](worktree-aware-recovery.md) | A session is one flat directory to `ccresu`, so a recovery that must run in a git worktree runs in the wrong tree | PRD 1 | `Proposed` |

PRD 6 is last because it is the only one about *where* a recovery happens rather
than *whether* it did, and that question only becomes answerable once PRD 1 has
settled that recoveries run under the supervisor and therefore inherit the
supervisor's worktree behaviour.

There were seven drafts. A seventh PRD, `capability-gating.md`, was **withdrawn**
rather than deferred: it would have maintained a version→capability table so an
old CLI kept today's print-mode path, and §4 rules backward compatibility out, so
a table whose only purpose is graceful degradation has nothing to degrade to. Its
slot is occupied by PRD 6, and PRDs 4, 5 and 6 each say in a sentence why the
behaviour they propose needs no such gate.

## 3. Shared terminology

These six documents use the following words in exactly one sense. Where a term
also exists in Claude Code's own vocabulary, the definition says how the two
relate.

| Term | Meaning in this set |
|---|---|
| **Recovery attempt** | One row in `recovery_attempts`: one decision to continue one session, with one start instant, one outcome and one error. A single limit window may contain several, and `v0.6.0` bounds them at one per window. |
| **Limit window** | The interval a usage-limit banner names, from the moment the limit is in force to the reset instant it states. `ccresu` persists it as a `usage_limits` row and schedules against it. It is the unit "one resume per window" counts. |
| **Transcript-growth heuristic** | The `v0.6.0` test for "did the resume work": take a baseline of the session transcript before the attempt, then treat any growth as proof the session continued. Named because it must be retired, not tuned. |
| **Background session** | A session the Claude Code supervisor hosts rather than a terminal, so it keeps running after the terminal that started it closes. Claude Code reports it as `kind: "background"`; agent view lists it as a row. A session started by running `claude` directly is *interactive* and is not one until it is backgrounded. |
| **Supervisor** | The Claude Code background service that runs background sessions. Started by the first `claude --bg` or `claude agents`; reachable through `claude daemon status`; logs to `~/.claude/daemon.log`. It is not `ccresu`'s process and `ccresu` must not pretend to be it. |
| **State ID vs session ID** | `claude agents --json` reports two identifiers that are easy to confuse. `id` names the *job* and is what `claude attach`, `claude logs` and `claude stop` take; `sessionId` is the conversation UUID and is what `claude --resume` takes. Every PRD in this set says which one it means. |
| **Blocked vs done** | Two Claude Code session states that look alike from outside and must never be conflated. `done` means the last turn finished and the session is ready for the next prompt. `blocked` means the session needs something only a human can supply. `v0.6.0` reports both as recovered; PRD 2 forbids that. |
| **Linked worktree** | A git worktree that git records as linked to a main checkout — either one Claude Code created under `.claude/worktrees/` or one created with `git worktree add` elsewhere. A directory that merely sits inside a repository is not one. |
| **Recovery cwd** | The working directory a resume is executed in. `v0.6.0` derives it from `Session.ProjectPath`, which is one flat path. When the session has moved into a linked worktree, the recovery cwd must be that worktree and not the project path; PRD 6 is about that gap. |
| **Capability gate** | A version→feature test a caller consults before using a surface an older CLI lacks. This set has **none**: the draft that would have built one was withdrawn (§2), because §4 rules out backward compatibility, so an older CLI is refused at the version floor rather than degraded around it. Where a PRD says it "needs no capability gate", it means no version test is required for that behaviour — not that one is deferred. |

## 4. Standing constraints

These hold for the whole set and for any implementation that follows it:

- **PRDs only.** This directory changes; `internal/`, `cmd/` and `web/` do not.
  The §2 status column is the one place implementation state is recorded; the PRD
  documents themselves stay specifications and are never rewritten to describe
  what shipped.
- **No new module dependencies.** `go.mod` is unchanged by this set.
- **No tag, no release, no publish.** `v0.6.0` remains the published version.
  An accepted PRD that ships later would bump the version then, not now.
- **Linear history.** `main` stays ff-mergeable, as every commit on it has been.
- **No backward compatibility.** The `-p --resume` path is superseded, not
  preserved. No PRD here owes a compatibility rule for it, and none may add a
  fallback that quietly keeps it alive: an operator who wants the old behaviour
  stays on `v0.6.0`. What each PRD must still state is the *migration* — what an
  existing installation does on its first run after the change, and what an
  operator sees when a capability their CLI lacks is the only way forward. Every
  PRD answers both in a **Migration** subsection at the end of its §8, including
  what happens to an attempt that is already in flight and to a session parked in
  `WAITING_FOR_RESET`.
- **Grounded in the two sources.** A behaviour a PRD relies on must be traceable
  to `agent-view` or `cli-reference`. Where those pages warn that a surface is
  unstable or in research preview, the PRD says so and designs around the
  supported interface instead.

## 5. The evidence base

Everything asserted about Claude Code in this set comes from the two pages named
in the header. Everything asserted about `ccresu` was read out of this
repository at `55bf09e`:

| Fact | Where |
|---|---|
| The resume invocation is `-p --resume <id> --output-format json <msg>` | `internal/claude/cli.go:353` (`ResumeArgs`) |
| Recovery verifies by comparing transcript baselines | `internal/recovery/manager.go:423` (`grew`) |
| A session's directory is one flat `ProjectPath`, with no worktree, branch or parent repository | `internal/storage/models.go:153` |
| There is no worktree awareness anywhere in the Go source | no match for `worktree` under `internal/` or `cmd/` |
| `claude agents --json` is already parsed, tolerantly | `internal/claude/provider.go`, `internal/claude/agents.go` |
| The refusal's job id is already extracted and used | `internal/claude/stop.go` |
| The version floor is a single constant, `2.0.0` | `internal/claude/version.go:106` |
| 14 persisted session states, text-valued so they need no migration | `internal/storage/models.go:14` |
| `default_model` and `default_effort` are declared, defaulted and **validated** — but never passed to the invocation | `internal/config/config.go:45`, `internal/config/validation.go:92`; no reader anywhere else |
| Seven notification events are rendered and queued; until `55bf09e` only the two limit events were ever produced | `internal/storage/models.go:126-134`, `internal/app/notifications.go`, `internal/telegram` |

The last-but-one row is the smallest and the most telling: `ccresu` already
validates an effort level it never uses. A configuration surface that promises
what the invocation does not deliver is the same class of defect as the previous
PRD's — a true fact recorded in the wrong layer — and PRD 5 exists partly to
close it.

## 6. What this set does not propose

- **Replacing the supervisor.** `ccresu` decides *when* to continue a session;
  Claude Code decides *how* to run it. No PRD here builds a second supervisor,
  and PRD 3 exists to stop `ccresu` from behaving like one by accident.
- **Reading `~/.claude/jobs/`.** The docs call those files an unstable
  interface. `claude agents --json` and `claude daemon status` are the
  supported reads and are the only ones any PRD here uses.
- **Any change to limit detection inside transcripts.** That path works and the
  previous PRD left it alone; so does this set.
- **Anything cloud.** Agent view sessions are local, and the docs say so. Every
  PRD here assumes local sessions and a local supervisor.
- **A new notifier.** PRD 4 feeds the queue that already exists.
- **A compatibility shim for print mode.** Explicitly out of scope. The old
  invocation is retired, and no PRD keeps two resume paths alive in order to
  avoid a version bump.

## 7. How to review the set

Each PRD follows the structure of
[`usage-limit-refusal-misreported.md`](usage-limit-refusal-misreported.md): a
status table, then Summary, Symptom, Impact, Evidence, a numbered causal chain,
a numbered requirements table with acceptance criteria, Non-goals, the proposed
shape of the change, and Open questions. A PRD that cannot fill the acceptance
criteria with something falsifiable is not ready to be accepted. §8 ends with the
Migration subsection §4 requires, so a reviewer can check the first-run and
old-CLI answers in a fixed place rather than looking for them in the argument.

Reviewing them out of order is fine; accepting them out of order is not, because
PRDs 2 and 3 read state that only exists once PRD 1's sessions are background
sessions, and PRD 6 changes *where* a recovery runs — a question that only has a
meaning once PRD 1 has settled that recoveries run under the supervisor.
