# PRD: the sessions browser answers the keyboard only when `claude` does

| | |
|---|---|
| **Status** | `Proposed` — design only. Nothing described here is implemented; no file outside `prds/` has changed |
| **Affected version** | `v0.7.0` (`c603ee8`), the current release; reproduced on `main` at `74fafc4` |
| **Component** | `internal/app` (the browser) → `internal/claude` (the supervisor reads, the execer) |
| **Reported symptom** | "ccresu becomes unresponsive; pressing Enter etc. doesn't open the Details of a session" |
| **Reproduced** | Yes, in a pty against a build of `74fafc4` — measurements in §4.1 |
| **Owner** | ccresu maintainers |

This is a defect PRD, not one of the six in [`README.md`](README.md) §2: it is not
an enhancement derived from the two Claude Code documentation pages, and it
measures itself against the release those six produced. It keeps the standing
constraints of that set which apply to it — no new module dependency, linear
history, no tag or release from a PRD, and a **Migration** subsection at the end
of §8 — and it needs no capability gate for the same reason the withdrawn draft
did not (§2 of the index): on a platform or a CLI where the new behaviour is
unavailable, the browser does exactly what it does today.

## 1. Summary

`ccresu sessions` is a keystroke loop with one thread of control
(`internal/app/tui.go:276-295`): read a key, **do the work**, paint. Four of the
browser's keys do work that talks to `claude` — Enter and `i` render the
`sessions status` page, which reads the supervisor on demand; `n` runs the
daemon's discovery pass; `r` and `R` run a whole recovery — and every one of
those calls happens on the goroutine that is supposed to be reading the keyboard.
So for as long as `claude` takes to answer, the browser reads nothing, paints
nothing, and shows no sign that anything is happening.

The operator's report is exactly this, and the second half of it is the part that
makes the first half look permanent. The terminal keeps accepting bytes while the
process is blocked, so the keys pressed during the wait are not lost: they queue
in the tty and are decoded the moment the work returns, **against the screen the
work produced**. Press Enter, see nothing, press Enter again, and the second
Enter arrives on the detail screen and closes it — the browser ends up back on
the list, with the detail screen's status line still painted, as if Enter had
never been pressed at all.

None of it can be cancelled. The browser clears `ISIG`
(`internal/app/tui_term_linux.go:26`) so `ctrl-c` arrives as a byte and can
restore the screen — a good decision — but that byte is only decoded by the loop
that is currently blocked, so `ctrl-c` does nothing either. The one cancellation
path that exists, `App.signalContext` (`internal/app/app.go:380-401`), is only
reachable from a signal, and the keyboard no longer sends one. The operator's
only exit is `SIGKILL` from another terminal, which leaves their terminal in raw
mode with the cursor hidden and the alternate screen on, because the deferred
restore never runs.

The fix is not "make `claude` faster". It is that the browser must never do work
on the goroutine that reads the keyboard, must say what it is waiting for, must
let `ctrl-c` mean cancel, and must not let a queued keystroke act on a screen the
operator has not seen yet.

## 2. What an operator sees today

Everything below was observed in a pty, not reasoned about; §4.1 lists the
measurements and §4.2 the code.

| Step | What the operator does | What the screen does |
|---|---|---|
| 1 | `ccresu sessions` | draws the list in milliseconds |
| 2 | presses `enter` on a row | **nothing at all** — no frame, no cursor move, no status line |
| 3 | waits | still nothing, for as long as the CLI takes |
| 4 | presses `enter` again, because the first press appeared to do nothing | still nothing |
| 5 | waits | the detail screen is painted; then the queued second `enter` is decoded, which is "close the detail" on that screen |
| 6 | looks at the screen | the **list**, with `press enter, q or esc to return to the list` painted where the status line goes — the detail page they asked for is gone and they never saw it |

Two smaller instances of the same defect sit beside it:

- **`esc` does not work either, until you press something else.** The documented
  footer on the detail screen is `press enter, q or esc to return to the list`,
  and `esc` alone does nothing: `decodeEscape` (`internal/app/tui.go:123-133`)
  reads the byte *after* the escape before deciding it was a bare escape, so the
  key is decoded only when the next keystroke arrives.
