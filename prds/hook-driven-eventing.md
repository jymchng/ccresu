# PRD: the notification hook is a wake-up call, and ccresu should stop polling to hear it

| | |
|---|---|
| **Status** | `Proposed` — design only. Nothing here is implemented |
| **Affected version** | `v0.6.0` (`55bf09e`) |
| **Component** | `cmd/ccresu` → `internal/app` → `internal/recovery`, `internal/telegram`, `internal/storage`, `internal/config` |
| **Sources** | <https://code.claude.com/docs/en/agent-view>, <https://code.claude.com/docs/en/hooks>, <https://code.claude.com/docs/en/cli-reference> |
| **Owner** | ccresu maintainers |

The third source needs a word of explanation, because the rest of this set is
grounded in two. `agent-view` states *that* Claude Code fires the `Notification`
hook with type `agent_needs_input` or `agent_completed` for background sessions.
The hook's **input contract, exit-code semantics and configuration shape** are
documented on `hooks`, not on either of the other two pages. This PRD could not
specify a subcommand that consumes that contract without citing the page that
defines it, so it cites it — rather than paraphrasing a contract it cannot
source.

## 1. Summary

`ccresu` learns that a session needs attention in exactly one way: a timer
fires, the daemon re-reads the world, and something in a transcript has changed.
The timer is `DefaultDaemonInterval = 30 * time.Second`
(`internal/app/daemon.go:20-27`), and the pass that follows it runs six stages in
order — discover, detect, schedule, recover, complete, deliver
(`daemon.go:135-141`). Every one of those stages answers its question by
reading. So the latency of "this session needs you" is up to one interval plus a
transcript scan, and the answer itself is inferred from a file rather than read
from a state.

Claude Code already announces the same two facts, and announces them at the
moment they happen. While agent view is open it "sends a notification through
your configured terminal notification channel when a local background session
starts needing your input, finishes, or fails" and "fire[s] the `Notification`
hook with the `agent_needs_input` or `agent_completed` type". A `Notification`
hook is a command Claude Code runs and hands a JSON object on stdin, containing
at least `session_id`, `cwd`, `hook_event_name` and `notification_type`.

The proposal is a `ccresu hook` subcommand that Claude Code invokes as that
hook. It is **not** a second notifier. It reads the payload, performs **one**
observation of the session it names, and hands the result to the milestone
producer that already exists — the same one the poll uses
(`internal/app/notifications.go:31`). The hook decides *when to look*, never
*what is true*, so a hook observation and a poll observation of the same moment
cannot disagree, and cannot both notify.

Two repository facts make that the only defensible design. The first is that
PRD 2's `MilestoneNeedsInput` has **no destination**: `notificationEvent`
(`notifications.go:70-84`) ends in `default: return "", false`, so a milestone
it does not know is silently dropped, and none of the seven events in
`internal/storage/models.go:123-134` means "a session is waiting on a human".
The second is that the dedupe key gives per-episode identity only to limits and
attempts (`internal/telegram/template.go:177-198`), so the obvious mapping —
`agent_completed` onto `session_completed` — would announce a session once and
never again.

So the proposal answers PRD 2's delegation instead of restating it. PRD 2 defines
`MilestoneNeedsInput` and says the mapping is this PRD's (§6.2 restates the
boundary); this PRD names the event it becomes — `needs_input`, an eighth event
wired through the twelve sites RC3 lists — and §6.2 maps **every** milestone kind
PRD 2 can emit, so no requirement there terminates in a dropped value and no
milestone reaches an event this document does not define.

And one documented limit governs the whole design: `agent_completed` "Fires only
while agent view is open in a terminal", and `agent_needs_input` likewise fires
"while agent view is open". `ccresu` is deployed as a systemd service with no
terminal at all. The hook is therefore unavailable **exactly where ccresu runs**,
which makes it an accelerator and never the only path: polling stays, and a
missing or broken hook changes nothing.

## 2. What an operator sees today

| Observation | Why it happens |
|---|---|
| A background session that asked a question sits unanswered until the next pass | the only discovery loop is a 30-second timer (`daemon.go:20-27`) |
| An operator who turns on every notification switch still hears nothing when a session blocks | none of the seven events means "a session needs a human" (`models.go:123-134`) |
| `notify_waiting` looks like the switch for this and is not | it means a usage window has not reset yet, which is a wait with a known end |
| Claude Code fires a `Notification` hook for the same two moments, and nothing subscribes | ccresu installs no hook and has no subcommand shaped for one |
| No `ccresu` command is safe to call from another program's event pipeline | every command reports failure on stderr and exits non-zero (`app.go:214-228`) |
| A session that finishes a turn is indistinguishable from one that has settled | `session_completed` is the only completion-shaped event, and it is once per session, ever |

The fourth row is the opportunity and the fifth is the obstacle: the signal
already exists, and ccresu's CLI is shaped entirely for humans.

## 3. Impact

- **The signal is late by construction.** Thirty seconds is the floor, not the
  worst case: a session that blocks just after a pass waits the whole interval,
  and the pass that follows still has to read a transcript to decide anything.
- **The signal is inferred, which is the class of defect PRD 2 retires.** "This
  session needs you" is currently derived from transcript contents, by the same
  kind of shortcut that read a refused resume's own write as its success.
- **The operator's switches cannot express the condition.** Seven events, seven
  config keys, and no way to ask to be told that a session is waiting on a human.
  Turning everything on does not produce that message.
