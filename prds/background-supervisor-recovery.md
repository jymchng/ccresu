# PRD: recovery should hand the session to the supervisor, not run it

| | |
|---|---|
| **Status** | `Proposed` — design only. Nothing here is implemented |
| **Affected version** | `v0.6.0` (`55bf09e`) |
| **Component** | `internal/claude` → `internal/recovery` → `internal/app` (daemon) |
| **Sources** | <https://code.claude.com/docs/en/agent-view>, <https://code.claude.com/docs/en/cli-reference> |
| **Owner** | ccresu maintainers |

## 1. Summary

`ccresu` resumes a paused session by spawning
`claude -p --resume <session-id> --output-format json "<message>"` and holding
the child process until it exits. Everything downstream assumes that shape: the
verification reads the child's JSON result, the transcript baseline is taken
around the child's lifetime, and the timeout that bounds the attempt is the
child's timeout.

The consequence is that `ccresu` **owns** the resumed work. If `ccresu` is
stopped, restarted, upgraded, or killed, the resume dies with it. The recovered
session never appears in agent view, so the operator cannot watch it, attach to
it, or read its output with any Claude Code tool. And when the session turns out
to be held by a background agent already, the only thing `ccresu` knows how to
do is stop that agent so print mode can take the conversation back
(`internal/claude/cli.go:260-299`) — it tears down background work in order to
run foreground work.

Claude Code now runs sessions under a supervisor that outlives the terminal, and
`--bg` is the flag that hands a session to it. The proposal is to invert the
relationship: `ccresu` decides **when** a session continues, and the supervisor
decides **how** it runs. Recovery becomes
`claude --resume <session-id> --bg "<message>"`, a command that returns in
seconds and leaves the work running somewhere `ccresu` can inspect but does not
own.

This is the foundation PRD. PRD 2 replaces the verification that this change
removes; PRD 3 replaces the lifecycle management it makes redundant.

## 2. What an operator sees today

This is a design gap rather than a filed incident, so there is no terminal
transcript to reproduce. The observable facts are these:

| Observation | Why it happens |
|---|---|
| `claude agents` shows nothing for a session `ccresu` has just recovered | the resume was a print-mode child of `ccresu`, never a background session |
| `ccresu sessions status <id>` says `RESUMED` while the work is, in practice, invisible | verification is satisfied by the child's own JSON, not by an independent observer |
| Restarting `ccresu` mid-recovery loses the attempt silently | the child is a descendant of the daemon and dies with it |
| A session already held by a background agent is **stopped** before it is resumed | `ErrSessionRunningInBackground` is answered with `claude stop`, then the identical resume |
| The resumed process runs in `ccresu`'s working directory, not the session's | no invocation ever sets a working directory (§5, RC5) |

The last row is the quietest and the worst: an operator who runs `ccresu` as a
user service from `$HOME` recovers every session from `$HOME`.

## 3. Impact

- **Recovery does not survive the recoverer.** The one moment `ccresu` matters
  most — a long wait ending in a resume — is the moment its own restart destroys
  the work it just started.
- **The recovered work is unobservable.** No row, no `claude logs`, no
  `claude attach`. The operator's only view is `ccresu`'s own summary of a
  process it no longer has.
- **Background work is destroyed to make room.** The stop-then-resume path
  (`cli.go:260-299`) kills a session that was doing something, on the
  assumption that print mode is the only way to continue a conversation.
- **Wrong directory.** Because no `Dir` is set, a resume inherits the daemon's
  cwd. Sessions that rely on their own directory — a relative path, a build
  tool, a `.claude/settings.json` — resume into the wrong context.
- **A dead end for every later PRD.** State reading, lifecycle delegation,
  hook-driven events and spend guardrails all assume a session that something
  else is running. Until this PRD lands, there is nothing to read.

## 4. Evidence

### 4.1 From the two documentation pages

