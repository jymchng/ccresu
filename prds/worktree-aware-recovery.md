# PRD: a session is not one directory, and ccresu does not even run in that one

| | |
|---|---|
| **Status** | `Implemented` in `v0.7.0` (`c603ee8`) — `claude.ResolveWorkspace`, the persisted `sessions.runs_in_path` column (migration `0012_session_runs_in_path.sql`) and the worktree fixtures are on `main`, and every requirement in §6 has a test. This document stays the specification it was; §2 of [the index](README.md) is the record of what shipped |
| **Affected version** | `v0.6.0` (`55bf09e`) |
| **Component** | `internal/claude` → `internal/recovery` → `internal/storage`, `internal/app` |
| **Sources** | <https://code.claude.com/docs/en/agent-view>, <https://code.claude.com/docs/en/cli-reference>, <https://code.claude.com/docs/en/worktrees> |
| **Owner** | ccresu maintainers |

`worktrees` is a third source because the skip conditions, the isolation setting
and the resume-re-entry rules are documented there and only referenced from
`agent-view`. This PRD replaces the withdrawn capability-gating PRD: backward
compatibility is out of scope for this set, so a version→capability degradation
table has no purpose. What replaces it is a correctness fix that applies to every
supported CLI.

## 1. Summary

`ccresu` stores one directory per session, `Session.ProjectPath`
(`internal/storage/models.go:153`), and that one column is asked to answer two
different questions. It is *populated* from the session's observed `cwd` on every
pass (`internal/claude/provider.go:241-242`, on a map rebuilt per pass at `:193`)
and, when `agents --json` does not report the session, from the transcript's
project directory (`provider.go:282-283`). It is *used* to display the session —
the table, the TUI, the notification body — to filter discovery
(`claude.go:230-234`) and to persist the session row
(`internal/storage/sessions.go:99`). It is never used as a working directory.
`RunOptions.Dir` exists and is honoured (`internal/claude/exec.go:29-30`,
`cmd.Dir = opts.Dir` at `:77`), but the one place that builds a `claude`
invocation never sets it (`internal/claude/cli.go:359-375`). Every child process
therefore inherits ccresu's own cwd — and `deployments/systemd/ccresu.service`
sets no `WorkingDirectory=`, so under systemd that is the user's home directory.

So the bug is not "the directory is missing". It is recorded, refreshed,
persisted and displayed, and then not handed to the child: **ccresu never runs a
recovery in the session's project directory at all**, worktree or not. The
worktree case is the sharp end of a defect that is already present, and it adds a
second one — a column that is overwritten with the observed `cwd` cannot also
keep where the session *started*, which is what the resume's re-entry rules and
the operator's display both need (§6.2).

Two documented facts decide the design. The first is that a background session
does not live in its project directory: a dispatched session "moves into its own
worktree before editing files", under `<repo>/.claude/worktrees/<name>`
(`cli-reference` §`--worktree`), unless one of five documented conditions makes
Claude skip the worktree. The second is that the resume guarantee is scoped to
launching **from inside the repository**: "resume with `--resume` from the main
checkout or another directory of the repository" (`worktrees` §Resume a worktree
session). ccresu launches from `$HOME`, which is outside that envelope, so the
re-entry rules do not even apply to it.

The proposal is to record the directory a session actually runs in — which the
supported read path already publishes as `cwd`, "Always" present in every
`claude agents --json` entry (`agent-view` §List sessions as JSON), and which
`internal/claude/agents.go` already parses into `AgentEntry.CWD` (`:39`, from
`cwd`/`workingDirectory` at `:126-127`, mapped at `:151`) and reads only to
overwrite `ProjectPath` (`provider.go:242`) — and to set `RunOptions.Dir` from it
on every resume, falling back to `ProjectPath` when no observation has recorded
one.

## 2. What an operator sees today

| Observation | Why it happens |
|---|---|
| A recovered session edits files in the wrong checkout, or reports it cannot find the project | the child inherits the daemon's cwd, never `ProjectPath` (`cli.go:359-375`) |
| The same session recovers fine when the operator runs `ccresu recover` by hand from the project | the child then inherits the *operator's* cwd, which happens to be right |
| A session that moved into `.claude/worktrees/<name>/` resumes as if it were in the main checkout | the observed worktree path does reach `ProjectPath`, and then nothing hands it to the child (`cli.go:359-375`) |
| Nothing in ccresu fails when this happens | from v2.1.223 `--resume <id>` "searches the current project directory and its git worktrees, then every other project on this machine", so the session is found anyway |
| `ccresu sessions` shows a project path that is not where the work started | `ProjectPath` is overwritten with the latest observed `cwd` (`provider.go:241-242`), so the launch directory is kept nowhere |
| Worktrees under `.claude/worktrees/` accumulate across recoveries | a dispatched or copied session gets a worktree of its own, and nothing bounds how many recoveries happen |