- **The status line outlives its screen.** `b.status` is written by every action
  and never cleared (`internal/app/tui.go:193`, `:772-775`, `:846-848`), so a
  message about the detail screen is painted on the list and a message about the
  list is painted on the detail screen.

And the detail page itself, when it does arrive, is a page of 21 to 26 lines —
depending on what the session and the supervisor have to say — of which 15 fit,
with the operator's most useful block — what the supervisor says about the
session, `writeSupervisorDetail` — starting below the fold. The detail
screen's footer names only the keys that leave it; it never says the page
scrolls, and the list's `g`/`G` do nothing there.

## 3. Impact

- **The browser's primary key looks broken.** Enter is the key the footer puts
  first (`enter detail`), and on a machine whose `claude` takes two seconds it is
  a two-second silence; on one where the supervisor is wedged it is 30 seconds
  per invocation and up to 90 seconds for one press (§4.1, §5 RC3).
- **The work is done and then thrown away.** Because the queue replays, the
  operator's impatient second press *undoes* the thing the first press asked for.
  The browser does not merely feel slow; it discards the result.
- **Nothing can be cancelled, and the terminal pays for it.** `ctrl-c` is dead
  while an action runs, and the only remaining exit is `SIGKILL`, which leaves the
  tty in raw mode with no echo and the alternate screen active. The operator's
  shell is unusable until they run `reset`.
- **The slowest action is the one that matters most.** `R` (resume now) runs a
  whole recovery inline: the supervisor check, the session status read, the
  published-listing observation, the handoff, the verification and a second
  observation (§5 RC2). On a slow CLI that is minutes of a frozen screen with the
  cursor hidden, and no indication that a recovery is in progress.
- **The defect is invisible to the test suite**, which is why it shipped (§5
  RC8). Every browser test drives an instant fake through a pre-loaded keystroke
  string, so the suite cannot observe a wait, a queue or a cancel.

## 4. Evidence

### 4.1 Measured, in a pty, against `74fafc4`

A build of `cmd/ccresu` at `74fafc4`, a temporary database with one seeded
session, and a pty driven by a script that timestamps every frame the browser
writes. `claude.executable` was pointed at stand-ins where a real CLI's timing
had to be controlled: `/bin/sh` scripts that sleep then answer, a Python script
that sleeps in its own process, and one that sleeps while leaving a child
holding the pipes.

| Measurement | Result |
|---|---|
| `claude daemon status` on this machine, no supervisor running | 2.45 s, exit 1 |
| `enter` → detail page, real `claude`, no supervisor | 0.89 s, then 2.38 s on a second run |
| `enter` → detail page, stand-in at 3 s per call, supervisor reported running | **6.03 s** (two calls: `daemon status`, then `daemon logs`) |
| `enter` → detail page, stand-in that never answers | **30.03 s** — the exact `DefaultQueryTimeout` — then the page appears with the supervisor block unread |
| `enter` → detail page, stand-in that never answers **and leaves a child holding the pipes** | no frame after **45 s**, and no bound in sight (§5 RC3a) |
| Bytes the browser writes between the keystroke and the frame | **0** |
| `ctrl-c` and then `q` pressed during that window | 0 bytes written, process still alive, no cancellation |
| `SIGKILL` during that window | no `ESC[?1049l`, no `ESC[?25h`; the pty still has `ICANON` and `ECHO` cleared — a shell with no echo |
| `enter`, then `enter` again 0.4 s later | detail painted at 6.03 s and immediately closed; final screen is the **list**, carrying the detail screen's status line |
| a lone `esc` on the detail screen | no frame for 3 s; the `esc` is decoded only when `j` is pressed afterwards |

The last two rows are the reported symptom and the report's "etc." respectively.

### 4.2 Read out of the repository