| Fact | Source |
|---|---|
| `--bg` starts the session as a background agent and returns immediately, printing the session ID and management commands | `cli-reference`, `--bg` |
| `--bg` **cannot be combined with `-p`/`--print`**; the combination is rejected before any session is created, "because `--print` never starts the interactive session that `claude agents` attaches to" | `agent-view` §Dispatch an agent from your shell; `cli-reference` `--bg` |
| The rejection was added in `v2.1.198`; before it, `--bg` with `-p` was not rejected | `agent-view` §Version history |
| To continue an existing conversation in the background, pass its **full session ID** with `--resume` | `agent-view` §Dispatch an agent from your shell |
| On `v2.1.257`+, `--resume <id> --bg` either continues that session under its own ID, or starts a copy under a new ID **and prints a `note:` line explaining why** | `agent-view` §Dispatch an agent from your shell |
| `--continue`, a bare `--resume`, and `--resume` with a **name or file path** always start a copy | `agent-view` §Dispatch an agent from your shell |
| `--fork-session` starts a copy on purpose, without the `note:` line | `cli-reference` `--fork-session` |
| `--bg` prints a `backgrounded · <short-id> · <name>` block naming `claude agents`, `claude attach <id>`, `claude logs <id>`, `claude stop <id>` | `agent-view` §Dispatch an agent from your shell |
| When no supervisor is running, `--bg` may print `Starting background service…` **above** that block | `agent-view` §Dispatch an agent from your shell |
| `--name` sets the display name in agent view instead of the auto-generated one, and the name appears after the short ID | `agent-view` §Dispatch an agent from your shell |
| `--bg` checks workspace trust for the directory first; where no dialog can appear, such as in a script, it exits with `Workspace not trusted` | `cli-reference` `--bg`; `agent-view` §Version history (`v2.1.281`) |
| `--bg` is read as the flag only before any bare `--` separator and outside another flag's value; put the flag first, as in `claude --bg "<prompt>"` | `agent-view` §Version history (`v2.1.299`) |
| A background session starts in your working directory and is moved into an isolated worktree under `.claude/worktrees/` before it edits files | `agent-view` §How file edits are isolated |
| The short ID is the session's directory name under `~/.claude/jobs/`, and those files are **not a stable interface** | `agent-view` §Manage sessions from the shell, §Read session state from a script |

### 4.2 From this repository

| Fact | Where |
|---|---|
| The resume argv is fixed at `-p --resume <id> --output-format json <msg>` | `internal/claude/cli.go:353` (`ResumeArgs`) |
| Success is decided from the child's own JSON, by comparing the reported session ID | `internal/claude/cli.go:308-351` (`ResumeResult.Verified`) |
| A background-held session is stopped and the identical resume repeated | `internal/claude/cli.go:260-299`; `ErrSessionRunningInBackground` at `internal/claude/claude.go:107` |
| The stop needs the CLI's eight-hex job name, read out of the refusal text | `internal/claude/stop.go:67` (`BackgroundJobID`) |
| **No working directory is ever set on a child**: the only `RunOptions` literal sets `MaxOutputBytes` and nothing else | `internal/claude/cli.go:370`; `RunOptions.Dir` at `internal/claude/exec.go:30` |
| `Session.ProjectPath` is display-only — printed in three places, never used to place a process | `internal/app/commands.go:314`, `:486`; `internal/app/tui.go:349`, `:798`; `internal/telegram/template.go:93` |

## 5. Why it happens — the causal chain

### RC1 — The invocation shape is print mode, and print mode cannot become background (`internal/claude/cli.go:353`)

```go
func ResumeArgs(sessionID, message string) []string {
	return []string{"-p", "--resume", sessionID, "--output-format", "json", message}
}
```

The obvious reading of this PRD is "add `--bg`". That does not work. `--bg`
cannot be combined with `-p`, and the rejection happens *before any session is
created*, so there is no session to inspect and no partial success to salvage.
The `-p` flag is not decoration either: it is what makes the child print a JSON
result, which is what §5's RC3 depends on. This is a shape to replace, not a
flag to add, and it is the reason the change is a PRD rather than a patch.

### RC2 — `ccresu` is the parent, so the lifetime is `ccresu`'s (`internal/claude/exec.go`)

`OSExecer.Run` builds a `exec.CommandContext`, wires pipes, calls `cmd.Run()` and
waits. The child is a descendant of the daemon; a cancelled context kills it; a
daemon restart orphans it; a supervisor never learns it existed. Ownership is not
a policy `ccresu` chose, it is a property of calling `Run`.

### RC3 — Verification is bound to the runner (`internal/claude/cli.go:308-351`)

`ResumeResult.Verified` is set only after the child's JSON reports the expected
session ID, and the manager layers a transcript-growth check on top. Both need
`ccresu` to be holding the process. Remove print mode and the verification goes
with it — which is why this PRD cannot ship alone and why PRD 2 is a dependency,
not a follow-up.