- **PRD 2's requirement currently terminates in a dropped value.** R12 of
  `authoritative-session-state.md` requires `MilestoneNeedsInput`; the mapping it
  would flow into returns `("", false)` for any kind it does not list, and its
  caller treats that as "nothing to say" (`notifications.go:40-42`).
- **The obvious fix is a trap.** Mapping completion onto `session_completed`
  inherits a dedupe key of `session_completed:<session id>`
  (`daemon.go:500-502`), which is deliberately once per session — so a
  background session that finishes five turns would be announced once, forever.
- **A second notifier would be the wrong answer.** The queue, the dedupe
  constraint, the retry budget and the delivery loop all exist and work
  (`internal/storage/notifications.go`, `internal/telegram/notifier.go`). The
  hook needs to feed them, not parallel them.

## 4. Evidence

### 4.1 From the documentation

| Fact | Source |
|---|---|
| The `Notification` hook "Runs when Claude Code sends notifications. Matches on notification type. Omit the matcher to run hooks for all notification types" | `hooks` §Notification |
| `agent_needs_input`: "A background session starts waiting on your input while agent view is open in a terminal. Also fires when a terminal session shows you an agent team teammate's terminal setup question or auto mode's notice about classifier request charges and you haven't typed for about six seconds" | `hooks` §Notification |
| `agent_completed`: "A background session finishes or fails. Fires only while agent view is open in a terminal" | `hooks` §Notification |
| `agent_needs_input` for a teammate's terminal setup question requires Claude Code v2.1.248 or later | `hooks` §Notification |
| A `Notification` hook receives the common fields plus "`message` with the notification text, an optional `title`, and `notification_type` indicating which type fired" | `hooks` §Notification input |
| Common fields are `session_id`, `prompt_id`, `transcript_path`, `cwd`, `scratchpad_dir`, `permission_mode`, `effort`, `hook_event_name`; "For command hooks, this JSON arrives via stdin" | `hooks` §Common input fields |
| "Notification hooks can't block or modify notifications. Claude Code discards their `systemMessage` and `continue` fields… Notification hooks are intended for side effects such as forwarding the notification to an external service" | `hooks` §Notification input |
| Exit code 2 on `Notification`: **can block? No** — "Exit code and stderr are ignored" | `hooks` §Exit code 2 behavior per event |
| "You receive these hook events even with desktop notifications turned off: the `preferredNotifChannel` setting, including `notifications_disabled`, changes only how you're alerted, not whether your hook runs" | `hooks` §Notification |
| A command hook's `timeout` defaults to **600 seconds** | `hooks` §Common fields |
| Hook configuration is `{"hooks": {"Notification": [{"matcher": …, "hooks": [{"type": "command", "command": …}]}]}}` | `hooks` §Notification |
| "When the script path doesn't exist or isn't executable, the shell exits with a code like 127… a hook that can't start is a non-blocking error too" | `hooks` §Troubleshooting |
| "While agent view is open, Claude Code also sends a notification… when a local background session starts needing your input, finishes, or fails. Sessions that run on a schedule, such as `/loop` sessions, notify only when they need your input. Notifications use the same `preferredNotifChannel` setting as the rest of Claude Code and fire the `Notification` hook with the `agent_needs_input` or `agent_completed` type" | `agent-view` §Read session state |

The last two rows of that table are not in conflict, and the difference matters:
`agent-view` says the *alert* follows `preferredNotifChannel`; `hooks` says the
*hook* runs regardless of it, including under `notifications_disabled`. A hook
therefore does not depend on the operator's notification channel — which is one
more reason it is a reasonable thing to install.

### 4.2 From this repository

| Fact | Where |
|---|---|
| The only discovery loop is a 30-second timer | `internal/app/daemon.go:20-27` (`DefaultDaemonInterval`) |
| A pass is six read-and-act stages in a fixed order | `internal/app/daemon.go:135-141` (discover, detect, schedule, recover, complete, deliver) |
| Seven notification events, none meaning "a session needs a human" | `internal/storage/models.go:123-134`, `Valid` at `:137` |
| Each event has a config switch, an env var and a default | `internal/config/config.go:108-120`, `internal/config/defaults.go:51-61`, `internal/config/env.go:96` |
| Event → subject, body and dedupe key are three separate switches | `internal/telegram/template.go:59`, `:85`, `:177` |
| The two switches disagree about an unknown event: `Subject` returns `""` silently, `Render` returns `ErrInvalidEvent` | `template.go:75-76`, `:164` |
| The event set is enumerated in twelve places, in four packages; `Events` and `handleTelegram` are the two no other site implies | `models.go:133`, `:139-141`; `notifier.go:180`; `template.go:20`, `:73`, `:152`, `:177`; `config.go:119`; `env.go:96`; `defaults.go:60`; `configs/config.example.yaml:82`; `server/handlers.go:440-442` |
| A milestone the mapper does not know is dropped without a word | `internal/app/notifications.go:70-84` (`default: return "", false`) |
| The recovery engine announces four milestone kinds | `internal/recovery/observe.go:18`, `:22`, `:32`, `:41` |
| The observer runs on the recovery goroutine and must not fail the recovery | `observe.go:59-66`; `notifications.go:31-63` |
| Dedupe is a database unique key; a repeat reports `created=false` | `internal/storage/notifications.go:23`, `:66` (`ON CONFLICT (dedupe_key) DO NOTHING`) |
| `session_completed` has no case in the key switch, so it dedupes once per session, ever, and re-queues every pass on that basis | `template.go:183-196`; `daemon.go:490-533` |
| The tolerant-parser precedent: "unknown keys are ignored and a missing field is treated as absent rather than as an error, because the CLI reserves the right to add or drop keys between releases" | `internal/claude/agents.go:29` |
| The CLI contract is a non-zero exit and `ccresu: <err>` on stderr | `internal/app/app.go:32-48` (exit codes), `:214-228` (`Execute`) |
| The command table is `commandSpec{name, summary, usage, run}` | `internal/app/app.go:420-` (`commands`) |
| A command can read an injected stream, defaulting to `os.Stdin`, so a test can feed it a recorded payload | `app.go:110-112` (`Deps.TUIInput`), `:186-187`, `:205` |
| The CLI's own query timeout is 30 seconds | `internal/claude/cli.go:18-20` (`DefaultQueryTimeout`) |
| SQLite `busy_timeout` defaults to 5 seconds | `configs/config.example.yaml:98` |
| ccresu is deployed as a systemd service with no terminal | `deployments/systemd/ccresu.service` |