The fourth row is why this defect has survived: the resume succeeds, so there is
no error to read. The wrong directory is invisible in an exit code.

## 3. Impact

- **Every recovery runs in the wrong directory.** Not only worktree sessions:
  a plain project session recovered under systemd runs in `$HOME`, so a resumed
  turn that reads `CLAUDE.md`, loads `.claude/settings.json` or resolves a
  relative path sees the wrong project — or none.
- **The failure is silent.** The ID search fallback finds the session from any
  cwd, so ccresu records a success and the transcript grows. PRD 2 is what makes
  the resulting state observable; this PRD is what makes the directory right.
- **The worktree case compounds it.** A background session's real directory is
  under `.claude/worktrees/`, so the observed `cwd` is the *only* correct answer
  for it — and one column cannot hold that answer and the launch directory at
  once.
- **A forking recovery loop would accumulate worktrees.** PRD 1 R4 treats a copy
  under a new ID as survivable. A dispatched copy "moves into its own worktree
  before editing files" (`agent-view` §Copy the session with /fork), so each copy can
  leave a worktree behind — and worktrees are only deleted with their session,
  and deletion is refused while commits are unpushed.
- **The data to fix it is already parsed and already flowing — to the wrong
  place.** `AgentEntry.CWD` is populated on every observation and read exactly
  three times, all of them into `Session.ProjectPath` (`provider.go:207`, `:212`,
  `:242`). The measurement exists; it reaches a label and a filter, never a
  process.
- **The fix is one field wide.** `RunOptions.Dir` is already threaded to
  `exec.Cmd.Dir`. No new dependency, no new subprocess, no new API.

## 4. Evidence

### 4.1 From the documentation

| Fact | Source |
|---|---|
| "When you dispatch a background session from agent view or start one with `claude --bg`, the session starts in your working directory. Before editing files, Claude moves the session into an isolated git worktree under `.claude/worktrees/`, so parallel sessions can read the same checkout but each writes to its own" | `agent-view` §How file edits are isolated |
| "Claude skips the worktree when: You moved a session you already had open to the background with `←` or `/background`. That session keeps editing files where it was already working / The session is already inside a linked git worktree, whether Claude created it under `.claude/worktrees/` or you created it with `git worktree add` somewhere else / The file that Claude is editing is inside a linked git worktree, such as one that the session or its subagent created with `git worktree add` / The working directory isn't a git repository and no `WorktreeCreate` hook is configured / The write is outside the working directory" | `agent-view` §How file edits are isolated |
| "To find a session's worktree path, attach and check its working directory" | `agent-view` §How file edits are isolated |
| "To turn off worktree isolation for a repository where git worktrees are impractical, set `worktree.bgIsolation` to `"none"`. Background sessions then edit your working copy directly without moving into a worktree first", via `{"worktree": {"bgIsolation": "none"}}` in the project's `.claude/settings.json` | `agent-view` §How file edits are isolated |
| "Once the session is in its worktree, Claude Code enforces worktree isolation for the session and for any subagents it spawns" | `agent-view` §How file edits are isolated |
| `claude agents --json`: "Each entry describes one session" — `cwd`, `kind`, `startedAt` are present **Always**; `cwd` is "The working directory" | `agent-view` §List sessions as JSON |
| `claude agents --cwd <path>` "shows only sessions started under that directory. It still lists a session that has moved into a worktree under `~/projects/my-app/.claude/worktrees/`" | `agent-view` §List sessions as JSON |
| "When you resume a session that ended inside a worktree without exiting it, Claude Code returns the session to that worktree" | `worktrees` §Resume a worktree session |
| "Launch directory: resume with `--resume` from the main checkout or another directory of the repository. Claude Code re-enters a worktree it created with git under `.claude/worktrees/` even when you launch from inside it. When you launch from inside any other worktree, Claude Code re-enters it only if it can vouch for it from there" | `worktrees` §Resume a worktree session |
| "When you pass a session ID, Claude Code searches the current project directory and its git worktrees, then every other project on this machine. Before v2.1.223, the ID search covered only the current project directory and its git worktrees" | `cli-reference` §`--resume` |
| `--worktree, -w`: "Start Claude in an isolated git worktree at `<repo>/.claude/worktrees/<name>`. If you don't give a name, Claude Code generates one" | `cli-reference` §CLI flags |
| "While an agent is running, Claude Code holds a `git worktree lock` on its worktree so that concurrent cleanup can't remove it, and releases the lock when the agent finishes. Claude Code holds the same lock on the worktree it created for a backgrounded session while the session runs, so the sweep leaves the worktree in place and `git worktree remove` refuses to remove it" | `worktrees` §Worktree locks and manual removal |
| "A session backgrounded with `←` or `/background` holds the `git worktree lock` on its worktree while it runs" (v2.1.248) | `agent-view` §Version history |
| "Non-interactive runs with `-p` have no exit prompt, so Claude doesn't clean up their worktrees, and Claude Code leaves the lock it took on each one at creation in place until a later session's stale-lock sweep releases it" | `worktrees` §Clean up subagent and background-session worktrees |
| "Deleting a session in agent view removes the worktree Claude created for it, but some worktree directories stay on disk. Directories git no longer recognizes don't appear in `git worktree list`, so remove those yourself" | `agent-view` §`.claude/worktrees/` is filling up |
| "Claude-created worktrees are deleted with the session in agent view: commit changes before deleting a session that edited files in its own worktree. Some worktrees stay on disk" | `agent-view` §Limitations |
| `claude rm <id>` refuses over unpushed commits (`--discard-unpushed <commit>@<worktree-id>`, v2.1.260+) or an unremovable directory (`--force-remove-worktree <worktree-id>`) | `agent-view` §Manage sessions from the shell |
| `worktree.bgIsolation` "setting added" in v2.1.143 | `agent-view` §Version history |
| `permissions.blockWritesOutsideWorkingDirectories`, when on, makes Claude Code refuse a background session's edits in the main checkout until it enters a worktree it "doesn't have"; `claude --bg --worktree`, `bgIsolation: "none"`, or a `git worktree add` under the start directory are the documented escapes | `worktrees` §Claude has no EnterWorktree tool |

