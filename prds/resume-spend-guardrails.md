# PRD: one resume per window bounds how often ccresu resumes, not what a resume costs

| | |
|---|---|
| **Status** | `Proposed` — design only. Nothing here is implemented |
| **Affected version** | `v0.6.0` (`55bf09e`) |
| **Component** | `internal/claude` → `internal/recovery` → `internal/config`, `internal/storage`, `internal/app` |
| **Sources** | <https://code.claude.com/docs/en/cli-reference>, <https://code.claude.com/docs/en/permission-modes>, <https://code.claude.com/docs/en/agent-view> |
| **Owner** | ccresu maintainers |

`permission-modes` is a third source for one reason: the flag that replaces
`--permission-prompts none` on a background handoff is `--permission-mode dontAsk`,
and what `dontAsk` actually denies is defined on that page, not on `cli-reference`.
The other two are the pages the rest of this set is grounded in.

## 1. Summary

ccresu's safety story is a frequency bound. A session is resumed once per usage
window; the reset instant comes from the refusal itself; `MaxAttempts` defaults
to `10` (`internal/config/defaults.go:41`) and a retry backs off. Every one of
those limits counts **attempts**. None of them counts **money**.

The gap is not theoretical. The incident this set was written after recorded
`60` attempts for one session, attempts 22–60 "inside a ~30 minute window"
(`prds/usage-limit-refusal-misreported.md:64-65`), every one of them a real
`claude -p --resume` call. Ten attempts at a few dollars each is a bounded but
real bill, and the attempt budget is *per cycle*: a session returned to
`DISCOVERED` gets "a fresh attempt budget" on the next discovery pass
(`internal/recovery/classify.go:74-90`). So the count bound resets, and the spend
does not.

Two things make this fixable now. The CLI reports the cost of every resume in the
JSON result ccresu already parses — `total_cost_usd`, mapped to
`ResumeOutput.TotalCostUSD` (`internal/claude/cli.go:560`, `:575`) — and ccresu
**throws it away**: there is no cost field on `RecoveryAttempt`
(`internal/storage/models.go:213-234`) and no cost column in `recovery_attempts`
(`internal/storage/migrations/0001_init.sql:53-72`). And the CLI offers flags that
bound spend and length directly.

The honest complication, and the reason this PRD is not simply "pass the flags",
is that **the two flags that bound total cost are print mode only**, and PRD 1
removes print mode from the recovery invocation. `--max-budget-usd` and
`--max-turns` cannot ride a `--bg` handoff. The four flags that constrain *unit*
cost — `--model`, `--effort`, `--fallback-model`, `--permission-mode` — do ride it.
So the design splits: pass the four that survive, and replace the two that do not
with a bound ccresu enforces itself from the cost figure it is already given.

## 2. What an operator sees today

| Observation | Why it happens |
|---|---|
| A nightly loop of recoveries costs real money and nothing in ccresu knows the number | the CLI's `total_cost_usd` is parsed and dropped (`cli.go:560`; no column in `0001_init.sql:53-72`) |
| `max_attempts` looks like a spend bound and is not | it counts attempts, and it resets when the session is rediscovered (`classify.go:74-90`) |
| `ccresu sessions` cannot show what a session has cost | no persisted field exists to show |
| There is no way to say "resume this session cheaply" | the resume vector is fixed: `-p --resume <id> --output-format json <message>` (`cli.go:353-355`) |
| A runaway resumed session cannot be stopped by a budget | the flag that would stop it is print mode only, and PRD 1 retires print mode |
| An operator who wants a hard ceiling has to watch the bill | the ceiling can only be applied after the fact, by hand |

## 3. Impact

- **The invariant protects the wrong quantity.** "One resume per reset window"
  is a rate limit on a resource nobody is billed for. The thing that costs money
  is unbounded, and it is unbounded per *cycle*, not per session.
- **The measurement is free and discarded.** Every resume already returns its
  own price. Persisting it is a column and a write, not a new integration — and
  it turns "what did this cost me" from unanswerable into a `SELECT`.
- **The strongest bound is unavailable by construction.** `--max-budget-usd`
  would stop a runaway turn; PRD 1's handoff cannot carry it. Pretending
  otherwise would be the kind of overpromise this set exists to avoid.
- **Unit cost is still controllable.** `--model` and `--effort` change what a
  turn costs before it runs, and both survive the handoff. For an unattended
  recovery that is the highest-leverage knob available.
- **The unattended-prompt problem has a documented answer that survives.**
  `--permission-prompts none` is print mode only, but `--permission-mode dontAsk`
  "denies" anything that would prompt and carries no print-mode restriction — so
  the posture the brief asks for is achievable, by a different flag.
- **A wrong bound is worse than none.** Mapping a spend stop onto
  `ClassPermanent` would return the session to `DISCOVERED`, which restarts the
  attempt budget and resumes again — the cap would *cause* the spending it exists
  to prevent.