## 5. Why it happens — the causal chain

### RC1 — The only discovery loop is a timer (`internal/app/daemon.go:20-27`)

```go
// DefaultDaemonInterval is how often the reconcile pass runs.
DefaultDaemonInterval = 30 * time.Second
```

Every stage of the pass is a read: `discover` lists sessions and transcripts,
`detect` scans transcripts for limit banners, `complete` lists sessions in
`COMPLETED`. Nothing in the daemon can be woken, so the granularity of every
fact ccresu knows is the interval. A hook is the only mechanism the platform
offers for "tell me now", and ccresu subscribes to none.

### RC2 — The notification vocabulary has no word for a human waiting (`internal/storage/models.go:123-134`)

```go
const (
	EventLimitDetected    NotificationEvent = "limit_detected"
	EventWaiting          NotificationEvent = "waiting_for_reset"
	EventRecoveryStarted  NotificationEvent = "recovery_started"
	EventRecoverySuccess  NotificationEvent = "recovery_success"
	EventRecoveryFailure  NotificationEvent = "recovery_failure"
	EventPermanentFailure NotificationEvent = "permanent_failure"
	EventSessionCompleted NotificationEvent = "session_completed"
)
```

All seven are about a usage window or about the outcome of a recovery. None
means "this session is waiting on you". PRD 2's R12 requires exactly that
milestone, and the mapper it would reach ends in `default: return "", false`, so
the requirement currently has no destination — the observer's caller reads the
`false` and returns without logging anything (`notifications.go:40-42`). A
missing message class is worse than a missing message: nothing in the log says
it was dropped. §6.1a rejects each of the seven as a carrier for that milestone
by name; the eighth is the one PRD 2's R12 actually needs.

### RC3 — An event is wired in twelve places, and the two that fail do it differently (`internal/telegram/template.go:75-76`, `:164`)

`Subject` returns `""` for an event it does not know; `Render` returns
`ErrInvalidEvent`. So a half-added event does not fail loudly — it produces a
message with no subject, or an error the sender retries. And the event set is not
declared once: it is **enumerated in twelve places**, in four packages, so adding
an eighth event is a change to all of them.

| # | Site | What it decides | If it is missed |
|---|---|---|---|
| 1 | `internal/storage/models.go:133` | the constant | it does not exist |
| 2 | `internal/storage/models.go:139-141` (`Valid`) | whether the event is known | it is rejected at the boundary |
| 3 | `internal/telegram/notifier.go:180` (`EnabledFromConfig`) | whether the operator's switch reaches it | it is silently never delivered |
| 4 | `internal/telegram/template.go:20` (`Events`) | the advertised set | `TestEnabledFromConfig` fails on the count, so this one is caught |
| 5 | `internal/telegram/template.go:73` (`Subject`) | the title | `""` — no subject, no error |
| 6 | `internal/telegram/template.go:152` (`Render`) | the body | `ErrInvalidEvent` on every send |
| 7 | `internal/telegram/template.go:177-198` (`DedupeKey`) | the identity | it falls through to once-per-session (RC4) |
| 8 | `internal/config/config.go:119` | the `notify_*` field | no switch exists |
| 9 | `internal/config/env.go:96` | the environment override | the operator cannot override it |
| 10 | `internal/config/defaults.go:60` | the shipped default | it defaults to `false` by accident |
| 11 | `configs/config.example.yaml:82` | the documented config | it is undocumented |
| 12 | `internal/server/handlers.go:440-442` (`handleTelegram`) | the API's `events` map | the local API reports one event fewer, and **no test notices** |

Row 12 is the one this PRD would otherwise have shipped a hole for. `handleTelegram`
builds its `events` map from a hand-written list, not from `Events()`, so a new
event is absent from the API's own report of what it can deliver. Rows 5 and 7 are
the pair the heading is about: one fails as an empty string, the other as an error
the sender retries.

### RC4 — The dedupe key gives per-episode identity only to limits and attempts (`internal/telegram/template.go:177-198`)