Two rows carry the argument. The resume-re-entry guarantee is explicitly scoped to
"the main checkout or another directory of the repository" — ccresu launches from
outside the repository entirely, so it is not covered. And `cwd` is documented as
**always** present in `agents --json`, which means the worktree path is available
from the supported read path without a new integration.

### 4.2 From this repository

| Fact | Where |
|---|---|
| One directory per session, described as "the working directory the session belongs to" | `internal/storage/models.go:149-153` |
| The child's working directory is a supported option | `internal/claude/exec.go:29-30` (`Dir`), `:77` (`cmd.Dir = opts.Dir`) |
| No non-test code ever assigns `Dir` | `grep -rn 'Dir:' --include=*.go` → no hits outside tests |
| The resume invocation does not set it | `internal/claude/cli.go:359-375` (`run` builds `RunOptions{MaxOutputBytes: …}` and nothing else) |
| The resume vector has no directory flag either | `internal/claude/cli.go:353-355` (`ResumeArgs`) |
| The daemon runs with no configured working directory | `deployments/systemd/ccresu.service` — no `WorkingDirectory=` |
| The session's directory is rendered in nine places | `internal/app/commands.go:314`, `:486`, `:678`, `:689`; `internal/app/tui.go:349`, `:798`; `internal/telegram/template.go:93-94`, `:155-156`; `internal/claude/claude.go:201-203` (`Session.String`) |
| It is also a filter input and a persisted column, not only a label | `internal/claude/claude.go:230-234` (`DiscoverFilter.matches` → `underPath`); `internal/storage/sessions.go:78`, `:99`, `:105`, `:465`; `internal/storage/forget.go:220`, `:379`, `:400`; `internal/server/dto.go:97` |
| It is written from the observed `cwd` on every pass, and from the transcript slug as a fallback | `internal/claude/provider.go:241-242` (agents), `:282-283` (transcript); the map is rebuilt per pass at `:193`, and only `DiscoveredAt`/`Name` are carried over (`:178-184`) |
| `cwd` and `workingDirectory` are already parsed | `internal/claude/agents.go:126-127`, mapped at `:151` into `AgentEntry.CWD` |
| `AgentEntry.CWD` is documented as "the session's working directory" and is read only to overwrite `ProjectPath` | `internal/claude/agents.go:38-39`; `internal/claude/provider.go:207`, `:212`, `:242` |
| The observation path that would supply it exists and is the supported one | PRD 2's `claude agents --json --all` read |
| A copied session under a new ID is treated as survivable | PRD 1 R4 |
| The state machine has no vocabulary for "the directory moved" | `internal/recovery/state.go` |

## 5. Why it happens — the causal chain

### RC1 — `ProjectPath` is recorded, rendered, filtered, persisted — and never used as a working directory (`internal/storage/models.go:153`, `internal/claude/cli.go:359-375`)