## 4. Evidence

### 4.1 From the documentation

| Fact | Source |
|---|---|
| `--max-budget-usd`: "Stop the run once estimated spend on API calls reaches this amount **(print mode only)**. Claude Code checks the cap against its client-side cost estimate, which can differ from your bill. Spend from subagents counts toward the cap. Spend can pass the cap, so leave headroom. When you return to a conversation with `--continue` or `--resume`, totals restored from earlier runs don't count toward it. Once spend reaches the cap, spawning another subagent fails with `Budget limit reached`… the cap-enforcement behaviors require Claude Code v2.1.217 or later" | `cli-reference` §CLI flags |
| `--max-turns`: "Limit the number of agentic turns **(print mode only)**. No limit by default" | `cli-reference` §CLI flags |
| `--json-schema`: "Get validated JSON output matching a JSON Schema after the agent completes its workflow **(print mode only)**" | `cli-reference` §CLI flags |
| `--permission-prompts`: "Set who answers permission prompts **in print mode**. With the default `host`, Claude Code sends them to the Agent SDK host or the `--permission-prompt-tool` tool. Pass `none` when nobody can answer, and Claude Code denies them instead… Requires Claude Code v2.1.259 or later" | `cli-reference` §CLI flags |
| `--permission-prompt-tool`: "Specify an MCP tool to handle permission prompts **in non-interactive mode**" — **no print-mode restriction**, and a `--bg` handoff *is* non-interactive; Claude Code "waits for that tool's MCP server to connect before running the first turn, up to the `MCP_TIMEOUT` startup timeout, 30 seconds by default", and "The prompt tool can't approve an MCP tool" | `cli-reference` §CLI flags |
| `--model`: "Sets the model for the current session with a model alias such as `sonnet`, `opus`, `haiku`, or `fable`, or a model's full name. Overrides the `model` setting and `ANTHROPIC_MODEL`" — **no print-mode restriction** | `cli-reference` §CLI flags |
| `--effort`: "Set the effort level for the current session. Options: `low`, `medium`, `high`, `xhigh`, `max`, or `ultracode`. Available levels depend on the model… overrides the `modelSettings` and `effortLevel` settings for this session and doesn't persist" — **no print-mode restriction** | `cli-reference` §CLI flags |
| `--fallback-model`: "Enable automatic fallback to the specified model(s) when the primary model is overloaded or not available… Accepts a comma-separated list tried in order" — **no print-mode restriction** | `cli-reference` §CLI flags |
| `--permission-mode`: "Begin in a specified permission mode. Accepts `default`, `acceptEdits`, `plan`, `auto`, `dontAsk`, `bypassPermissions`, or `manual` as an alias for `default`" — **no print-mode restriction** | `cli-reference` §CLI flags |
| `dontAsk` mode: "File reads inside your working directories and pre-approved tools; anything that would prompt is denied, though a mod you installed can still approve some of those calls" — recommended for "Locked-down CI and scripts" | `permission-modes` §modes table |
| "`dontAsk`: never appears in the cycle; set it with `--permission-mode dontAsk`" | `permission-modes` §Allow only pre-approved tools with dontAsk mode |
| "Bypass permissions ignored in a background session": "the background session didn't find your acceptance of the bypass permissions warning in user or managed settings, so it **ignored the bypass request made at launch**" | `permission-modes` §Skip all checks with bypassPermissions mode |
| `--dangerously-skip-permissions`: "For sessions started with `--bg`, the mode persists when the supervisor restarts the session" | `cli-reference` §CLI flags |
| `--bg`: "Start the session as a background agent and return immediately… **Can't be combined with `-p`/`--print`**" | `cli-reference` §CLI flags |
| `claude agents` accepts "`--permission-mode`, `--model`, `--effort`, or `--agent` to set defaults for dispatched sessions" — a *different* surface from a `--bg` handoff | `cli-reference` §CLI commands |
| Backgrounding a session mid-workflow is a cost event: "When Claude relaunches the workflow, any of its subagents that were still running when you backgrounded the session **start over and spend their tokens again**" | `agent-view` |
| `agent-view` contains **no** occurrence of "budget" or "usd", and no spend cap of any kind | `agent-view` (full text searched) |

Two rows carry the argument. The print-mode restriction on `--max-budget-usd` and
`--max-turns` is explicit and repeated, and `agent-view` — the page that defines
the background-session surface PRD 1 hands off to — offers nothing that replaces
them. There is no documented way to cap what a background session spends.

### 4.2 From this repository