```go
case storage.EventLimitDetected, storage.EventWaiting:
	if data.LimitID != 0 {
		fmt.Fprintf(&key, ":%d", data.LimitID)
	}
case storage.EventRecoveryStarted, storage.EventRecoverySuccess,
	storage.EventRecoveryFailure, storage.EventPermanentFailure:
	...
```

Anything else — `session_completed` included — falls through to
`event:<session id>`. That is right for a settlement and wrong for a turn end:
a background session finishes many turns, and each one would collide with the
first. So the hook cannot be mapped onto an existing completion event; the new
event needs an episode component that both the hook path and the poll path can
compute from the same persisted fact.

### RC5 — ccresu has no subcommand safe to call from another program's event pipeline (`internal/app/app.go:214-228`)

```go
code := exitCode(err)
fmt.Fprintf(a.stderr, "ccresu: %v\n", err)
if code == ExitUsage {
	fmt.Fprintln(a.stderr, "Run 'ccresu --help' for usage.")
}
return code
```

Every command reports failure loudly, which is right for a human and wrong for a
hook: a hook that prints to stderr and exits 1 is a notice in someone's
transcript. The subcommand is one row in `commands()`, but its *contract* —
always exit 0, never write to either stream — is unlike every other row.

### RC6 — The hook is unavailable exactly where ccresu runs (`deployments/systemd/ccresu.service`)

`agent_completed` "Fires only while agent view is open in a terminal", and
`agent_needs_input` fires "while agent view is open". `ccresu` runs as a systemd
service with no terminal, and its whole purpose is to work while nobody is
watching. So the accelerator is absent in the deployment that needs it most, and
present when a human is already looking at agent view. This is not a reason to
skip the hook — it costs one subcommand and buys latency whenever someone *is*
watching — but it is the reason the poll cannot be retired, and the reason a
missing hook needs no error path.

### The resulting shape

```
Claude Code (agent view open)
  └─ Notification hook ──► ccresu hook ──► one observation ──┐
                                                             ├─► milestone ─► event ─► queue ─► Telegram
ccresu daemon (every 30s)                                    │
  └─ reconcile pass ─────► same observation ─────────────────┘
```

Both arrows end in the same mapper and the same dedupe key. That is what makes
the hook an accelerator rather than a second source of truth. The arrows do not
carry the same *amount*: the pass can raise all five milestone kinds, and the
hook can raise one (§6.1, R14).

## 6. Requirements

| # | Requirement |
|---|---|
| **R1** | `ccresu hook` exists as a subcommand and **always exits 0**. It writes nothing to stdout and nothing to stderr. Its exit code is not a verdict; any other code is a bug, and a test asserts the whole matrix of bad inputs. |
| **R2** | It reads one JSON object from stdin tolerantly, on the precedent of `internal/claude/agents.go:29`: unknown fields are ignored, a missing field is absent rather than an error, and a payload that is empty, truncated, not JSON, or larger than a bounded size is ignored. The read is bounded by both a size cap and a deadline. |
| **R3** | It acts only on `notification_type` `agent_needs_input` and `agent_completed`. Every other documented type is ignored silently — they describe an interactive terminal session, which ccresu does not model. |
| **R4** | The hook **never decides the event**. It triggers one observation and passes the result to the existing milestone producer, so a hook observation and a poll observation of the same *fact* yield the same milestone, the same event and the same dedupe key. It cannot raise a milestone from a fact it may not act on (R14). There is no second notifier and no second mapping. |
| **R5** | The observation is one bounded `claude agents --json --all` read filtered to the session id from stdin — the supported read path of PRD 2. It does not read a transcript, and it does not open agent view (which "requires an interactive terminal"). |
| **R6** | Both paths enqueue through `Notifier.Notify`, so the database's `dedupe_key` unique constraint is the only dedupe mechanism. The key carries episode identity for `needs_input`, per §6.3. |
| **R7** | A new event `EventNeedsInput NotificationEvent = "needs_input"` is added, wired in **every one of the twelve sites RC3 enumerates** — the constant, `Valid`, `EnabledFromConfig`, `Events`, `Subject`, `Render`, `DedupeKey`, the config field, the env var, the default, the example config and `handleTelegram` — with a config key `notify_needs_input`. |
| **R8** | `MilestoneNeedsInput` is added to `recovery.MilestoneKind` and mapped in `notificationEvent`. No kind PRD 2 can emit may reach the dropping `default`; a table test walks every kind and asserts `ok` is true. |
| **R9** | A missing, broken, slow or disabled hook degrades to today's behaviour. No hook configured, a binary that cannot start, a non-zero exit, a timeout, a locked database and an unknown session id all leave the poll as the only path, with nothing else changed. |
| **R10** | The hook cannot slow or block a session: bounded stdin read, bounded database wait, bounded CLI query, no network call, and no attempt to start ccresu's daemon. The documented installation sets an explicit `timeout`, because a command hook's default is 600 seconds. |
| **R11** | The hook never creates, discovers, enables or recovers a session. An unknown `session_id` is ignored; a session ccresu does not know is discovery's business, not a hook's. |
| **R12** | Installing the hook is documented, never performed. `ccresu` does not edit `~/.claude/settings.json`, and no code path writes to a Claude Code configuration file. |
| **R13** | `notify_needs_input` defaults to **false**, matching `notify_waiting` (`internal/config/defaults.go:55`), because it is a new message class and an upgrade should not start sending one. |
| **R14** | The hook raises **exactly one** milestone: `MilestoneNeedsInput`. It never raises `MilestoneStarted`, `MilestoneSucceeded`, `MilestoneFailed` or `MilestonePermanent`, because each of those is a verdict about an attempt row the hook does not hold and may not write (R11). `needs_input` is therefore the only event two producers can reach, and both compute it from the same milestone, the same observation and the same key (§6.3) — so one condition cannot yield two different events. |