`RunOptions` has a `Dir` field and `OSExecer` honours it:

```go
// Dir is the working directory; empty inherits the parent's.
Dir string
```

but the invocation builder never sets it:

```go
opts := RunOptions{MaxOutputBytes: c.maxOutputBytes}
if len(stdin) > 0 {
	opts.Stdin = bytes.NewReader(stdin)
}
return c.exec.Run(runCtx, c.bin, args, opts)
```

`Dir` stays empty, so `cmd.Dir = ""`, and the child inherits the daemon's cwd.
`ProjectPath` is not an unused field: it is rendered in nine places, fed to the
discovery filter (`claude.go:230-234`), persisted on every upsert
(`storage/sessions.go:99`) and rewritten from the observed `cwd` on every pass
(`provider.go:241-242`). The field exists and is busy; the use as a working
directory was never written.

### RC2 — Under systemd, the daemon's cwd is not a project at all (`deployments/systemd/ccresu.service`)

The unit sets `ExecStart=%h/.local/bin/ccresu daemon` and no `WorkingDirectory=`,
so a user service starts in the user's home directory. Every resume ccresu has
ever performed under systemd therefore ran in `$HOME`. This is the deployment the
project documents and ships, so it is the normal case, not the edge case.

### RC3 — The resume succeeds anyway, which is why nobody noticed (`cli-reference` §`--resume`)

From v2.1.223 an ID search is not scoped to the project: "searches the current
project directory and its git worktrees, then every other project on this
machine". The conversation is found, the transcript grows, PRD 2's observation
says the turn completed, and ccresu records a successful recovery. The only thing
wrong is the directory the work happened in — which no exit code reports.

A defect that fails silently is the class this whole set exists to remove, and it
is the reason this PRD cannot be satisfied by "the resume worked".

### RC4 — A background session's real directory is under `.claude/worktrees/` (`agent-view` §How file edits are isolated)

A dispatched session "moves into its own worktree before editing files". So the
launch directory is the wrong answer for a background session by construction: the
project directory is where it *started*, and the worktree is where it *is*. The
observed `cwd` is the only value that answers the second question — and the column
that holds it is the same one that was supposed to answer the first.

Five documented conditions make Claude skip the worktree, and they matter here
because each one implies a different recovery cwd (§6.1). Two of them — "the file
Claude is editing is inside a linked git worktree" and "the write is outside the
working directory" — are properties of a single *write*, not of the session, so
they do not move the session at all. Conflating them with the session-level
conditions would produce a cwd that follows a file rather than a session.

### RC5 — The worktree path is published, parsed, and discarded (`internal/claude/agents.go:39`, `:151`)

`claude agents --json` publishes `cwd` in every entry, documented as present
"Always" and described as "The working directory". `agentsWire` already accepts
both spellings:

```go
CWD        flexString `json:"cwd"`
WorkingDir flexString `json:"workingDirectory"`
```

and `mapAgentEntry` resolves them into `AgentEntry.CWD`. That field *is* read —
three times, and never for this: `provider.go:207` builds an unidentified
session's key from it, and `:212`/`:242` copy it into `Session.ProjectPath`. So
the value reaches a display column and a filter, and stops there. The fix needs
no new query, no new parser and no new subprocess: the directory is in the struct
that PRD 2's observation already builds, on the read path the docs call "the
supported way to read session state from outside Claude Code".

### RC6 — A forking recovery loop would multiply worktrees (`agent-view` §Copy the session with /fork, §Limitations)

PRD 1 R4 accepts that `--resume <id> --bg` may start a **copy** under a new ID. A
dispatched copy "moves into its own worktree before editing files". Worktrees are
"deleted with the session", and deletion is refused while commits are unpushed
(`claude rm` offers `--discard-unpushed`). So a recovery loop that repeatedly
forks would leave one worktree per attempt on disk, each of which may hold work
that blocks its own deletion.

This is the accumulation risk the goal asks about, and it is not hypothetical: it
is the composition of a PRD 1 design decision with a documented worktree
behaviour. The rule that prevents it is in §6.4.

### The resulting shape

```
claude agents --json --all        (PRD 2's observation)
  └─ cwd (always present) ──► AgentEntry.CWD  [already parsed, used only as a label]
        │
        ├─ recorded as Session.RunsInPath  ──► shown by `ccresu sessions`
        │
        └─ RunOptions.Dir  ──► exec.Cmd.Dir  ──► claude --resume <id> --bg
                 ▲                                    │
                 │                                    └─ re-enters the tree it occupies
                 └── fallback: Session.ProjectPath
                     (never empty, never the daemon's cwd)
```