### RC4 — The one existing encounter with a background session is destructive (`internal/claude/cli.go:260-299`)

```go
if resumeErr == nil || !IsSessionRunningInBackground(resumeErr) {
	return result, resumeErr
}
…
jobID := BackgroundJobID(resumeErr)
…
if stopErr := c.Stop(ctx, jobID); stopErr != nil { … }
```

This is `ccresu`'s only understanding of a background session: an obstruction to
be removed so that print mode can have the conversation back. It is careful,
well-commented code doing the wrong thing well. The PRD inverts the premise —
the background session is the destination, not the obstacle — and R8 exists to
stop this path from firing on a session `ccresu` itself backgrounded.

### RC5 — The working directory is never set (`internal/claude/cli.go:370`)

```go
opts := RunOptions{MaxOutputBytes: c.maxOutputBytes}
```

`RunOptions.Dir` exists, is documented as "the working directory; empty inherits
the parent's", and is assigned nowhere in the repository. `Session.ProjectPath`
is carried faithfully from discovery to the database to the UI, and then printed
rather than used. So a resume executes wherever the daemon happens to be. The
consequence is invisible until a session depends on its directory, at which point
it looks like a session-level bug rather than a process-placement one.

### The resulting shape

```
pass N:  RESET_TIME_REACHED → CHECKING_SESSION → RESUMING
         ccresu spawns: claude -p --resume <id> --output-format json <msg>
         ccresu waits, in ccresu's own cwd, owning the work
         child exits → ccresu reads the child's JSON → RESUMED
         the recovered work exists only in that child's lifetime
```

Nothing in that sequence is wrong on its own. The fault is that the fourth line
is the only place the work ever exists.

## 6. Requirements

| # | Requirement |
|---|---|
| **R1** | Recovery continues a session by handing it to the supervisor: `claude --resume <session-id> --bg "<message>"`. The print-mode flags (`-p`, `--print`, `--output-format`) are **removed** from the recovery invocation, not retained beside `--bg`. |
| **R2** | The continuation message is passed as the positional argument, `--bg` is placed before any bare `--` separator and outside another flag's value, and no `--` separator is emitted. |
| **R3** | `--resume` is given the session's **UUID**, never its display name and never a transcript path, because a name or path always produces a copy. |
| **R4** | A copy is an expected outcome, not a failure: when the CLI prints a `note:` line, `ccresu` records the copy's session ID as the conversation's current ID, keeps the original as its predecessor, and reports the recovery against the copy. When the session continues in place, no new conversation row is created. |
| **R5** | `--fork-session` is never passed on the recovery path. A deliberate fork is a separate operation with its own command, not something a recovery does by accident. |
| **R6** | `--name` carries a deterministic, `ccresu`-owned label so the recovered row is identifiable in agent view without consulting `ccresu`'s database. |
| **R7** | `ccresu` parses the `backgrounded · <short-id> · <name>` block, tolerating a preceding `Starting background service…` line, and records the short ID as the **state ID** — distinct from the session UUID — because `claude attach`, `claude logs` and `claude stop` all take it. |
| **R8** | The working directory of the handoff is set explicitly, and it is the directory the session actually occupies — never the daemon's cwd. For a session that has moved into a linked worktree under `.claude/worktrees/`, that directory is the worktree and not the project path it was dispatched from. Deciding *which* of the two it is, is PRD 6's job; this requirement demands only that the cwd is chosen deliberately rather than inherited, which today it never is. |
| **R9** | `Workspace not trusted` is a distinct, named outcome with its own operator-facing message and remedy, not a generic resume failure. |
| **R10** | The stop-then-resume path (`cli.go:260-299`) must not fire for a session `ccresu` backgrounded itself. A background session that `ccresu` created is the success case, not the obstruction. |
| **R11** | The handoff timeout and the verification timeout are separate settings. The handoff returns in seconds; the work does not. A single timeout covering both is the defect this PRD exists to remove. |
| **R12** | No compatibility shim. The print-mode resume path is retired rather than retained as a fallback; `v0.6.0` remains available to anyone who needs the old behaviour. |
| **R13** | An attempt is **stale** only when it has no recorded handoff (R7) and no finish time. A stale attempt — one the previous process opened and died inside — is closed as interrupted by the restart, never as a success and never as a failure of the session, and its session returns to `DISCOVERED` so the next pass re-derives the wait from the transcript instead of guessing it. An attempt with a recorded handoff is never stale: the supervisor owns that work, and a restart of `ccresu` is the event it is designed to survive (R11). The closure's failure-or-retry classification is PRD 3's R6. |