| Fact | Where |
|---|---|
| The resume vector is five fixed elements with no guardrail flag | `internal/claude/cli.go:353-355` (`ResumeArgs`) |
| The CLI reports the cost of every resume | `internal/claude/cli.go:560` (`TotalCostUSD`), `:575` (`Cost flexFloat` on `total_cost_usd`), set at `:323` and `:760` |
| Nothing reads it: no cost field on the attempt model | `internal/storage/models.go:213-234` (`RecoveryAttempt`) |
| Nothing stores it: no cost column in the schema | `internal/storage/migrations/0001_init.sql:53-72` |
| Five failure classes, none meaning "stopped for spend" | `internal/recovery/classify.go:15-44` |
| `ClassPermanent` returns to `DISCOVERED`, and "the next cycle starts a fresh attempt budget" | `internal/recovery/classify.go:74-90` (`Class.State`) |
| `UNRECOVERABLE` is defined as "a fact about the session, not a budget" | `internal/recovery/state.go:68-72` |
| Automatic recovery is driven by an explicit state list; a state not in it is never resumed | `internal/recovery/state.go:240-250` (`PendingStates`), `internal/app/daemon.go:478-481` |
| The attempt budget default is 10 | `internal/config/defaults.go:41`; struct at `internal/config/config.go:94-105` |
| A resume is bounded in wall-clock time at 15 minutes | `internal/claude/cli.go:20-22` (`DefaultResumeTimeout`) |
| A failure's text reaches the message body, human-readable | `internal/telegram/template.go:50-51` (`EventData.Reason`), set from the cause at `internal/app/notifications.go:91` (`milestoneData`) |
| The permanent-failure milestone already means "automatic recovery stopped" | `internal/recovery/observe.go:41`; subject at `internal/telegram/template.go:71-72` |
| The hot-retry incident: 60 attempts, 22–60 inside ~30 minutes | `prds/usage-limit-refusal-misreported.md:17`, `:64-65` |
| ccresu runs unattended under systemd | `deployments/systemd/ccresu.service` |

## 5. Why it happens — the causal chain

### RC1 — The invariant counts attempts, and the count resets (`internal/recovery/classify.go:74-90`)

`MaxAttempts` bounds one cycle. A permanent failure returns the session to
`DISCOVERED`, and rediscovery starts a fresh budget:

```go
default:
	// Permanent: repeating on this schedule cannot help. The session is
	// rediscovered rather than abandoned, and the next cycle starts a fresh
	// attempt budget.
	return StateDiscovered
```

So "at most ten attempts" is "at most ten attempts per discovery cycle", and a
session that keeps failing keeps being rediscovered. The incident's 60 attempts
are what that looks like when the limit window is short. The count is a rate
limit; it is not a ceiling on anything an operator is billed for.

### RC2 — The cost is measured and discarded (`internal/claude/cli.go:560`, `internal/storage/models.go:213-234`)

ccresu parses the result object and maps `total_cost_usd`:

```go
TotalCostUSD: chosen.Cost.Float(),
```

and then keeps it nowhere. `RecoveryAttempt` has fields for the command, the
output, the error, whether the session id was verified and whether the transcript
grew — everything except what it cost. `recovery_attempts` has no cost column
(`0001_init.sql:53-72`). The figure is in memory for the duration of one function
call and is gone.

This is the single most important repository fact in this PRD: the spend bound
this PRD proposes needs no new measurement, only a place to put the measurement
ccresu already takes.

### RC3 — The invocation is fixed, so no flag can reach it (`internal/claude/cli.go:353-355`)

```go
func ResumeArgs(sessionID, message string) []string {
	return []string{"-p", "--resume", sessionID, "--output-format", "json", message}
}
```

There is no options struct, no configuration lookup, and no place a guardrail flag
could be appended. Every resume is byte-identical in shape regardless of what the
operator wants to spend.

### RC4 — The two flags that bound total cost cannot ride the handoff (`cli-reference` §CLI flags)

`--max-budget-usd` and `--max-turns` are both documented "(print mode only)".
PRD 1 replaces the print-mode invocation with `claude --resume <id> --bg <message>`,
and `--bg` "Can't be combined with `-p`/`--print`". So the flags and the handoff
are mutually exclusive by documentation, not by preference. A PRD that specified
them as the guardrails would specify flags that cannot be passed.

The asymmetry is what makes the split possible rather than fatal: `--model`,
`--effort`, `--fallback-model` and `--permission-mode` carry no print-mode
restriction, so the *unit* cost of a resume is still controllable on a background
handoff even though the *total* is not.

### RC5 — No class or state can express "stopped for spend" (`internal/recovery/state.go:68-72`)

The state machine is explicit that the only state which stops automatic work is
`UNRECOVERABLE`, and equally explicit about why:

```go
// The only state that stops automatic work on its own is UNRECOVERABLE, and only
// because ccresu has positively established that it has no verified way to
// continue the conversation. That is a fact about the session, not a budget.
```

So a spend stop cannot be `UNRECOVERABLE` without contradicting the state
machine's own stated principle. It cannot be `ClassPermanent` either, because that
returns to `DISCOVERED` and re-spends (RC1). There is no existing vocabulary for
"this session is fine and the conversation is fine, but ccresu will not spend more
on it until a human says so" — which is precisely the outcome a guardrail needs.