## 6. Requirements

| # | Requirement |
|---|---|
| **R1** | Record the directory a session actually runs in, as a field distinct from `ProjectPath` (proposed: `Session.RunsInPath`), sourced from `AgentEntry.CWD` on PRD 2's supported read path. `ProjectPath` gains a single, stable meaning — where the session started — which it does not have today, because the observed `cwd` overwrites it (`provider.go:241-242`); both facts then survive. |
| **R2** | Set `RunOptions.Dir` on **every** `claude` invocation, not only resumes: the version probe, `agents --json` and the resume all currently inherit the daemon's cwd. An empty `Dir` becomes impossible: if neither the recorded run directory nor `ProjectPath` is usable, the attempt fails with a named error rather than inheriting. |
| **R3** | Resolve the recovery cwd in a fixed order: the recorded run directory, then `ProjectPath`. The order is a rule, not a preference, because the two disagree exactly when the session has moved into a worktree. |
| **R4** | Refresh the recorded run directory on **every** observation, not only at discovery. Today's single column is already rewritten per pass (`provider.go:241-242`, on a map rebuilt at `:193`); the requirement is to keep refreshing it *and* stop overwriting the launch directory with it, so a session that moves into a worktree mid-life has its run directory updated without losing where it started. |
| **R5** | **Never** pass `--worktree`/`-w` on a recovery handoff. A recovery must re-enter the tree the session already occupies; creating a worktree would fork the work into a second checkout that no one asked for. A test asserts the resume vector cannot contain it. |
| **R6** | Never create, delete, lock or unlock a worktree, and never run `git worktree` at all. ccresu does not own the worktree lifecycle: PRD 3 delegates lifecycle to the supervisor, and the lock is held by Claude Code while a backgrounded session runs (v2.1.248). A recovery that waited on that lock would deadlock against the session it is trying to resume. |
| **R7** | Never call `claude rm` to tidy a worktree. Removal is PRD 3's decision, and the documented refusals — unpushed commits, an unremovable directory — are exactly the cases where an automatic removal would destroy work. |
| **R8** | A recorded run directory that no longer exists falls back to `ProjectPath` and logs once, and the attempt proceeds. This mirrors the documented behaviour when Claude Code "clears the worktree binding" and records the clear in the transcript: a missing worktree is a fact to observe, not a reason to abandon the session. |
| **R9** | A recorded run directory that exists but is **not** the tree the session occupies must not be used silently. If `RunsInPath` and `AgentEntry.CWD` disagree on the next observation, the newer observation wins and the disagreement is logged — a recovery must never resume into a tree the session does not occupy. |
| **R10** | Recovery is **worktree-neutral**: N attempts for one session leave exactly the worktrees that existed before the first attempt. §6.4 states the rule and its three consequences, including the copy hazard from RC6. |
| **R11** | There is no configuration switch to disable any of this. A `respect_worktree: false` knob would be a switch whose only effect is to reintroduce the bug, and the set's standing constraint is that correctness is not optional. |
| **R12** | The recorded run directory is shown wherever `ProjectPath` is shown today, and the two are labelled distinctly — `project` for the launch directory and a separate label for where the session actually runs — so an operator can see the difference rather than infer it. |

### 6.1 Every documented worktree-skip condition, and the recovery cwd it implies

The five conditions are `agent-view` §How file edits are isolated. They are not
interchangeable: three move the *session*, and two describe a single *write* and
therefore do not change the session's directory at all.

| # | Documented condition | Where the session actually runs | Recovery cwd must be | Why |
|---|---|---|---|---|
| 1 | "You moved a session you already had open to the background with `←` or `/background`. That session keeps editing files where it was already working" | the directory it was already working in | that directory — the recorded `cwd` | the session never moved, so `ProjectPath` is only correct if that is where it was working |
| 2 | "The session is already inside a linked git worktree, whether Claude created it under `.claude/worktrees/` or you created it with `git worktree add` somewhere else" | the linked worktree | **that worktree**, never `ProjectPath` | this is the case the whole PRD exists for; `ProjectPath` is the main checkout |
| 3 | "The working directory isn't a git repository and no `WorktreeCreate` hook is configured" | the working directory | `ProjectPath` | no worktree exists, so the launch directory is the answer |
| 4 | "The file that Claude is editing is inside a linked git worktree, such as one that the session or its subagent created with `git worktree add`" | unchanged — the session stays where it is | the session's recorded `cwd`, **not** the worktree containing the edited file | the condition is about one write target, not the session; following it would move the cwd per file |
| 5 | "The write is outside the working directory" | unchanged | the session's recorded `cwd` | same reason as 4: a property of the write, not of the session |
| — | `worktree.bgIsolation` set to `"none"` (not a skip condition but the same effect) | the working copy | `ProjectPath`, which is the working copy | isolation is off entirely, so there is no worktree to find |