| Fact | Where |
|---|---|
| The browser is one loop: read a key, `b.handle` (which does the work), `b.draw` | `internal/app/tui.go:276-295` |
| `enter` and `i` call `openDetail`, which renders the whole `sessions status` page inline | `internal/app/tui.go:421-423`, `:452-453`, `:585-600` |
| `r` and `R` call `Manager.Retry` / `Manager.RecoverRequest` inline | `internal/app/tui.go:616-631` |
| `n` calls `Provider.Discover` inline | `internal/app/tui.go:671-706` |
| The detail page reads the supervisor on demand: `daemon status`, then `daemon logs`, then `logs <state-id>` | `internal/app/commands.go:598-600`, `:1292-1346` |
| Each of those is bounded by the query timeout, 30 s | `internal/claude/cli.go:20`, `:127-132`, `:357-383`; `internal/claude/daemon.go:182-209`, `:243-254` |
| `Discover` runs two invocations of its own: `--version`, then `agents --json` | `internal/claude/provider.go:172-189`, `internal/claude/cli.go:163`, `:185` |
| A recovery is a chain of four or more invocations plus the handoff and the verification | `internal/recovery/manager.go:518-724` |
| The handoff and verification budgets are 120 s each by default | `internal/config/defaults.go:52-54` |
| `exec.CommandContext` kills the child, then `cmd.Run()` waits for the pipes to close | `internal/claude/exec.go:76`, `:90` |
| `ISIG` is cleared, so `ctrl-c` is a byte and not a signal | `internal/app/tui_term_linux.go:14-26` |
| The cancellation that exists is driven by `SIGINT`/`SIGTERM` only | `internal/app/app.go:308-310`, `:380-401` |
| A bare `esc` is decided only after the next byte is read | `internal/app/tui.go:123-133` |
| `b.status` is written on every action and never cleared | `internal/app/tui.go:193`, `:772-775`, `:846-848` |
| The detail screen draws 15 of the page's lines and names only the leave keys | `internal/app/tui.go:833-849`, `:868-887` |
| The store reads behind `reload` are bounded by `busy_timeout` (5 s), not by anything the browser does | `internal/storage/storage.go:58` |
| `claude.FakeProvider` implements no supervisor surface, so the browser's supervisor read is never exercised | `internal/claude/fake.go:152-268`; `internal/app/runtime.go:59-66` |
| The browser tests feed the whole keystroke string at once, so a wait cannot be observed | `internal/app/harness_test.go:341-347`, `internal/app/tui_test.go:396-430` |
| One test already encodes the replay: `"\r\rq"` expects the detail screen and then the list | `internal/app/tui_test.go:421` |
| The docs promise `ctrl-c` exits cleanly because it is a keystroke | `docs/cli.md:161-167` |

## 5. Why it happens — the causal chain

### RC1 — One goroutine reads the keyboard, does the work and paints (`internal/app/tui.go:276-295`)

```go
for {
    key, err := decodeKey(b.in)      // ← nothing is read while handle runs
    …
    quit, err := b.handle(ctx, key)  // ← this is where claude is invoked
    …
    b.draw()
}
```

There is no worker, no channel and no timer anywhere in the file. Every property
below follows from that one line: the browser cannot read while it works, cannot
paint while it works, and cannot notice `ctrl-c` while it works.

### RC2 — "Handle" means "invoke `claude`", for four of the keys

| Key | What `handle` does | Invocations of `claude` |
|---|---|---|
| `enter` / `i` | `writeSessionDetail` → `readSupervisor` | 0 if there is no supervisor, else 1–3 |
| `n` | `Provider.Discover` → `Version`, then `Agents` | 2 |
| `r` | `Manager.Retry` → `RecoverRequest` | 4 or more, plus the handoff |
| `R` | `Manager.RecoverRequest` | 4 or more, plus the handoff |
| `e`, `d`, `x` | one store write | 0 |
| `j`, `k`, `g`, `G`, `a`, `/`, `c` | `reload`: two store reads | 0 |

The cheap keys are the ones that were designed as the browser's own; the four
expensive ones are the ones the browser exists for, because each is a documented
CLI command rendered as a keystroke. Nothing in the design says those commands
may take a minute; nothing in the code notices that they do.