### RC6 — An unattended resume that meets a permission prompt has nowhere to send it (`cli-reference` §CLI flags)

`--permission-prompts none` exists for exactly this: "Pass `none` when nobody can
answer, and Claude Code denies them instead." It is print mode only. On a
background handoff a session that hits a prompt has no `none` to fall back on, so
it waits — it becomes `blocked` with `waitingFor: permission prompt` (PRD 2's
vocabulary), and without PRD 2 and PRD 4 that wait is invisible to the operator.
The answer is not to accept the block: `--permission-mode dontAsk` denies anything
that would prompt and survives the handoff.

`--permission-prompt-tool` is the other documented route, and it is the one flag
that *answers* rather than denies. It is scoped to "non-interactive mode", not to
print mode, so unlike `--max-budget-usd` it can ride a handoff — but it needs an
operator-run MCP server implementing the prompt contract, and the CLI blocks the
first turn for up to `MCP_TIMEOUT` waiting for that server to connect. ccresu
cannot assume such a server exists, so `dontAsk` is the default answer and the
tool is recorded as an operator option with its own open question (§9.6).

### The resulting shape

```
config: recovery.guardrails
   ├─ model / effort / fallback_model / permission_mode ──► ResumeArgs ──► --bg handoff
   │                                                        (survives: no print-mode restriction)
   └─ max_cost_usd_per_session / max_cost_usd_per_attempt
            │
            ├─ before the attempt: persisted spend ≥ cap ──► refuse, spend nothing
            └─ after the attempt:  recorded cost  ──► attempt row gets a cost column
                                                       │
                                     over cap ─────────┘
                                          └─► ClassSpendCapReached ─► SPEND_CAP_REACHED
                                              (not in PendingStates: no pass resumes it)
                                              └─► MilestonePermanent ─► permanent_failure
```

The left branch is delegation; the right branch is ccresu's own bound, because
the supervisor offers none.

## 6. Requirements

| # | Requirement |
|---|---|
| **R1** | A guardrail set exists under `recovery.guardrails`. Every knob is optional, and an unset knob means today's behaviour exactly: no flag added, no check performed. |
| **R2** | The four flags with no print-mode restriction — `--model`, `--effort`, `--fallback-model`, `--permission-mode` — are appended to the resume vector when configured, and **omitted entirely** when not. An empty value never becomes an empty flag argument. |
| **R3** | The four print-mode-only flags — `--max-budget-usd`, `--max-turns`, `--json-schema`, `--permission-prompts` — are never passed on a handoff, and neither is `--print`/`-p`. A test asserts `ResumeArgs` cannot emit any of them, so a future edit cannot reintroduce the mistake. `--permission-prompt-tool` is **not** on that list: the docs scope it to "non-interactive mode", which a handoff is, so nothing print-mode-related forbids it — though whether the `--bg` parser accepts it is undocumented (§9.6). ccresu does not pass it because it requires an operator-run MCP prompt server, which §9.6 records as a question rather than a settled refusal. |
| **R4** | The cost of every attempt is persisted: a `cost_usd` column on `recovery_attempts`, a field on `RecoveryAttempt`, written from `ResumeOutput.TotalCostUSD` when the attempt resolves. This is the measurement ccresu already takes (`cli.go:560`). |
| **R5** | A session-level spend cap: before an attempt is started, ccresu sums the session's recorded attempt costs and refuses to start when the sum is at or above `max_cost_usd_per_session`. The check runs **before** `StartAttempt`, so a refusal spends nothing — consistent with the existing rule that a request which spends no attempt has started nothing (`observe.go:14-18`). |
| **R6** | A per-attempt spend cap: when an attempt resolves with a recorded cost above `max_cost_usd_per_attempt`, automatic recovery for that session stops under the same named outcome. |
| **R7** | Exceeding either cap produces a distinct, named outcome, never a generic failure: a new `ClassSpendCapReached`, a new `StateSpendCapReached`, and an error whose text names the knob, the configured cap and the observed spend. That text reaches the operator through `EventData.Reason` (`template.go:50-51`). |
| **R8** | `StateSpendCapReached` is **not** in `PendingStates()` (`state.go:240-250`), so no reconcile pass resumes the session and the cap cannot be defeated by rediscovery. It is reachable from the states where a spend decision can be made, and leaves only by an explicit operator action (`ccresu recover`, raising the cap, or `ccresu reset`). |
| **R9** | The spend stop is announced through the existing `MilestonePermanent` → `permanent_failure` path, whose subject is already "Automatic recovery stopped". **No new milestone and no new notification event**, so PRD 4's §6.2 mapping stays complete and this PRD does not extend the event vocabulary. |
| **R10** | A missing or unparseable cost never blocks a resume. `total_cost_usd` is optional in the CLI's result; an absent value is recorded as *unknown*, not as zero, and unknown attempts do not count toward the cap. The cap therefore cannot silently stop recovery because of a parse gap — and the PRD states plainly that an attempt with unknown cost is also ungoverned. |
| **R11** | The caps apply to **automatic** recovery. An explicit `ccresu recover <id>` is a human act and is allowed past the cap, but its cost is still recorded and the refusal it overrode is logged. The cap exists to replace a human who is not there; it must not bind one who is. |
| **R12** | Both caps default to **off** (`0`), so an upgrade changes no behaviour, and `configs/config.example.yaml` ships a commented recommendation rather than an active cap. A default that silently stopped recovery on upgrade would be the behaviour change `prds/README.md` §4 forbids. |