### Acceptance criteria

- `TestRecoveryHandsTheSessionToTheSupervisor` asserts the exact argv:
  `--bg --resume <uuid> --name <label> <message>`, with no `-p`, no
  `--output-format`, and no bare `--`. Falsifiable without a live supervisor,
  because it asserts the vector rather than the effect.
- `TestACopiedContinuationIsRecordedAsTheCurrentConversation` feeds a
  `note:`-bearing handoff and asserts the copy becomes current while the
  original survives as its predecessor; feeding an in-place handoff asserts no
  new conversation row.
- `TestAHandoffRecordsTheStateIDNotTheSessionID` asserts the parsed short ID is
  stored in a field distinct from the session UUID, and that a `Starting
  background service…` prefix line does not break parsing.
- `TestWorkspaceNotTrustedIsItsOwnOutcome` asserts the named outcome and its
  message, and that no retry is scheduled.
- `TestTheHandoffRunsInTheSessionsDirectory` asserts the child's working
  directory is the session's directory and not the test process's.
- `TestACcresuBackgroundedSessionIsNotStopped` asserts R10: the stop path is not
  reached for a session carrying a `ccresu`-owned handoff record.
- `TestAnAttemptWithNoHandoffIsClosedAndRediscovered` starts from an attempt row
  with no `FinishedAt` and no handoff record, runs the first pass, and asserts the
  attempt is closed without a success result and the session is `DISCOVERED`; the
  same fixture with a handoff recorded asserts the attempt stays open and the
  session is not moved.

## 7. Non-goals

- **Becoming the supervisor.** No daemon, no socket, no roster. `ccresu` issues
  commands and reads state; it does not host sessions.
- **Reading `~/.claude/jobs/<id>/`.** The docs call those files unstable. The
  short ID comes from the CLI's own printed block; everything else comes from
  `claude agents --json` (PRD 2).
- **Verification.** This PRD removes the old verification and deliberately does
  not replace it. PRD 2 does, and R10 above is written so that PRD 2 can be
  dropped in without touching the handoff.
- **Lifecycle.** `respawn`, `rm`, `daemon stop` and version-mismatch handling are
  PRD 3.
- **`--exec`.** Running a non-Claude command as a background job is a real
  capability and a plausible later PRD; it is not part of recovery.
- **Print-mode compatibility.** Explicitly out of scope. There is one resume
  path after this PRD, not two.

## 8. Proposed shape

| Step | Change | Satisfies |
|---|---|---|
| 1 | Replace `ResumeArgs` with a handoff argv builder that emits `--bg` first, the message last, and no print-mode flags | R1, R2, R3, R5 |
| 2 | Parse the handoff's printed block into a `Handoff` value: short ID, name, and an optional `note:` line | R7, R4 |
| 3 | Set the child's working directory from the session, threading `ProjectPath` into the invocation for the first time | R8 |
| 4 | Add the `Workspace not trusted` outcome and its message | R9 |
| 5 | Split the single resume timeout into handoff and verification settings | R11 |
| 6 | Narrow the stop-then-resume path so it cannot fire on a `ccresu`-owned background session | R10 |
| 7 | Record the handoff on the attempt row: state ID, label, copy-or-in-place, and the argv actually used | R4, R6, R7 |
| 8 | On startup, close an attempt that has neither a handoff nor a finish time and return its session to `DISCOVERED` | R13 |

The audit trail matters as much as the invocation. The previous PRD's lesson was
that a refusal's most valuable fact was destroyed at the first layer that saw it;
here the equivalent fact is *which* session the continuation landed in, and it
must be written down at the moment it is known.

### Migration

`prds/README.md` §4 asks every PRD for two things: what an existing installation
does on its first run after the change, and what an operator sees when their CLI
is too old for the only path that remains. This PRD's answers are blunt because
R12 removes the shim that would soften them.

- **First run: the print-mode child does not come back.** Upgrading restarts the
  daemon, and the resume it was holding was a child of the old process (RC2). The
  new process cannot adopt it, must not read its result, and must not signal it.
  R13 defines such an attempt as *stale* — no recorded handoff, no finish time —
  closes it as interrupted by the restart, and returns the session to
  `DISCOVERED` so the next pass re-reads the transcript and re-derives the wait
  instead of guessing it. That is not a compatibility path; it is the cleanup of a
  process that is already dead.