### RC3 — The detail page is the *inspect* page, and inspect asks the supervisor (`internal/app/commands.go:1292-1346`)

Enter is the reported key, and it is the cheapest of the four: it reads the store,
then asks the supervisor three things in sequence — `claude daemon status`, then
`claude daemon logs` (only while the supervisor is running), then
`claude logs <state-id>` (only when the latest attempt recorded a state id). Each
is bounded by `DefaultQueryTimeout`, 30 s. So one keystroke is bounded by
**90 seconds**, and on this machine a single `daemon status` already costs 2.45 s.

The three reads are right for `ccresu inspect`: it is a batch command, its whole
job is to answer the question, and waiting is what it is for. They are wrong for
the browser, whose job is to answer the keyboard.

### RC3a — A call that misses its deadline can still block forever (`internal/claude/exec.go:76`, `:90`)

`exec.CommandContext` kills the process when the context is done, but
`cmd.Run()` also waits for the stdout and stderr pipes to close, and a descendant
that inherited them keeps them open. A `claude` that spawns a child and exits, or
one that is itself a wrapper around a long-lived child, therefore returns neither
on completion nor at the deadline. Measured: a stand-in that sleeps in a child
(`/bin/sh -c 'sleep 300'`) left the browser frozen with no frame after 45 s, where
the same stand-in sleeping in its own process produced a frame at exactly 30.03 s.

This is not a browser bug; it is the reason the browser's worst case is not
"30 seconds" but "until something else happens". Go 1.20 added `cmd.WaitDelay`
for precisely this case, and this module is on Go 1.26.

### RC4 — `ctrl-c` is a byte that only the blocked loop can read (`internal/app/tui_term_linux.go:26`)

Clearing `ISIG` is the right call — the alternative is a `SIGINT` that kills the
process mid-paint and leaves the terminal broken — but it has a consequence the
file does not state: the browser's only cancellation input is a byte, and the only
reader of bytes is the goroutine that is currently inside `writeSessionDetail`.
The signal path that *does* cancel the context (`App.signalContext`) is
unreachable from the keyboard, so the cancellation machinery the process already
has cannot be used by the person sitting in front of it.

### RC5 — The tty keeps the keystrokes, and they act on the next screen (`internal/app/tui.go:97-119`)

The kernel buffers what the operator types while the process is not reading. When
`openDetail` returns, `decodeKey` reads those bytes in order and hands them to
`handle` — but `b.mode` is now `tuiModeDetail`, so a queued `enter` is the detail
screen's *close* key, a queued `j` scrolls the page instead of the list, and a
queued `q` closes the detail and then quits the browser. The keys were pressed
against one screen and are applied to another. This is the mechanism by which "I
pressed Enter and nothing happened" becomes "the detail page opened and closed
before I saw it".

### RC6 — The screen says nothing about waiting, and its status line is shared (`internal/app/tui.go:193`, `:772-775`)

There is no busy state, no spinner, no elapsed time and no "asking the
supervisor" line, so a browser that is working and a browser that is hung paint
the same bytes — none. The status line that would carry such a message is a
single field shared by every screen and cleared by nothing, which is how the
detail screen's `press enter, q or esc to return to the list` ends up painted on
the list.

### RC7 — A bare `esc` waits for the next keystroke (`internal/app/tui.go:123-133`)

```go
next, err := in.ReadByte()   // blocks until the terminal says something else
if next != '[' && next != 'O' { … return keyEvent{name: keyEsc}, nil }
```

A real terminal sends `ESC` alone and then waits for the operator, so the decoder
waits with it. The detail screen's footer therefore advertises a key that does
nothing until the operator gives up and presses another one. It is the same defect
as RC1 at a smaller scale: the browser's input path is allowed to block on
something that may never arrive.

### RC8 — No test can see any of this (`internal/app/harness_test.go:341-347`, `internal/app/tui_test.go:421`)

Three independent reasons:

1. `runWithKeys` hands the browser a `strings.Reader` containing every keystroke,
   so input is always available and ordering is total. A wait cannot exist.