### 6.1 What each hook type does

| `notification_type` | What ccresu does | Milestone it can raise | Event it can produce | Why |
|---|---|---|---|---|
| `agent_needs_input` | one observation of the named session | `MilestoneNeedsInput`, when the observation says `blocked` | `needs_input` | the hook says a human was needed a moment ago; the published state says whether one still is |
| `agent_needs_input`, observation no longer `blocked` | one observation | none | none | the human answered between the notification and the hook; the hook reports a past moment and the observation is the present |
| `agent_completed` | one observation of the named session | `MilestoneNeedsInput`, when the observation says `blocked` | `needs_input` | the type says "finishes or fails" and does not say which, so the observation decides — and `blocked` is the only answer that is a message |
| `agent_completed`, observation `done`, `failed`, `stopped` or absent | one observation | none | none | a turn ending is not a settlement, and a failure is the engine's verdict rather than an observation's — see below |
| `permission_prompt`, `idle_prompt`, `auth_success`, `auth_storage_failure`, `elicitation_dialog`, `elicitation_url_dialog`, `elicitation_complete`, `elicitation_response`, `quota_auto_resume_fired`, `quota_auto_resume_stale`, `quota_auto_resume_disabled`, `plugin_notification` | nothing | none | none | they describe an interactive terminal session, not a background one; mapping them would make ccresu a second notifier for events it does not model |

Three of those rows produce nothing, and the silence is the design rather than an
omission. **`agent_completed` never produces a completion event.** A background
session that ends a turn is `done`, and `done` means "ready for your next prompt",
not "settled" (README §3, *Blocked vs done*); announcing every turn end would be
noise, and mapping it to `session_completed` would spend a once-per-session dedupe
key on the first turn (RC4). **It never produces a failure event either**, and
that is the sharper reason: a `failed` observation is `RETRY_WAIT` (PRD 2 §6.1),
which the recovery engine announces as `recovery_failure` keyed on the attempt
row (`template.go:188-196`). A hook holds no attempt row, so it could only produce
`recovery_failure:<session id>` — a *different key for the same failure*, hence a
second message when the next pass queues the real one. **And it never produces a
success event**, because success is PRD 2's verified `RESUMED`/`COMPLETED`, not
the absence of bad news in a notification.

So what `agent_completed` is worth is the observation itself, and the only fact in
an observation the hook may turn into a message is `blocked` (R14). A session
that fails is announced by the pass that follows, up to thirty seconds later,
with the attempt that caused it — which is the message an operator needs and the
one a hook cannot compose. A `blocked` observation is different in kind: it is
not a verdict about an attempt, it is a fact about the present that carries its
own reason (`waitingFor`), so both producers can raise it without either
contradicting the other.

### 6.1a Why not reuse one of the seven existing events

The alternative to an eighth event is reusing one, and it is worth rejecting by
name rather than by taste, because several of the seven look close enough to be
tempting.

| Existing event | Why it cannot carry "a session is waiting on a human" |
|---|---|
| `limit_detected` | its body promises the opposite of what is true: "ccresu is waiting for the reset and will resume this session automatically" (`template.go:98`) |
| `waiting_for_reset` | the same promise (`template.go:107`), and its dedupe key is limit-scoped, so with no limit row it collapses to once per session (`template.go:184-187`) — the first needs-input message would swallow the usage-limit message that follows it |
| `recovery_started` | it announces a handoff with `claude -p --resume` (`template.go:113`); a blocked session is deliberately never handed off (PRD 2 R11) |
| `recovery_success` | PRD 2 R12 forbids this event by name: a `blocked` observation is never success |
| `recovery_failure` | its remedy is "ccresu keeps the session and tries again automatically" (`template.go:136`); answering a permission prompt is not something a retry can do, and the retry would meet the same prompt |
| `permanent_failure` | its subject is "Automatic recovery stopped" (`template.go:72`); nothing stopped — the session is parked and continues the moment the human answers |
| `session_completed` | no episode component in its key, so once per session, ever (`template.go:186-196`) — a session that asks five questions would be announced once |

Two of those rows would also merge two switches into one: an operator who turned
`notify_permanent_failure` off because they do not want "your session is dead"
messages, or `notify_session_completed` off because they do not want a message
for every finished conversation, would silently lose "your session needs you". A
message class the operator can switch on its own is the reason for adding an
eighth event rather than bending a seventh.

Two consequences are worth naming for whoever implements the neighbouring PRDs,
because they are message text this PRD does not own. `recovery_success`'s body
says "The original conversation resumed and its transcript grew"
(`template.go:120`), and PRD 2 retires growth as proof; `recovery_started`'s body
says ccresu is "Resuming the original conversation with `claude -p --resume`"
(`template.go:113`), and PRD 1 retires print mode. Both sentences change with the
PRD that invalidates them. They are recorded here only so the change has an
owner; the mapping in §6.2 does not change because of either.

### 6.2 Every milestone PRD 2 can emit, and its event