### 6.1 Each flag, and whether it survives the handoff

This table is the answer to PRD 1's open question 2. It is deliberately about the
*flag*, not about the intent behind it: of the nine flags this PRD names, four can
ride a `--bg` handoff, four cannot, and the ninth is neither forbidden nor used.
No amount of wanting it changes which column a flag falls in. One cell is an
inference rather than a quotation — whether a `--bg` handoff *accepts*
`--permission-prompt-tool` is not documented for `--bg` specifically — and it is
marked as such rather than presented as settled.

| Flag | Print-mode restricted? | Available on a `--bg` handoff | Evidence | What ccresu does |
|---|---|---|---|---|
| `--model` | no | **yes** | `cli-reference`: "Sets the model for the current session… Overrides the `model` setting and `ANTHROPIC_MODEL`" — no restriction stated | passes it when configured (R2) |
| `--effort` | no | **yes** | `cli-reference`: "Set the effort level for the current session… doesn't persist" — no restriction stated | passes it when configured (R2) |
| `--fallback-model` | no | **yes** | `cli-reference`: "Enable automatic fallback to the specified model(s)… Accepts a comma-separated list tried in order" — no restriction stated | passes it when configured (R2) |
| `--permission-mode` | no | **yes, with one documented exception** | `cli-reference`: accepts `dontAsk`, `bypassPermissions`, … no restriction; but `permission-modes`: "Bypass permissions ignored in a background session" — the session "ignored the bypass request made at launch" unless the acceptance is in user or managed settings | passes it when configured; **warns** if the value is `bypassPermissions`, because on a handoff it may be silently ignored |
| `--max-budget-usd` | **yes** — "print mode only" | **no** | `cli-reference` §CLI flags | **not passed.** Replaced by ccresu's own session cap (R5) |
| `--max-turns` | **yes** — "print mode only" | **no** | `cli-reference` §CLI flags | **not passed.** Replaced by the wall-clock and attempt bounds in §6.3 |
| `--permission-prompts` | **yes** — "in print mode" | **no** | `cli-reference` §CLI flags; requires v2.1.259+ | **not passed.** Replaced by `--permission-mode dontAsk` (§6.3) |
| `--json-schema` | **yes** — "print mode only" | **no** | `cli-reference` §CLI flags | **not passed.** It was never a spend guardrail — it constrains output shape, not cost — and ccresu does not need structured output from a handoff |
| `--permission-prompt-tool` | no — "in non-interactive mode" | **probably** — scoped to "non-interactive mode", which a handoff is; not documented for `--bg` itself, so the cell is an inference | `cli-reference` §CLI flags: "Specify an MCP tool to handle permission prompts in non-interactive mode"; no print-mode restriction | **not passed by default.** It needs an MCP server implementing the prompt contract and blocks the first turn up to `MCP_TIMEOUT`; `dontAsk` needs no infrastructure (§6.3, §9.6) |

The `--permission-mode bypassPermissions` row is the subtle one and the reason the
table is per-flag rather than per-family. "No print-mode restriction" is not the
same as "works on a background session": `bypassPermissions` survives the *flag*
check and is then ignored by the *supervisor* unless the operator has accepted the
bypass warning in user or managed settings. A PRD that had reasoned from
"`--permission-mode` is unrestricted" alone would have promised a mode that a
background session silently drops.

### 6.2 The config surface

| Knob | yaml | Default | How it takes effect | When unset |
|---|---|---|---|---|
| Session spend cap | `recovery.guardrails.max_cost_usd_per_session` | `0` (off) | ccresu-side check before `StartAttempt` (R5) | no session cap; every attempt proceeds as today |
| Attempt spend cap | `recovery.guardrails.max_cost_usd_per_attempt` | `0` (off) | ccresu-side check when the attempt resolves (R6) | no per-attempt cap |
| Model | `recovery.guardrails.model` | `""` | `--model <value>` on the handoff | flag omitted; the CLI's own configured model |
| Effort | `recovery.guardrails.effort` | `""` | `--effort <value>` | flag omitted; the CLI's own effort setting |
| Fallback chain | `recovery.guardrails.fallback_model` | `""` | `--fallback-model <value>` | flag omitted; no fallback |
| Permission mode | `recovery.guardrails.permission_mode` | `""` | `--permission-mode <value>` | flag omitted; the CLI's configured default mode |