2. `claude.FakeProvider` implements `Discover`, `GetSession`,
   `GetSessionStatus`, `PublishedListing` and `ResumeSession` — and no supervisor
   surface at all, so `rt.Provider.(SupervisorReader)` fails in every browser test
   and the three reads that make Enter slow are never made.
3. The one test that presses Enter twice, `runWithKeys("\r\rq")`, asserts exactly
   the replay: the detail screen is drawn, and the final screen is the list. It
   passes today, and it passes for the wrong reason.

A test suite that cannot observe a wait cannot protect the operator from one.

### The resulting shape

```
operator                        │ browser goroutine               │ claude
   │                            │                                 │
   ├── enter  ─────────────────►│ ───────────────────────────────►│ daemon status  (30 s)
   │   no frame, no echo,       │                                 │ daemon logs    (30 s)
   │   no cancel                │                                 │ logs <id>      (30 s)
   ├── enter  ─────────────────►│ …still blocked; byte queued     │
   │                            │◄────────────────────────────────┤
   │                            │ ├── draw detail                 │
   │                            │ ├── decodeKey → enter (queued)  │
   │                            │ ├── handleDetail: close         │
   │                            │ └── draw list + stale status    │
```

## 6. Requirements

| # | Requirement |
|---|---|
| **R1** | No keystroke may run a subprocess, a supervisor read, a discovery pass or a recovery on the goroutine that reads the keyboard. The reader, the painter and the action runner are three concurrent things joined by channels, and the browser's select loop is the only place that paints. |
| **R2** | The browser has a busy state and paints it. Within one repaint of the keystroke that starts an action, the screen names what it is waiting for and the key that cancels it — `recovering 4d1f9c2a… (ctrl-c cancels)` — and the cursor stays where it was. |
| **R3** | A keystroke is never applied to a screen the operator has not seen. While a gated action is in flight, the keys that start another gated action (`n`, `e`, `d`, `r`, `R`, `x`, `enter`, `i`) are **refused**, with a status line naming the action that is running; they are not queued. |
| **R4** | `ctrl-c` cancels the in-flight action from the keyboard and does not wait for it: the action's context is cancelled (so its subprocess is killed), the browser stays open on the list, and the status line says `cancelled`. `q` (and a second `ctrl-c`) quits, cancelling first. |
| **R5** | Every action is bounded twice: by the budget that belongs to it (§6.2) and by the operator's `ctrl-c`. No action may wait on a call with no deadline. |
| **R6** | The detail screen is painted in two parts. The session's own facts — everything the store holds — are painted from the keystroke, unconditionally. The supervisor block is a **background fill** with its own budget, tagged with the session id; a late answer for a session the operator has left is discarded, a new detail request cancels the previous fill, and at most one fill is in flight. |
| **R7** | A lone `esc` is decoded as `esc` within a bounded time (`tuiEscapeTimeout`, §6.2), without a second keystroke. On a platform where the read cannot carry a deadline, today's behaviour is the fallback and the documentation says so. |
| **R8** | The status line belongs to a screen: entering or leaving a screen clears it, and a message that describes one screen is never painted on another. |
| **R9** | A call that misses its deadline returns. `OSExecer.Run` must not outlive its context when a descendant inherited the pipes (`cmd.WaitDelay`). |
| **R10** | The terminal is restored on every exit path that can be caught: `q`, `ctrl-c`, end of input, an error, and `SIGTERM`/`SIGINT`/`SIGHUP` delivered from outside. `SIGKILL` cannot be caught, so the docs name `reset` as the remedy — and no exit path may depend on the process surviving. |
| **R11** | The detail page stays the `sessions status` page: byte-for-byte when the supervisor answers, and the busy and expired variants differ only inside the supervisor block. Its footer names its own keys, from `tuiKeyMap`, so it cannot disagree with the list about how to move. |
| **R12** | Nothing here polls the supervisor (PRD 3 R10), adds a module dependency, changes a key's meaning, or adds a screen. |

### 6.1 The busy gate