- **First run: an attempt that survives.** An attempt carrying a recorded handoff
  (R7) is not stale and is not touched. This is why R7 writes the handoff onto the
  attempt row: without it, a restart looks identical for an attempt the supervisor
  is running and one that died with `ccresu`, and the safe answer for the first is
  the wrong answer for the second.
- **First run: a session parked in `WAITING_FOR_RESET`.** Unaffected. The
  scheduler owns that state and the reset instant does not move because the binary
  did. What changes is what happens when the instant arrives: the first attempt
  after the change is a handoff, and print mode never runs again on that
  installation.
- **First run: an operator who watched `ccresu` own the work.** The shape they are
  used to — a child in `ps`, a JSON result in the log — is gone. What replaces it
  is a `backgrounded · <short-id> · <name>` line (R7) and a row in agent view
  (R6). `ccresu inspect` names the state ID, because that is what `claude attach`,
  `claude logs` and `claude stop` take.
- **A `claude` older than the handoff needs.** There is no fallback, because R12
  retires print mode rather than keeping it beside `--bg`, so the version floor is
  the answer. `internal/claude/version.go:106` moves from `2.0.0` to `2.1.257` —
  the release the docs name for `--resume <id> --bg` continuing the session in
  place or printing a `note:` line when it copies, which is exactly what R4
  records. The operator sees the existing `ErrUnsupportedVersion` from
  `checkVersion` (`"… is older than the required 2.1.257"`), raised wherever the
  CLI is touched (`internal/claude/cli.go:207`, `:256`;
  `internal/claude/provider.go:116`, `:133`) and listed as non-retryable in
  `internal/recovery/classify.go:98`, so the attempt is not retried on a backoff:
  the session returns to `DISCOVERED` (`classify.go:74-90`) and the operator sees
  the named version error instead of a retry loop that hides it. The remedy is
  the operator's: upgrade Claude Code, or stay on `v0.6.0`, which keeps the
  print-mode path they are running today.
- **Every later PRD inherits that floor.** PRDs 2–6 each answer the old-CLI case
  with a bullet of their own in §8 — "publishes no `state`", "without the `daemon`
  subcommand", "predates these notification types", "predates the guardrail
  flags", "publishes no `cwd`" — and each points back here instead of adding a gate
  of its own, which is why none of them repeats the version number as a rule.

## 9. Open questions

1. **Does `--name` combine with `--resume <uuid>` without forcing a copy?** The
   docs say `--resume` with "a name or file path" always starts a copy, and
   separately that `--name` sets the display name. They do not say whether
   `--name` alongside `--resume <uuid>` is the same trigger. R4 makes a copy
   survivable either way, but the answer decides whether R6 is cheap or needs a
   different labelling strategy. Settle it by experiment before implementing.
2. **Do the guardrail flags survive the move off print mode?** **Answered by PRD 5
   §6.1, and the answer is no for four of them.** `cli-reference` documents
   `--max-budget-usd`, `--max-turns`, `--json-schema` and `--permission-prompts` as
   print mode only; `--model`, `--effort`, `--fallback-model` and
   `--permission-mode` carry no such restriction and do ride the handoff. PRD 5
   replaces the two that bound total cost — `--max-budget-usd` and `--max-turns` —
   with a bound ccresu enforces itself from the `total_cost_usd` it already parses
   and discards, and replaces `--permission-prompts none` with
   `--permission-mode dontAsk`. The fallback if that proves impossible is stated
   there too: the spend cap covers the series of attempts, not a single turn.
   PRD 5 §9.6 also records the one documented way to *answer* a prompt on a
   non-interactive session, `--permission-prompt-tool`, which carries no
   print-mode restriction but needs an operator-run MCP server; `dontAsk` remains
   the stated fallback until that question is settled.
3. **What does a handoff to an already-`blocked` session do?** The session may be
   waiting on a permission prompt when the limit resets. Whether the handoff
   queues the message, is refused, or blocks in turn is PRD 2's territory, but
   the behaviour should be decided there rather than discovered in production.
4. **Should the handoff be retried when the supervisor is mid-restart?** The docs
   describe `--bg` printing `Starting background service…` and starting it, so
   the common case is handled. The uncommon case — a supervisor that is stopping
   — has no documented answer.