This is the complete mapping, and it is the only one. Both the hook path and the
poll path call it.

Ownership first, because two documents meet here and the boundary is what keeps
them from drifting: **PRD 2 defines the milestone kind and its payload** — that a
`blocked` observation raises `MilestoneNeedsInput`, carrying the verbatim
`waitingFor` when the observation has one and recording that none was reported
when it does not (`authoritative-session-state.md` R12, §6.1) — and **this PRD defines
the event name, its subject, its body and its dedupe key**. PRD 2 says "the
mapping from milestone to `storage.NotificationEvent` and transport is PRD 4's"
and stops there; it cites the event name only to say whose it is, and defines
none of that value's properties.

| `recovery.MilestoneKind` | `storage.NotificationEvent` | Produced by | Note |
|---|---|---|---|
| `MilestoneStarted` | `recovery_started` | poll only | unchanged (`notifications.go:72-73`); needs an attempt row the hook does not have (R14) |
| `MilestoneSucceeded` | `recovery_success` | poll only | unchanged (`:74-75`); success is verified, not observed once (PRD 2 R3, R6) |
| `MilestoneFailed` | `recovery_failure` | poll only | unchanged (`:76-77`); keyed on the attempt, which the hook cannot compute (R14) |
| `MilestonePermanent` | `permanent_failure` | poll only | unchanged (`:78-79`); a retry-policy classification, which is the engine's |
| `MilestoneNeedsInput` | `needs_input` **(new)** | **both** | PRD 2 R12; the only milestone a hook can raise, and the only event two producers reach |
| — | `session_completed` | daemon | has no milestone: a settled session never enters the recovery engine (`daemon.go:490-498`) |
| — | `limit_detected`, `waiting_for_reset` | limit path | have no milestone; produced by `notifyLimit` |

The last two rows exist so the table is complete rather than merely consistent:
eight events for five milestones, and the reader can see which producer owns each.
The five kinds are all of them — PRD 2 adds exactly one kind to the four in
`internal/recovery/observe.go:18-41` and says so in its own §7 — so "covers every
milestone PRD 2 can emit" is checkable by reading PRD 2's requirement table rather
than by trusting this one.

The mapping stays a single switch with an `ok` result, so a new milestone is a
compile-visible omission rather than a dropped message — and R14 keeps the hook
from widening it: the subcommand can only construct `MilestoneNeedsInput`, so
there is one row two callers share and four rows exactly one caller can reach.

### 6.2a Subject and body

§6.2 says the name, the subject and the body are this PRD's to define, so they are
defined here rather than left to whoever implements it. Both follow the shape the
seven existing events already use (`template.go:59-78` for `Subject`,
`:85-168` for `Render`): a short title, then a body that opens with the same title
in bold, names the session, carries the observation, and closes with what will or
will not happen next.

| Property | `needs_input` |
|---|---|
| `Subject` | `Needs your input` — a title, not a sentence, matching `Usage limit detected` (`:62`) and `Recovery started` (`:66`) |
| Body, title | bold `Needs your input`, as every body repeats its subject in bold |
| Body, identity | `Session: ` plus the session id in `Code`, the row every existing body carries first |
| Body, reason | `Reason reported: ` plus the `waitingFor` value escaped — **only when the milestone carried one** |
| Body, no reason | `No reason was reported.` plus `The session is blocked but did not say why.` — **when `waitingFor` was absent**, per `authoritative-session-state.md` R12 |
| Body, project | `Project: ` plus the path in `Code`, when the observation knows it, as `EventLimitDetected` does (`:93-95`) |
| Body, close | `ccresu cannot answer this for you. The session stays as it is until you reply.` |

Three rules govern that body, and each has a failure it prevents.

**The reason is never inferred.** A `blocked` observation with no `waitingFor` is
the common case, not an edge case — every session in the payload recorded in
`authoritative-session-state.md` §6.1 was in exactly that shape. Substituting
`input needed`, the most plausible of the five documented values, would put a
specific claim in the operator's notification that the session never made.

**No row is invented.** When the reason is absent the "no reason" line *replaces*
the reason line; the body does not print an empty `Reason reported: ` or omit the
row silently and leave the operator unable to tell "no reason" from "ccresu did
not look".

**The value is escaped.** `waitingFor` is CLI text, and it is escaped exactly as
`ResetRaw` is (`:105`) and for the same stated reason (`:82-84`): a hostile or
merely unusual value must not be able to break the markup or inject a line that
looks like one of ccresu's own rows.

### 6.3 The dedupe rule

| Event | Key | Episode component |
|---|---|---|
| `limit_detected`, `waiting_for_reset` | `event:sessionID:limitID` | the usage-limit row — unchanged |
| `recovery_started`, `recovery_success`, `recovery_failure`, `permanent_failure` | `event:sessionID:attemptID` (or `:attempt`) | the attempt row — unchanged |
| `session_completed` | `session_completed:sessionID` | none, deliberately: once per session, ever — unchanged |
| `needs_input` | `needs_input:sessionID:episodeStart` | **the instant the session entered `WAITING_FOR_INPUT`**, as a Unix second — new |

The rule, in one sentence: **the key is computed from the observation, never from
the hook.** Four consequences follow, and they are the reason this is a rule and
not a convention.