| Key | While a gated action is in flight |
|---|---|
| `enter` / `i` | refused, status line names the running action |
| `n`, `e`, `d`, `r`, `R`, `x` | refused, same line |
| `j`, `k`, `g`, `G`, `a`, `/`, `c` | work as today; they read the store, which is not what the gate is for |
| `?` | works; the key map is static |
| `ctrl-c` | cancels the action, stays on the list (R4) |
| `q` | cancels and quits (R4) |
| EOF | cancels and quits |

A refused key must be *visible*: silence is what made the reported symptom
ambiguous in the first place.

### 6.2 The budgets

| Action | What it waits on | Budget today | Budget proposed |
|---|---|---|---|
| `enter` / `i` | store reads | ~1 ms | unchanged, and painted immediately |
| `enter` / `i` | supervisor fill | 0.9–90 s, unbounded in RC3a | `tuiSupervisorBudget` = 5 s, in the background; on expiry one line says it was not read and why |
| `n` | `--version` + `agents --json` | up to 60 s | unchanged — it is one deliberate pass — but cancellable |
| `r` / `R` | 4 or more reads, the handoff, the verification | up to ~4 min | unchanged — those budgets are the recovery's, not the browser's — but cancellable |
| `e`, `d`, `x` | one store write | ms | unchanged |
| movement, filter | two store reads | ~1 ms, bounded by `storage.busy_timeout` (5 s) | unchanged, and documented |
| a lone `esc` | the next keystroke | unbounded | `tuiEscapeTimeout` = 50 ms |

The 5 s figure is chosen from the measurements in §4.1 — a healthy `daemon
status` costs 0.9–2.5 s here, and the worst case is 30 s — and because the fill is
a fill: the operator is already reading the page while it happens. Whether the
browser should read the supervisor at all is open question 2.

### 6.3 What the detail screen paints, and when

1. On the keystroke: the facts, the `Next attempt` block, and the supervisor block
   as one line — `supervisor: reading… (enter or esc leaves; the page fills in)`.
2. On the answer: the same page with the real block, at the same scroll offset the
   operator has reached in the meantime.
3. On the budget: `supervisor: not read — claude daemon status did not answer
   within 5s`, and `press 'i' to try again`.

Case 3 is the only case in which the browser's page differs from
`sessions status`, and the difference is confined to the block that could not be
read.

### 6.4 Failure modes

- **The action fails.** The error goes to the status line, the browser keeps
  running and the cursor does not move — today's behaviour (`internal/app/tui.go:284-290`),
  preserved.
- **The action is cancelled.** `ctrl-c` cancels the context; the action reports
  `context.Canceled`, which the status line renders as `cancelled`. A cancelled
  recovery leaves whatever the manager had already written — the browser invents
  no state, and `Manager.RecoverRequest` already records the attempt row before it
  hands off (`internal/recovery/manager.go:655-666`).
- **The store read fails.** As today: the status line carries the error and the
  listing is left as it was.
- **EOF during an action.** Cancels and quits, so `printf 'q' | ccresu sessions
  --tui` and a closed terminal both behave.

### Acceptance criteria

- `TestTheBrowserPaintsTheFactsWhileTheSupervisorIsRead` — a fake whose
  `DaemonStatus` blocks on a channel: after `enter`, a frame containing the
  session's facts **and** the `reading…` line is drawn before the fake is
  released; releasing it paints the block.
- `TestTheDetailPageMatchesSessionsStatusWhenTheSupervisorAnswers` — the detail
  bytes equal `sessions status`'s output for the same session.
- `TestASecondEnterDoesNotCloseADetailThatWasNeverPainted` — `enter`, `enter`
  while the fill is blocked, release: the final screen is the detail page and the
  fake saw exactly one `DaemonStatus` call.