Rows 4 and 5 are the ones a table like this usually gets wrong. They read like
conditions under which "the session is elsewhere", and they are not: they describe
Claude declining to isolate a *particular edit*, while the session stays put. A
design that treated them as session-level would compute a cwd that changes with
the file being edited.

### 6.2 Where the run directory comes from, and why not from disk

| Candidate source | Available | Verdict |
|---|---|---|
| `claude agents --json` entry's `cwd` | documented "Always" present; already parsed into `AgentEntry.CWD` | **use this** — the supported read path (PRD 2), no new query |
| `~/.claude/jobs/<id>/state.json` | exists on disk | **never** — `agent-view` states "The files under `~/.claude/jobs/<id>/` are not a stable interface" (PRD 2's constraint) |
| The session's transcript path (`~/.claude/projects/<slug>/…`) | already recorded; today's fallback source | **no** — it names the *launch* project, which is exactly the value that is wrong after a move |
| `git worktree list` in the project directory | documented remedy for leftovers | **no** — it lists trees that exist, not which one a session occupies, and rows 4–5 of §6.1 have no answer in it |
| Attaching and reading the working directory | "To find a session's worktree path, attach and check its working directory" | **no** — it requires a terminal, and ccresu is a service with none |

The third row is the only source that names the launch project, and it is the
*fallback*: when `agents --json` does not report a session, `ProjectPath` comes
from the transcript's project slug (`provider.go:282-283`) and is structurally
the launch directory. When the CLI does report it, the same column holds the
observed `cwd` instead (`provider.go:241-242`). So one column is the worktree for
one session and the launch directory for another, with nothing to tell them
apart — which is the second half of this PRD, after `Dir`.

### 6.3 What must not change

| Behaviour | Why it must not change |
|---|---|
| `ResumeArgs`' argument shape | PRD 5 R2/R3 own the guardrail flags; this PRD adds no flag except `Dir`, which is not a flag |
| The resume *message* | a recovery's prompt is the existing one; nothing here changes what is asked, only where |
| `Session.ProjectPath`'s meaning | it must *become* "where the session started" and stay there: today the observed `cwd` overwrites it (`provider.go:241-242`), so the launch directory is lost on the first move. It stays the fallback, the display value and the value PRD 5's messages quote |
| Discovery | this PRD consumes PRD 2's observation; it does not add a discovery source |
| Worktree lifecycle | owned by Claude Code and the supervisor (R6, R7) |

### 6.4 The `.claude/worktrees/` accumulation rule

The documented facts that make this a rule rather than a nicety: a dispatched
session or a copy "moves into its own worktree before editing files"; a
Claude-created worktree is "deleted with the session"; deletion is refused while
the worktree holds unpushed commits, and `claude rm` offers `--discard-unpushed`
and `--force-remove-worktree` as the operator's explicit overrides; and leftover
directories that "git no longer recognizes" do not even appear in
`git worktree list`, so they are invisible to the documented cleanup path.

**The rule, in one sentence: N recovery attempts for one session must leave
exactly the worktrees that existed before the first attempt.**

Three consequences follow, and each closes a way a recovery loop would otherwise
worsen the accumulation:

1. **Never pass `--worktree`** (R5). A recovery re-enters a tree; it does not
   create one.
2. **Prefer same-ID continuation, and treat a copy as a new session with its own
   recorded cwd.** PRD 1 R4 accepts a copy under a new ID. If that happens, the
   copy is a *different session in a different tree*, so ccresu records the copy's
   `cwd` from the next observation and never re-forks it. Repeatedly forking one
   session would otherwise produce one worktree per attempt (RC6).
3. **Never clean up.** ccresu does not delete a worktree, does not `git worktree
   remove`, and does not call `claude rm` (R6, R7). Accumulation is reported —
   PRD 3's `claude logs`/`daemon status` surface and PRD 4's notifications are the
   places to say so — and never repaired by the component that is looping.

The honest limitation: this rule bounds what *ccresu* adds, not what exists. An
operator whose `.claude/worktrees/` is already filling up gets no automatic
remedy from ccresu, because the documented remedy (`git worktree list`, then
`git worktree remove <path>`, then `git worktree unlock` if git refuses) is an
operator action on the operator's repository.

### 6.5 Failure modes

| Failure | What ccresu observes | What it must do |
|---|---|---|
| **No run directory recorded yet** (every `v0.6.0` session) | `RunsInPath` empty | use `ProjectPath`; for an upgraded row that is the last observed `cwd`, which is the best answer available and still better than today's daemon cwd |
| **`ProjectPath` empty as well** | both empty | fail the attempt with a named error. Never inherit the daemon's cwd — an empty `Dir` is the bug |
| **Recorded run directory deleted** | `os.Stat` fails | fall back to `ProjectPath`, log once, proceed (R8) |
| **Recorded run directory is a worktree that still exists** | both exist and agree | use it, and only it (R3) |
| **`RunsInPath` and the latest observation disagree** | a newer `AgentEntry.CWD` | the newer value wins and the disagreement is logged (R9) |
| **Worktree locked by the running session** | a lock held by Claude Code (v2.1.248) | do nothing; never wait on it, never unlock it (R6). The resume either re-enters or the supervisor reports the session as running |
| **`blockWritesOutsideWorkingDirectories` is on** | a background session refusing edits in the main checkout | resume into the recorded worktree; the documented escapes (`--bg --worktree`, `bgIsolation: none`) are not ccresu's to apply |
| **Session copied to a new ID on handoff** | a new session id in the `backgrounded · <short-id>` block | record the copy's `cwd` on the next observation and never re-fork (R10) |
| **Non-git project** | no worktree ever exists | `ProjectPath` (row 3 of §6.1) |

### Acceptance criteria

- `TestRecoveryRunsInTheRecordedWorktree` seeds a session whose recorded run
  directory is a linked worktree (a real `git worktree add` in the test's temp
  repo), runs a recovery against a fake `Execer`, and asserts the `RunOptions.Dir`
  the fake received **equals the worktree path** — the falsifiable requirement the
  goal asks for, and it fails today because `Dir` is always empty.
- `TestRecoveryFallsBackToProjectPath` asserts that with no recorded run
  directory the child's cwd is `ProjectPath`, not the test process's cwd.
- `TestEmptyWorkingDirectoryIsANamedError` asserts that with neither directory
  available the attempt fails with a named error and no child process is spawned.
- `TestResumeVectorHasNoWorktreeFlag` asserts the handoff vector never contains
  `--worktree`, `-w`, or a `git worktree` invocation.
- `TestRunDirectoryIsRefreshedOnEveryObservation` asserts a session whose
  `AgentEntry.CWD` changes between two passes has its `RunsInPath` updated, so a
  mid-life move into a worktree is picked up.
- `TestNewerObservationWinsOverAStaleBinding` asserts R9: a recorded directory
  that disagrees with the latest observation does not get used, and the
  disagreement is logged.
- `TestRecoveryIsWorktreeNeutral` runs three recovery attempts for one session
  against a temp repo and asserts `git worktree list` is unchanged — zero
  worktrees created, zero removed.
- `TestMissingRunDirectoryFallsBackAndProceeds` asserts R8: a deleted worktree
  falls back to `ProjectPath`, logs once, and still resumes.
- `TestSessionsShowsTheRunDirectoryDistinctly` asserts R12: the table shows both
  the project and the run directory, with different labels, when they differ.

## 7. Non-goals

- **A version→capability table.** This PRD replaces the withdrawn
  capability-gating PRD. Backward compatibility is out of scope, and `Dir` is not
  a CLI flag, so there is nothing to gate.
- **Creating worktrees.** Never (R5, R6). A recovery re-enters a tree.
- **Cleaning up worktrees.** Never (R6, R7). The documented remedy is an operator
  action on the operator's repository.
- **`WorktreeCreate`/`WorktreeRemove` hook support.** The docs describe these as
  the non-git VCS path. ccresu reads a directory, so it needs no hook.
- **Overwriting `ProjectPath` with the worktree.** Keeping both is what makes the
  fallback and the display honest (R1, R12).
- **Subagent worktrees.** "Subagents that run in their own worktree carry the same
  checks", but a subagent's tree is not the session's, and ccresu does not model
  subagents.
- **Waiting on a worktree lock.** A recovery that blocked on the lock held by the
  session it is resuming would deadlock (R6).
- **The `~/.claude/jobs/<id>/` files.** "Not a stable interface" — PRD 2's
  constraint, and §6.2's second row.
- **Spend guardrails.** PRD 5.

## 8. Proposed shape

| Step | Change | Satisfies |
|---|---|---|
| 1 | Add `Session.RunsInPath` (migration + model field), distinct from `ProjectPath` | R1 |
| 2 | Populate it from `AgentEntry.CWD` in the observation PRD 2 already performs, on every pass | R1, R4 |
| 3 | Thread a resolved working directory into `cli.run` and set `RunOptions.Dir` for every invocation | R2, R3 |
| 4 | Make an empty resolution a named error instead of an empty `Dir` | R2 |
| 5 | On `os.Stat` failure of the recorded directory, fall back and log once | R8 |
| 6 | On disagreement between the binding and the latest observation, take the newer and log | R9 |
| 7 | Add `--worktree`/`-w` to the forbidden-flag assertion alongside PRD 5's print-mode flags | R5 |
| 8 | Show the run directory next to the project in `ccresu sessions` and the TUI, with distinct labels | R12 |

Step 3 is the whole bug: one struct field, set in one place, already honoured by
the executor. Everything else in this PRD is what makes the *value* right, and the
distinction matters for review — step 3 alone fixes every non-worktree session,
and steps 1–2 are what fix the worktree ones.

### Migration

- **First run with no recorded run directory.** Every existing session has
  `ProjectPath` and no `RunsInPath`. The first pass after the change records
  `RunsInPath` from the observed `cwd` for every session `agents --json` still
  reports — including a session already in a worktree — and falls back to
  `ProjectPath` for the rest. Either way the child stops inheriting the daemon's
  cwd, which is *already better than today*; nothing is back-filled and nothing is
  guessed.
- **A session mid-attempt when the change lands.** The working directory is
  resolved when the handoff is built, so an attempt whose handoff is already
  outstanding keeps the cwd it was handed off with, and PRD 1's R13 leaves that
  attempt open. The next attempt uses the resolved value.
- **A session parked in `WAITING_FOR_RESET`.** Unaffected; the scheduler owns that
  state and the cwd is resolved when the attempt would start.
- **A session whose worktree no longer exists at upgrade time.** `RunsInPath` is
  empty, so `ProjectPath` is used, which is the documented fallback when Claude
  Code "clears the worktree binding". No error is shown for a worktree that was
  never recorded.
- **An installation whose CLI publishes no `cwd`.** Not reachable through this
  PRD's own door: `cwd` is documented as always present in `agents --json`, and
  PRD 1's floor (`2.1.257`) is far newer than that surface. If it is absent anyway,
  `AgentEntry.CWD` is empty, every session falls back to `ProjectPath` — today's
  display value used as a working directory for the first time — and the operator
  sees the fallback named once in the log, on the rule R8 sets that a fallback is
  announced rather than silent. That is a strict improvement over inheriting the
  daemon's cwd, and it needs no version check, which is why this PRD needs no
  capability table.
- **An installation with `worktree.bgIsolation: "none"`.** The session never moves,
  `cwd` equals `ProjectPath`, and the two fields agree. No special case.

## 9. Open questions

1. **Does `cwd` in `agents --json` report the worktree after a session moves into
   one?** The docs say `cwd` is "The working directory" and is always present, and
   separately that "To find a session's worktree path, attach and check its working
   directory". They do not state the two together. **This is the load-bearing
   uncertainty of this PRD**: if `cwd` keeps reporting the launch directory, R1 has
   no source and the whole design needs a different read path. Settle it by
   observing one session before and after it enters a worktree. Note that the
   code already bets on the answer: `provider.go:242` writes `entry.CWD` into
   `ProjectPath`, so today's display value is right or wrong by this same
   uncertainty — it is not a new risk introduced by R1.
2. **Does `--resume --bg` need the cwd to be the worktree, or is the binding in the
   transcript?** "Claude Code re-enters a worktree it created with git under
   `.claude/worktrees/` even when you launch from inside it" suggests the binding
   is recorded, not inferred from cwd. If so, the cwd still decides which project's
   settings load and which sessions the ID search prefers, but it is not the only
   mechanism — and the PRD's claim should be scoped accordingly. Worth an
   experiment, because it decides how loudly to describe the current bug.
3. **Should `RunsInPath` be a path or a worktree identity?** A path is simple and
   works today; a worktree id would survive a move. The docs name worktree ids only
   in the `claude rm` overrides (`<worktree-id>`), which are not a read path.
4. **What should a recovery do when the recorded worktree exists but belongs to a
   different repository?** `worktrees` documents "The worktree directory belongs to
   a different repository" as a refusal case. ccresu can detect it only by asking
   git, and R6 forbids running git — so this may have to stay a supervisor
   problem. Left open deliberately.
5. **Should the two directories be reconciled at discovery or at recovery?** R4 says
   every observation, which is cheap. If the observation interval is ever raised,
   the reconciliation could lag a move by one pass — harmless, but worth stating
   rather than discovering.