Two defaults are judgement calls and are flagged as open questions rather than
presented as settled. The spend caps default to `0` because a non-zero default
would stop recovery on an upgrade nobody asked to change (R12, and
`prds/README.md` §4). The permission mode defaults to unset because changing it
changes what a resumed session is allowed to do; the *recommended* value is
`dontAsk`, and the example config says so in a comment.

### 6.3 What replaces the bounds that cannot ride the handoff

| Lost flag | What it did | Replacement | Honest limitation |
|---|---|---|---|
| `--max-budget-usd` | stop a *run* once estimated spend reached the cap | ccresu's persisted-spend session cap (R5, R6): sum the recorded attempt costs, refuse the next attempt at the cap | bounds the **series across attempts**, not a single turn. A single background turn that spends heavily cannot be stopped by ccresu, because the cap flag cannot ride the handoff. This is the real hole, and it is stated rather than papered over |
| `--max-turns` | stop a run after N agentic turns | the existing 15-minute wall clock (`DefaultResumeTimeout`, `cli.go:20-22`) plus the per-cycle attempt budget | turns are not wall-clock: a fast loop of cheap turns fits inside 15 minutes. The attempt budget bounds how many *resumes*, not how much one resume does |
| `--permission-prompts none` | deny prompts nobody can answer | `--permission-mode dontAsk`, which "denies" anything that would prompt and carries no print-mode restriction | `dontAsk` still allows "file reads inside your working directories and pre-approved tools", and "a mod you installed can still approve some of those calls" — so it is a narrower denial than a blanket `none`, not an identical one. `--permission-prompt-tool` would *answer* instead of denying and is permitted on a handoff, but it needs an operator-run MCP server, so it is an option rather than the replacement (§9.6) |
| `--json-schema` | constrain output shape | nothing; not needed | not a cost bound, so nothing is lost |

The budget caveats the docs give apply to the flag ccresu cannot use, and they
apply equally to the bound ccresu builds, so they are carried into the design
rather than dropped:

- The CLI's cap is checked against a **client-side estimate** "which can differ
  from your bill". ccresu's cap sums the CLI's own reported `total_cost_usd`,
  which is the same estimate — so ccresu's number is as approximate as the CLI's,
  and the PRD does not claim otherwise.
- "Spend can pass the cap, so leave headroom." A cap of `$5` can end at `$5.40`.
  The example config documents this.
- "Spend from subagents counts toward the cap" — so a session that fans out is
  counted, which is the case an operator most needs counted.
- "When you return to a conversation with `--continue` or `--resume`, totals
  restored from earlier runs don't count toward it." This one cuts the other way
  for ccresu: because ccresu resumes, a *single attempt's* reported cost covers
  that attempt, and the running total must be **ccresu's own sum of persisted
  attempts**, not a figure read back from the CLI. R5 sums the database, for
  exactly this reason.
- "the cap-enforcement behaviors require Claude Code v2.1.217 or later" — moot
  here, since ccresu does not use the flag; the replacement is version-independent.

One further documented cost fact is worth recording because it is a hazard of the
handoff in general: "When Claude relaunches the workflow, any of its subagents that
were still running when you backgrounded the session start over and spend their
tokens again" (`agent-view`). It does **not** apply to ccresu's path — ccresu
resumes a session that has already stopped, so there are no in-flight subagents to
restart — and it is listed here so a reviewer can see it was considered rather than
missed.

### 6.4 Failure modes

| Failure | What ccresu observes | What it must do |
|---|---|---|
| **Cap reached before an attempt** | persisted spend ≥ `max_cost_usd_per_session` | refuse before `StartAttempt`; `ClassSpendCapReached`, `SPEND_CAP_REACHED`, one `permanent_failure` naming knob, cap and spend. Nothing is spent |
| **Cap exceeded by an attempt** | the attempt's recorded cost > `max_cost_usd_per_attempt` | record the cost, then stop recovery under the same named outcome |
| **Cost absent from the result** | `total_cost_usd` missing or unparseable | record *unknown*, do not count it toward the cap, do not block the resume (R10) |
| **Cost present but absurd** | a negative or non-finite value | record unknown; never let a bad number move a session past or below a cap |
| **Both caps configured** | the stricter one binds | apply both; the per-attempt check is a post-hoc guard for the case the session cap cannot see (one very expensive turn) |
| **Cap raised while a session is parked** | the operator edits the config | the next pass re-evaluates: a session in `SPEND_CAP_REACHED` with room under the cap returns to `DISCOVERED`. No restart required |
| **`bypassPermissions` configured for a handoff** | the flag is passed but may be ignored by the supervisor | warn at configuration load, not at resume time: the operator learns once, not once per attempt |
| **`dontAsk` denies the work the session needed** | the attempt fails with denials in its output | a retryable failure like any other: the attempt budget and backoff apply. It is not a guardrail stop |
| **Guardrails unset** | every knob empty or zero | byte-for-byte today's behaviour: no flags, no checks, no new state |