- `TestGatedKeysAreRefusedWhileAnActionRuns` — `R`, then `n` and `enter` while the
  fake blocks: neither starts work (the fake's call count does not move) and the
  status line names the running action.
- `TestCtrlCCancelsTheActionAndKeepsTheBrowser` — `R`, `ctrl-c` while the fake
  blocks: the fake observes `ctx.Done()`, the browser returns to the list with
  `cancelled`, and a following `q` exits `ExitOK`.
- `TestQQuitsWithoutWaitingForTheAction` — `R`, `q` while the fake blocks: the
  invocation returns `ExitOK` and the fake was still blocked when it did.
- `TestALoneEscapeIsDecodedWithoutASecondKey` — `ESC` and no more bytes produces
  `keyEsc` within `tuiEscapeTimeout`.
- `TestTheStatusLineDoesNotSurviveAScreenChange` — the list frame painted after
  leaving the detail screen does not contain the detail screen's status text.
  **This one fails today.**
- `TestACallThatMissesItsDeadlineReturns` — a stand-in that leaves a child holding
  the pipes: `OSExecer.Run` returns within the deadline plus a small slack.
- `TestTheBrowserRestoresTheTerminalOnSIGTERM` — pty-based and skipped where a
  pty is unavailable: the alternate screen and the cursor are restored, and the
  termios flags are back.
- `TestTheBrowserNeverCallsClaudeOnTheKeystrokePath` — a source-level guard in the
  spirit of `TestCcresuNeverRespawns`: no handler reachable from `decodeKey`'s
  result invokes a `claude` method outside the worker's dispatch.

## 7. Non-goals

- **A TUI framework.** The hand-written loop and its "no new module dependencies"
  note (`internal/app/tui.go:19-26`) stay. The fix is a channel and a select.
- **Making `ccresu inspect` fast.** The batch command may wait; that is its job.
  R5 constrains the browser, not the CLI.
- **Making `claude` faster, or hiding that it is slow.** A slow CLI is a fact to
  report, not to paper over.
- **Changing the key map's meanings or adding screens.** `enter` still opens the
  detail, `R` still resumes; only *when* the browser answers changes.
- **Caching supervisor reads in SQLite, or polling them.** PRD 3 R10 stands: the
  reads are on demand and the browser is a consumer of that rule, not an exception
  to it.
- **The `busy_timeout` on store reads.** Movement is a SQLite read; the fix does
  not move it off the UI goroutine (open question 4).
- **The recovery engine's budgets.** `recovery.startup_timeout` and
  `recovery.verification_timeout` are the recovery's, and the browser inherits
  them unchanged.

## 8. Proposed shape

| Step | Change | Satisfies |
|---|---|---|
| 1 | Split `runSessionBrowser` into a select loop, a key reader goroutine and an action worker; the reader sends `keyEvent`s on a channel, the worker sends results on another, and only the loop paints | R1 |
| 2 | Add the busy state to `browser`: the action's name, the session it is about, its start instant and a cancel function; paint it in `drawList` and in the detail footer | R2, R5 |
| 3 | Gate the action keys on the busy state and refuse them with a message, instead of letting the tty queue them | R3 |
| 4 | Handle `keyInterrupt` in the loop while busy as "cancel the worker, keep the browser"; make `q` and EOF cancel then quit | R4 |
| 5 | Split `writeSessionDetail` into the facts and the supervisor block (the block is already `writeSupervisorDetail`, `internal/app/commands.go:1349`), and give the browser a `fillSupervisor(ctx, sessionID, budget)` that repaints on the answer | R6, R11 |
| 6 | Give the decoder a bounded escape lookahead — a `deadliner` interface satisfied by `*os.File` under the existing `tui_term_*` build tags, with today's behaviour where there is none | R7 |
| 7 | Clear `b.status` on every mode change, and give the detail screen a footer from `tuiKeyMap` | R8, R11 |
| 8 | Set `cmd.WaitDelay` in `OSExecer.Run` so a pipe-holding descendant cannot outlive the deadline | R9 |
| 9 | Install a signal handler around the browser that restores the screen before exiting, beside the existing `signalContext` | R10 |
| 10 | Give `claude.FakeProvider` a supervisor surface that answers instantly, and give the harness a blocking fake and a keystroke stream that can be paced | the acceptance criteria of R1–R4; closes RC8 |
| 11 | Update `docs/cli.md` §"Browse sessions interactively": the busy line, `ctrl-c` as cancel, the fill, `esc`, and the `reset` remedy for `SIGKILL` | R10 |

Step 1 is the whole fix; steps 2–5 are what make it visible to the operator, and
step 10 is what keeps it fixed. Steps 8 and 9 are small and independent, and each
one closes a way for the browser to become unkillable or leave a terminal broken.

### Migration

- **First run after the upgrade.** No configuration key, no migration and no key
  changes meaning. The differences an operator sees are: the busy line while an
  action runs, `ctrl-c` cancelling instead of doing nothing, the detail page
  appearing at once with its supervisor block filling in, and `esc` working on its
  own. `ccresu sessions --no-tui`, `--json` and piped output are byte-for-byte
  what they were.
- **Scripted use.** `printf 'q' | ccresu sessions --tui` and any redirect are
  unchanged: EOF still quits, and EOF during an action now cancels and quits
  rather than waiting for the action.
- **A platform with no deadline-capable read** (the `tui_term_other.go` case).
  The escape lookahead falls back to today's behaviour, so `esc` still needs a
  second key there. That is a documented degradation of one key, not a capability
  gate: everything else in this PRD is platform-independent Go.
- **A CLI below the version floor.** `daemon`/`logs` are absent, the read fails
  immediately, and the supervisor block says so — exactly today's behaviour, which
  is why this PRD needs no version test.
- **An action in flight when the change lands.** Nothing persists differently:
  the worker makes the same `Manager` and `Store` calls the browser makes today,
  and the terminal is restored on the way out. A recovery that was interrupted by
  the upgrade is closed as *interrupted* by the manager's existing rule
  (`internal/recovery/manager.go:488-497`).
- **A session parked in `WAITING_FOR_RESET`.** Unaffected. The browser never
  schedules, never wakes a wait, and reads the same rows it reads today.
- **A terminal left in raw mode by an earlier `SIGKILL`.** Not repaired by this
  change — no process can. `docs/cli.md` names `reset` (or `stty sane`) as the
  remedy, next to the new signal handling that makes it unnecessary for every exit
  path but `SIGKILL`.

## 9. Open questions

1. **Is 5 s the right supervisor budget?** The measurement is 0.9–2.5 s for a
   healthy `daemon status` and 30 s for a wedged one, on one machine. A budget
   that is too small turns the block into a permanent "not read"; one that is too
   large makes the page look unfinished. This needs measuring on a loaded machine
   with a real supervisor, not choosing by feel.
2. **Should the browser read the supervisor at all?** The block is the operator's
   most useful diagnostic and it starts below the fold on a page of 21 to 26
   lines. A dedicated key (`S`) that fills it on demand would make Enter instant
   and unconditionally correct, at the cost of one keystroke for the one case that
   needs it. This PRD keeps the block on the page because the page must stay the
   `sessions status` page (R11); a reviewer may prefer the key.
3. **Should a refused key be refused, or queued as one pending request?** R3
   refuses. Queueing one pending detail request would be friendlier and would
   reintroduce exactly the ambiguity RC5 describes, one screen later.
4. **Should movement's `reload` also leave the UI goroutine?** It is a SQLite read
   of ~1 ms, bounded by `storage.busy_timeout` (5 s) under write contention from
   the daemon. Moving it would make `j`/`k` fully non-blocking and would make the
   listing a snapshot the operator can act on while it is stale — a design change,
   not a fix.
5. **Does the real `claude` ever leave a child holding the pipes?** RC3a was
   reproduced with a stand-in. If the real CLI never does, `cmd.WaitDelay` is a
   guard rather than a fix; if it does, it is the difference between a 30-second
   freeze and an unbounded one, and the browser's budget would still be spent on
   a call that cannot return.
6. **What does `SIGHUP` mean here?** A terminal that closes sends it. Restoring
   the screen is pointless on a closed terminal and harmless on a live one; the
   question is whether the browser should also cancel its action, which is what a
   `ccresu daemon` would do.
7. **Should the detail page's scroll keys be documented as a key map row?** The
   list binds `g`/`G` and the detail screen does not, and neither screen says the
   detail page scrolls. R11 makes the footer authoritative; the row itself is a
   wording decision.