1. A hook at `T` and the next poll at `T+30s` compute the same `episodeStart`
   from the same persisted transition, so they produce the same key. The second
   `EnqueueNotification` is a no-op under `ON CONFLICT (dedupe_key) DO NOTHING`
   (`internal/storage/notifications.go:66`) and reports `created=false`
   (`:23`). Exactly one message is queued.
2. `episodeStart` is persisted, so a ccresu restart in the middle of an episode
   does not produce a second announcement.
3. A genuinely new episode — the human answers, the session asks again — writes a
   new transition, so it gets a new key and a new message.
4. A hook that fires twice for one episode (the docs describe a six-second gate
   for the terminal-only variants but do not state the background variant's
   repeat behaviour) is harmless for the same reason.

The documented limitation: a session that blocks twice within the same second is
announced once, because the episode instant is the only thing distinguishing the
two and it is truncated to the second. A sub-second distinction would need a
monotonic counter on the transition row, and an operator does not need two
messages one second apart.

### 6.4 Failure modes

| Failure | What ccresu observes | What it must do |
|---|---|---|
| **No hook installed** | nothing ever arrives | the poll is the only path; behaviour is byte-for-byte today's. This is the default state of every existing installation |
| **Hook binary missing or moved** | Claude Code reports a non-blocking error (`exit 127`); the session continues | nothing. ccresu cannot see the failure and must not try to; the poll is unaffected |
| **Hook exits non-zero** | for `Notification`, "Exit code and stderr are ignored" | nothing. R1 exists so this never happens, not because the platform punishes it |
| **Payload malformed, empty or oversized** | the hook's own stdin | exit 0 silently; log the payload's first bytes and the parse error to ccresu's log, never to stderr |
| **`notification_type` unknown or new** | a type this PRD does not list | exit 0 silently. A newer CLI adding a type must not produce a notice |
| **Session id unknown to ccresu** | the observation finds no such session | exit 0 silently. Discovery owns discovery (R11) |
| **Database locked by the daemon** | `busy_timeout` expires (5 s by default) | exit 0. The next poll catches up; a hook that waits is worse than a hook that gives up |
| **`claude` unavailable to the hook's environment** | the observation's subprocess fails | exit 0; log it. The hook runs in Claude Code's environment, which is not necessarily ccresu's |
| **Hook slower than its timeout** | Claude Code cancels it | nothing is queued; the poll catches up. R10 and the explicit `timeout` in the snippet exist to keep this rare |

### Acceptance criteria

- `TestHookAlwaysExitsZero` feeds the subcommand empty stdin, truncated JSON, a
  JSON array, a 1 MiB payload, an unknown `notification_type`, an unknown
  `session_id`, and a locked database, and asserts exit 0 with empty stdout and
  stderr in every case.
- `TestHookAndPollCannotBothNotify` feeds a recorded `agent_needs_input` payload
  and then runs a daemon pass over the same blocked session, and asserts exactly
  one `needs_input` row exists and that the second enqueue reported
  `created=false`.
- `TestNeedsInputIsOncePerEpisode` asserts a second observation inside one
  `WAITING_FOR_INPUT` episode queues nothing, while a new episode after a
  transition out and back queues a second message.
- `TestAgentCompletedDoesNotNotifyOnATurnEnd` asserts a session observed `done`
  with no settlement queues no notification, and that the pass which follows
  announces nothing either — a finished turn is not an event.
- `TestEveryMilestoneKindHasAnEvent` walks every `MilestoneKind` the recovery
  engine can emit, including `MilestoneNeedsInput`, and asserts
  `notificationEvent` returns `ok == true` for each — so no kind reaches the
  dropping default.
- `TestNeedsInputIsWiredInEverySwitch` asserts the new event is `Valid`, appears in
  `Events`, has a non-empty `Subject`, renders without error, appears in
  `EnabledFromConfig`, produces a dedupe key containing the episode instant, is
  present in `handleTelegram`'s `events` map, and carries a `notify_needs_input`
  key in `configs/config.example.yaml` — every site RC3 tabulates, so the one
  site no existing test covers (row 12) cannot be missed again.
- `TestNeedsInputBodyNeverInventsAReason` renders `needs_input` twice: with
  `waitingFor` carrying `permission prompt`, and with `waitingFor` absent as it is
  in the payload recorded in `authoritative-session-state.md` §6.1. The first body
  contains the escaped value and no "no reason" line; the second contains the
  no-reason line, does not contain `Reason reported:`, and does not contain any of
  the five documented values — a substituted reason is the failure this asserts
  against, so a body that merely renders is not enough.
- `TestHookPayloadWithUnknownFieldsIsAccepted` feeds a payload carrying fields no
  version of this PRD knows and asserts it is handled as if they were absent.
- `TestHookIsNotOnTheRecoveryPath` asserts that invoking the hook with no notifier
  configured queues nothing, writes no attempt row and changes no session state.

## 7. Non-goals

- **A second notifier.** PRD 4 feeds the queue that already exists — the same
  position `prds/README.md` §6 takes.
- **Mapping the interactive-only notification types.** `permission_prompt`,
  `idle_prompt`, the `auth_*`, `elicitation_*` and `quota_auto_resume_*` families
  and `plugin_notification` describe a terminal session. ccresu does not model
  them and will not start.
- **Editing the operator's settings.** Documented, never performed (R12).
- **A wake channel for the daemon.** Rejected rather than deferred: it would be
  new IPC and a new failure mode (a hook that cannot reach the daemon) to trigger
  an observation the hook can perform itself.