### Acceptance criteria

- `TestResumeArgsNeverCarriesAPrintModeFlag` asserts the vector produced from a
  fully populated guardrail set contains `--model`, `--effort`,
  `--fallback-model` and `--permission-mode`, and contains **none** of
  `--max-budget-usd`, `--max-turns`, `--json-schema`, `--permission-prompts` —
  the four print-mode-only flags — or `--print`/`-p`. A second assertion pins
  `--permission-prompt-tool` as absent too, and its comment says why: not because
  the docs forbid it on a handoff (§6.1 says they do not) but because ccresu
  configures no MCP prompt server (§9.6), so the test fails if someone adds one
  without revisiting that question.
- `TestUnsetGuardrailsProduceTheUnguardedVector` asserts an empty guardrail set
  produces exactly the vector the handoff builds when no guardrail is configured —
  element for element, so an unset knob adds nothing and removes nothing. It
  asserts against the post-PRD-1 handoff vector, not against `v0.6.0`'s print-mode
  vector, because PRD 1 owns that change and this PRD must not appear to revert it.
- `TestCostIsPersistedPerAttempt` asserts a resume whose result reports
  `total_cost_usd: 0.42` leaves `0.42` on the attempt row, and that an absent
  value leaves *unknown* rather than `0`.
- `TestSessionCapRefusesBeforeSpending` seeds attempts summing to the cap, runs a
  recovery, and asserts no new attempt row was created — the refusal spends
  nothing.
- `TestSpendStopIsNotRediscovered` asserts a session in `SPEND_CAP_REACHED` is
  absent from `PendingStates()` and that a full daemon pass leaves it untouched.
- `TestSpendStopHasANamedOutcome` asserts the resulting class is
  `ClassSpendCapReached` (not `ClassPermanent`), the state is `SPEND_CAP_REACHED`,
  and the rendered message body names the knob, the cap and the observed spend.
- `TestSpendStopAnnouncesPermanentFailure` asserts exactly one
  `permanent_failure` notification is queued with a dedupe key containing the
  attempt id, and that no new event name was introduced.
- `TestUnknownCostDoesNotBlockResume` asserts a result with no
  `total_cost_usd` still resumes and records the attempt.
- `TestManualRecoverIsExemptFromTheCap` asserts `ccresu recover <id>` proceeds
  past a reached cap, records the cost, and logs that it overrode the cap.

## 7. Non-goals

- **A per-turn spend cap.** Not implementable on a handoff: `--max-budget-usd` is
  print mode only and `agent-view` offers no equivalent. §6.3 states the hole
  instead of hiding it.
- **Predicting cost before a resume.** ccresu has no model of token prices, and a
  prediction that disagreed with the bill would be worse than no prediction.
- **A new notification event or milestone.** The spend stop uses
  `MilestonePermanent` → `permanent_failure`, whose subject is already "Automatic
  recovery stopped" (R9). A dedicated event would be PRD 4's table to extend, and
  this PRD does not extend it.
- **Reusing `UNRECOVERABLE`.** The state machine defines it as a fact about the
  session, "not a budget" (`state.go:68-72`).
- **Reusing `ClassPermanent`.** It returns to `DISCOVERED`, which restarts the
  attempt budget and resumes again — the cap would cause the spending it prevents.
- **Enforcing caps with `--bg --exec` or a wrapper process.** ccresu is not a
  process supervisor; PRD 3 delegates lifecycle to the supervisor rather than
  racing it.
- **Retroactive cost accounting for attempts already recorded.** `v0.6.0`
  attempts have no cost, and back-filling from transcripts is guesswork.
- **The agent-view dispatch defaults** (`claude agents --model/--effort`). That
  surface configures sessions *dispatched from agent view*; ccresu hands off from
  the CLI and does not use it.
- **Worktree cwd resolution.** PRD 6.

## 8. Proposed shape

| Step | Change | Satisfies |
|---|---|---|
| 1 | Add `Recovery.Guardrails` to the config struct with the six knobs, their defaults and env overrides | R1, R12, §6.2 |
| 2 | Turn `ResumeArgs` into a function of the guardrail set, appending only the four surviving flags and only when set | R2, R3 |
| 3 | Warn at config load when `permission_mode` is `bypassPermissions`, citing the background-session exception | §6.1 |
| 4 | Add `cost_usd` to `recovery_attempts` (migration) and `CostUSD *float64` to `RecoveryAttempt`, written from `ResumeOutput.TotalCostUSD` | R4 |
| 5 | Add `ClassSpendCapReached` and `StateSpendCapReached`; add the transitions and keep the new state out of `PendingStates()` | R7, R8 |
| 6 | Add the pre-attempt session check and the post-attempt per-attempt check, both producing the named outcome | R5, R6 |
| 7 | Route the named outcome through `MilestonePermanent` and assert the rendered body names the cap | R7, R9 |
| 8 | Document the knobs and the recommended `dontAsk` posture in `configs/config.example.yaml` | R12, §6.3 |