- **Retiring the poll.** RC6 is why not.
- **Reusing an existing event for the needs-input case.** Rejected by name in
  §6.1a rather than deferred: each of the seven would either promise something
  false about a blocked session or lose the message to a key scoped for a
  different episode.
- **Announcing a recovery outcome from an observation.** R14: a finished turn is
  not a settlement, a failure is the retry policy's verdict, and the dedupe key
  needs an attempt row a hook does not hold. The pass announces outcomes; the
  hook announces that a human is needed.
- **Capability gating.** The withdrawn PRD has no role here: a CLI too old to
  fire these types is indistinguishable from one where the hook was never
  configured, and both are today's behaviour.
- **Deciding anything from the hook's `message` or `title`.** They are human text
  for a human channel; the observation is the fact.
- **The state mapping itself.** PRD 2 owns it; this PRD consumes it.
- **Spend guardrails and worktrees.** PRDs 5 and 6.

## 8. Proposed shape

| Step | Change | Satisfies |
|---|---|---|
| 1 | Add `EventNeedsInput` and wire it in all twelve sites RC3 tabulates: the constant, `Valid`, `EnabledFromConfig`, `Events`, `Subject`, `Render`, `DedupeKey`, the config field, the env var, the default, the example config and `handleTelegram` | R7 |
| 2 | Add `notify_needs_input` to the config struct, its default and its env var | R7, R13 |
| 3 | Add `MilestoneNeedsInput` to `recovery.MilestoneKind`, a case to `notificationEvent`, and a needs-input producer that is the *only* milestone the hook path can construct | R8, R14 |
| 4 | Give `DedupeKey` an episode component for `needs_input`, read from the `WAITING_FOR_INPUT` transition | R6, §6.3 |
| 5 | Add a tolerant parser for the hook payload in `internal/claude`, beside the `agents --json` parser and on the same precedent | R2 |
| 6 | Add `ccresu hook` to `commands()` with a `run` that never returns an error, reads the injected stream, performs one observation, raises at most `MilestoneNeedsInput`, and returns nil | R1, R3, R4, R5, R11, R14 |
| 7 | Document the settings snippet with an explicit `timeout`, and add `ccresu hook` to the review checklist as a command whose contract is "always succeed" | R10, R12 |

Step 6 is the one that needs care in review. Every other row of `commands()` is a
human-facing command whose failure is the point; `hook` is the first row whose
failure is a bug, and the only one that must be silent on both streams.

### Migration

- **First run with no settings change.** Identical to today. The subcommand
  exists and nothing invokes it; `notify_needs_input` defaults to false, so no new
  message class appears on an upgrade. There is nothing to migrate and nothing to
  back-fill.
- **First run after the operator adds the snippet.** The hook fires on the next
  `agent_needs_input` or `agent_completed` while agent view is open, and an
  observation that says `blocked` writes the first `needs_input` row then; there
  is no history to reconstruct.
- **An installation whose CLI predates these notification types.** The hook never
  fires. That is not a degraded mode to detect — it is byte-for-byte today's
  behaviour, which is why this PRD needs no capability gate and why the poll
  stays. This is the answer `prds/README.md` §4 asks each PRD to give for a
  capability the operator's CLI lacks. PRD 1's floor (`2.1.257`) is newer than the
  release that added these types (`v2.1.198`), so under the floor they are always
  present and this bullet is a guard rather than a supported configuration.
- **An installation where `claude` is not on the hook's `PATH`.** The observation
  fails, the hook exits 0, the poll continues. Nothing to migrate and nothing to
  configure.
- **A session mid-attempt when the change lands.** Unaffected. The hook never
  writes an attempt (R11), never recovers, and never changes a session's state;
  it only observes and, through the shared mapper, queues.
- **A session parked in `WAITING_FOR_RESET` when the change lands.** Unaffected.
  The hook never schedules and never wakes a wait: the scheduler ends the wait as
  it does today, and a hook that fires for such a session observes a `state` and
  queues nothing.

## 9. Open questions

1. **Is the hook's `session_id` the conversation UUID?** The `hooks` example shows
   a bare `session_id` and a `transcript_path` naming a UUID, but not the two side
   by side, and this set is careful about the state-id/session-id distinction
   (README §3). Settle it by firing the hook once against a session whose UUID is
   known before implementing the lookup.
2. **How often can `agent_needs_input` fire for one episode?** The docs give a
   six-second gate for the terminal-only variants and say nothing about the
   background one. The episode dedupe makes a repeat harmless, but the answer
   decides whether the hook can be treated as one event or as a stream.
3. **Does `agent_completed` fire for a session the supervisor idle-stopped?** A
   stop is neither "finishes" nor "fails". If it does fire, the observation will
   say `stopped` and nothing is announced — which is correct, but worth confirming
   rather than assuming.
4. **Should `notify_needs_input` default to on?** This PRD says off, matching
   `notify_waiting`. That is a judgement about upgrade noise, not a measurement;
   an operator who wants the message will find the switch, and one who does not
   will not be surprised by it.
5. **Is a 10-second hook timeout enough on a loaded machine?** The observation
   spawns `claude agents --json --all`, and the CLI's own query timeout is 30
   seconds. The hook's budget is deliberately smaller because it runs inside
   another process's event pipeline, so the number needs measuring against a real
   supervisor rather than choosing by feel.