Step 2 is the one that must be reviewed against §6.1 rather than against intent:
the temptation is to pass `--max-budget-usd` "because it is the obvious flag",
and it is the one flag that cannot be passed.

### Migration

- **First run with no config change.** Identical to today. All six knobs unset,
  so no flag is added, no check runs, no new state is reachable. Existing attempts
  have no cost, and R10's *unknown* is the honest value for them — nothing is
  back-filled.
- **First run after an operator sets the caps.** The next attempt for a session
  already over the cap is refused before it spends anything, and the session moves
  to `SPEND_CAP_REACHED`. A session mid-attempt when the change lands is not
  interrupted: the pre-attempt check runs before `StartAttempt`, so it cannot touch
  one already running, and PRD 1's R13 leaves an attempt with a recorded handoff
  open rather than closing it at startup.
- **A session parked in `WAITING_FOR_RESET` when the change lands.** Unaffected.
  The scheduler owns that state, and the spend check runs when the attempt would
  start, not while the session waits.
- **An installation whose CLI predates the guardrail flags.** `--model`,
  `--effort`, `--fallback-model` and `--permission-mode` are long-standing flags;
  `dontAsk` is the newest named value and `manual` requires v2.1.200. An older CLI
  that rejects a value fails the attempt as a retryable error, and the operator
  sees a named flag error rather than silence. ccresu does not gate on version
  here, because the failure is legible and the alternative is a version table this
  set deliberately does not maintain (the withdrawn capability-gating PRD). PRD 1's
  floor (`2.1.257`) is newer than every flag this PRD passes, so this is a guard
  rather than a supported configuration; what stays real is a *value* a given
  release does not know.
- **An installation with a non-zero cap and a CLI that omits `total_cost_usd`.**
  Every attempt records *unknown*, so the cap never binds and never falsely
  refuses. The operator gets an ungoverned session rather than a broken one, which
  is the failure direction R10 chooses on purpose.

## 9. Open questions

1. **Should the session cap default to on?** This PRD says off, because a cap that
   stopped recovery on upgrade would change behaviour without being asked for. The
   counter-argument is that an unset cap protects nobody, and the operator who
   needs it most is the one who will not read the changelog. A defensible middle
   is a very high default that only a runaway could reach — but "very high" is a
   number nobody has measured, so it is left as a question rather than invented.
2. **What is a realistic default value for the caps?** Answering it needs the
   distribution of per-attempt cost, which is exactly what R4 starts recording.
   This is the one question the first release of the feature answers for itself.
3. **Should `--permission-mode dontAsk` be the default rather than a
   recommendation?** `dontAsk` denies what nobody can answer, which is the right
   posture for an unattended resume — but it also denies work the session may have
   needed, turning a would-be success into a retryable failure. The recommendation
   is honest; whether it should be the default is a judgement about how often a
   recovered session legitimately needs a prompt.
4. **Should the spend stop be its own event after all?** R9 reuses
   `permanent_failure` so PRD 4's mapping stays complete, and the message names the
   guardrail. But an operator who wants to page on spend separately from paging on
   a broken binary would need a distinct event, and that is PRD 4's table to
   extend. Settle it when someone asks to filter on it.
5. **Does `total_cost_usd` on a `--bg` handoff even get reported?** The field is
   documented for `-p --output-format json`. A `--bg` handoff returns immediately
   and prints a session block, not a result object — so the cost of a background
   attempt may have to be read from `claude logs <id>` (PRD 3) or from
   `claude agents --json` (PRD 2), neither of which is documented to carry cost.
   **This is the load-bearing uncertainty of the whole PRD**: if no supported read
   path exposes per-attempt cost for a background session, R4 has no source and
   R5/R6 have nothing to sum. It must be settled by experiment before
   implementation, and it is the first thing to check.
6. **Should ccresu support `--permission-prompt-tool`?** It is the one documented
   way to *answer* a prompt on a non-interactive session, and the docs do not
   restrict it to print mode — so unlike `--max-budget-usd` it is available on the
   handoff. Two things are unverified: whether `--bg` accepts the flag at all (the
   docs scope it to "non-interactive mode" and never mention `--bg`), and what a
   missing or slow prompt server costs the first turn — the CLI waits up to
   `MCP_TIMEOUT`, 30s by default. The cost of using it is infrastructure: the
   operator must run an MCP server implementing the prompt contract. `dontAsk`
   needs nothing and denies instead of answering. Left open with a stated fallback: until it is
   answered, `--permission-mode dontAsk` is what ccresu recommends and
   `--permission-prompt-tool` is never passed (§6.1, §6.3).
