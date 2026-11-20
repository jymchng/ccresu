# CLI reference

```text
ccresu [--config PATH] [--json] [--log-level LEVEL] <command> [flags]
ccresu --help | --version
```

Global flags: `-c/--config PATH`, `--json`, `--log-level`, `--log-format`,
`-v/--verbose` (shorthand for debug), `-q/--quiet` (shorthand for error). They
are accepted **before or after** the command name: `ccresu --json status` and
`ccresu status --json` are the same invocation. Flags and positionals may be
interleaved (`ccresu recover <id> --json`), and a flag value is never mistaken
for a global flag (`ccresu recover <id> --message --json` sends the text
`--json`), because the command's own flag set does the parsing. `--` ends flag
parsing, so a value that begins with `-` can still be passed.

The configuration file is chosen in this order, first match wins: `-c/--config
PATH`, then `CCRESU_CONFIG` in the environment, then
`$XDG_CONFIG_HOME/ccresu/config.yaml` (else `~/.config/ccresu/config.yaml`). A
leading `~` is expanded in all three, and everything derived from the file — such
as the API token beside it — follows the file actually in use.

Exit codes: `0` ok, `1` failure, `2` usage, `3` not found, `4` not eligible (the
session exists but this request could not be carried out — for example settled,
disabled, already running, out of attempts, or parked until its usage limit
resets), `130` interrupted.

## Commands

| Command | Usage | What it does |
| --- | --- | --- |
| `daemon` | `daemon [--once] [--interval DURATION]` | Runs the watchdog loop until interrupted. `--once` performs a single reconcile pass and exits; `--interval` overrides `claude.discovery_interval`. |
| `status` | `status` | One screen of the persisted state: `config` and `database` (the paths actually in use), `sessions` (`N total, M enabled`), `listing` (what a bare `sessions` shows), `pending`, `schedules`, `next wake` when one is due, `telegram`, `recovery` (`yes (max attempts 10)`), `now`, then a `STATE`/`SESSIONS` histogram. The queued-notification count is `pending_notifications` in `--json`. |
| `sessions` | `sessions [--all] [--state STATE]... [--limit N] [--tui] [--no-tui]` | Lists discovered sessions: `SESSION` (short id), `STATE`, `NEXT ATTEMPT`, `ENABLED`, `RECOVERABLE`, `PROJECT`, `RUNS IN`, `LAST SEEN`, `NOTE` (the session's reason, else its last error, truncated to 40). `PROJECT` is where the session started and `RUNS IN` is where it actually runs — they differ when a background session has moved into a git worktree, and `RUNS IN` shows `-` when the two agree rather than repeating one path in two columns. On a terminal this opens the interactive browser; a pipe, a redirect or `--json` gets the table exactly as before. A bare listing is **enabled only** and says so in the footer; `--all` includes disabled and settled ones; `--state` is repeatable; `--limit 0` means all. |
| `sessions status` | `sessions status <session-id>` | Everything known about one session: state, paths (including `runs in` — where the session actually runs, marked `(same as the project)` when it has not moved, and `unknown` when no observation has recorded one), the recorded spend and the configured spend caps, the next attempt, the latest usage limit, the latest attempt (including what it cost), any pending wait and recent transitions. Identical to `inspect`, which is kept as an alias. |
| `sessions delete` | `sessions delete <session-id>` | Removes the session from ccresu: the row, its attempts, transitions, usage limits, pending wait and notifications. Nothing under `~/.claude` is touched — the conversation stays where it is. A tombstone keeps discovery from re-creating the session; `ccresu enable <id>` undoes it. Deleting twice is a success, not an error. |
| `inspect` | `inspect <session-id>` | Everything known about one session. The same code as `sessions status`. |
| `recover` | `recover <session-id> [--message TEXT] [--force]` | Resumes one session now, optionally with a different continuation message. `--force` tries even when the attempt budget is spent. |
| `retry` | `retry <session-id>` | Retries a session whose attempt budget is spent. |
| `search-limit` | `search-limit <session-id> [--tail N]` | Reads the newest `--tail` transcript lines (default 25 — the same window the daemon's detect stage reads) and asks the detector whether one of them is a usage-limit banner. When one is, the session parks on the reset the banner names: `RESUMED → DISCOVERED → WAITING_FOR_RESET`, the same two edges a daemon detection takes — the machine has no `RESUMED → WAITING_FOR_RESET` edge, because a session that was just resumed must be observed again before it is parked again, and an operator's search *is* that re-observation — so the audit trail cannot tell an operator's search from a pass. The command exists because `RESUMED` is settled: no pass reads that transcript again, so a limit that lands after a resume would otherwise leave the session stuck for ever. Only an idle session can be searched — `RESUMED`, or `DISCOVERED`, because the operator's assertion that it has gone idle is the authority, and refusing the state the daemon is also reading would make the command's behaviour depend on the daemon's timing; anything else exits 4. Nothing is resumed and nothing is spent — the state machine gives the search no edge to `RESUMING` — and a search that finds nothing writes nothing (no attempt, no transition, no schedule) and still exits 0, because an answer is not a failure. `--json` reports `session_id`, `found`, `state`, `banner`, `known_reset`, `reset_at`, `wait`, `usage_limit_id` and `reason`. |
| `sessions search-limit` | `sessions search-limit <session-id> [--tail N]` | The `sessions`-namespaced spelling of `search-limit`. Same handler, same flags, same output. |
| `enable` | `enable <session-id>` | Allows ccresu to recover a session. It is also the undo for `sessions delete`: a deleted session is restored (tombstone removed, row re-created in `DISCOVERED`) before it is enabled. |
| `disable` | `disable <session-id>` | Stops ccresu from recovering a session. |
| `history` | `history <session-id> [--limit N]` | Every attempt (`#`, `STATE`, `STARTED`, `FINISHED`, `COST`, `RESULT`, `ERROR`) and the state transitions (`WHEN`, `TRANSITION`, `REASON`). `COST` is what the CLI reported for that attempt, or `unknown` when it reported nothing. `--limit` bounds the transitions only, default 50; the attempts are always all of them. |
| `config` | `config init [PATH] [--force]` / `config validate [PATH]` / `config show` | Creates the configuration from the shipped example, validates a file, or prints the effective one. |
| `doctor` | `doctor` | Checks the environment ccresu runs in. |
| `serve` | `serve [--addr HOST:PORT]` | Runs the local REST/SSE API (default `server.host:server.port`). |
| `version` | `version` | Prints version and build metadata. |

`ccresu --version` and `ccresu --help` work without a subcommand.

### Browse sessions interactively

On a terminal, `ccresu sessions` opens a full-screen browser instead of printing
the table. The columns are the table's except `RECOVERABLE`, which the browser
leaves out, and `SESSION` carries the short id, because a 36-character UUID does
not fit a fixed-width screen. The point is that triaging a night's worth of
sessions no longer means copying ids between commands: every action the CLI has
is one keystroke away, and it applies to the row the cursor is on.

```console
$ ccresu sessions --all
ccresu sessions — 3 shown (all)
  SESSION   STATE              NEXT ATTEMPT                    ENABLED  PROJECT                  RUNS IN                            LAST SEEN                NOTE
> 88dfd995  DISABLED           none (session is not enabled)   no       /home/you/projects/site  -                                  2026-11-13 09:34:43 +08  -
  3f9d1c07  WAITING_FOR_RESET  in 12m40s                       yes      /home/you/projects/demo  -                                  2026-11-13 09:34:43 +08  -
  4d1f9c2a  RESUMED            none (no usage limit detect...  yes      /home/you/projects/api   /home/you/projects/api/.claude/worktrees/fix-auth  2026-11-13 09:34:43 +08  -

enter detail  n new  x delete  R resume  e enable  d disable  r retry  / filter  a all  ? help  q quit
```

`RUNS IN` is where the session actually runs. It is `-` when that is the
project directory, because repeating one path in two columns is noise; the third
row is a background session that has moved into its own git worktree, which is
also the directory a resume re-enters.

The `>` marker is the cursor. The footer repeats the everyday keys, and `?`
paints the whole map, which is the same table as this one — both come from
`tuiKeyMap` in `internal/app/tui.go`, so the screen and the documentation cannot
drift apart:

| Key | What it does |
| --- | --- |
| `j / down` | next session |
| `k / up` | previous session |
| `g / home` | first session |
| `G / end` | last session |
| `/` | filter by text (enter applies, esc cancels) |
| `c` | clear the filter |
| `a` | toggle --all (disabled and settled sessions) |
| `enter / i` | session detail (the `sessions status` page) |
| `n` | create: add the sessions discovered on disk since the last pass |
| `e` | enable the selected session |
| `d` | disable the selected session |
| `r` | retry the selected session |
| `R` | resume the selected session now (the call `ccresu recover` makes) |
| `L` | search the selected session's transcript for a usage limit (the call `ccresu search-limit` makes) |
| `x` | delete the selected session (asks y/N) |
| `?` | this key map |
| `q / ctrl-c` | quit |

Each action is the command of the same name, not a second implementation:
`enter` renders the page `sessions status` prints, `R` makes the
`recovery.Manager.RecoverRequest` call `ccresu recover` makes, `e` and `d` flip
the same flag `enable` and `disable` flip, and `r` retries the way `ccresu retry`
does. Nothing happens without a selection — with an empty or filtered-out listing
the footer says `no session selected` and no store is touched.

Two of them deserve a word:

- **`x` deletes, after asking.** The confirm line names the session and says what
  is at stake: `delete 88dfd995-…? this forgets ccresu's row, not the
  conversation. y/N`. Only `y` proceeds; anything else prints `delete cancelled`.
  A confirmed delete is `ccresu sessions delete`: the row, its attempts,
  transitions, usage limits, pending wait and notifications go, the tombstone
  stays so discovery does not resurrect it, and nothing under `~/.claude` is
  touched.
- **`n` creates by discovering.** It does not invent a session: it runs the
  daemon's discovery pass (`Provider.Discover`, then `UpsertSession`, skipping
  tombstoned ids) and reports `create: N new session(s) of M discovered`, adding
  `, K left forgotten` when tombstones were skipped. The same rows appear on the
  daemon's next pass, so pressing it twice is harmless.

#### While a command runs

Four of the keys run a command that invokes `claude` — `enter` and `i` render the
status page, `n` runs a discovery pass, and `r` and `R` run a recovery — and any
of them can take seconds. None of them runs on the goroutine that reads the
keyboard, so the browser keeps drawing and keeps answering while they do:

```console
busy: discovering… (ctrl-c cancels)
```

While that line is up the keys that start work — `enter`, `i`, `n`, `e`, `d`,
`r`, `R`, `L`, `x` — are **refused** with the same line rather than queued, because a
queued key is applied to the screen the command produced: a second `enter` used
to open the detail page and close it again before it could be read. `L` is in
that list even though it invokes no CLI: reading a large transcript is not free,
and the keyboard must not wait for it either. Movement,
`?`, `ctrl-c` and `q` keep working. `ctrl-c` cancels the command — its context is
cancelled, so the `claude` call behind it is killed — and leaves the browser open
on the list with `cancelled` on the status line; `q` cancels and quits.

The commands are the ones that are slow, and their budgets are their own: a
recovery is bounded by `recovery.startup_timeout` and
`recovery.verification_timeout`, and a discovery pass by the CLI's query timeout.
`ctrl-c` is the bound that always applies.

#### The detail screen

`enter` paints the page `ccresu sessions status <id>` prints, in two parts. The
session's own facts come from ccresu's store and are painted from the keystroke;
the supervisor block costs an invocation of `claude`, so it is read *behind* the
page and filled in when it answers:

```console
reading the supervisor… (the page fills in)
```

If it does not answer within five seconds the block says so — `status: not read`
with the call that did not answer — and the rest of the page is unaffected.
Nothing else on that page waits for the CLI, and the block is never polled.

The page scrolls, and its footer is its own key map:

| Key | What it does |
| --- | --- |
| `j / down` | scroll down one line |
| `k / up` | scroll up one line |
| `g / home` | first line |
| `G / end` | last line |
| `enter / q / esc` | back to the list |
| `ctrl-c` | back to the list |

The listing shows 15 rows per page and says `rows 16-30 of 57` when there are
more; `j`/`k` and `g`/`G` move within it. The filter is a case-insensitive
substring match on everything the row is derived from — id, transcript path,
project, state, type, the note and the last error — so `/flaky` finds a session
by the failure that put it there.

#### When the browser opens

`sessions` decides in this order, first match wins:

| Situation | What you get |
| --- | --- |
| `--tui` | the browser, even when stdout is a pipe or a file |
| `--no-tui` | the table, even on a terminal |
| `--json` | the JSON document, even on a terminal |
| stdout is a terminal | the browser |
| anything else — a pipe, a redirect, a cron job, `ssh` without a tty | the table |

`--json` outranks the terminal because a machine-readable document can never be a
full-screen browser. The two flags are for the cases the terminal cannot express:
`--tui` when stdout is a pipe but you still want the browser, `--no-tui` when you
want the plain table on a terminal you happen to be sitting at.

Two combinations are usage errors (exit `2`), rejected before the database is
opened:

```console
$ ccresu sessions --tui --no-tui
ccresu: --tui and --no-tui are mutually exclusive
Run 'ccresu --help' for usage.
$ ccresu sessions --tui --json
ccresu: --tui cannot be combined with --json; --json output is for scripts
Run 'ccresu --help' for usage.
```

The TTY gate is not a second renderer. Piped, redirected and `--json` output is
byte-for-byte what it was before the browser existed, and `--no-tui` on a
terminal prints exactly the bytes a pipe would, so the two can never drift.

The browser takes the alternate screen and hides the cursor on the way in, and it
puts both back on **every** exit path it controls — quit, `ctrl-c`, end of input,
an error, and `SIGTERM`, `SIGINT` or `SIGHUP` from outside, which are turned into
a return so the restore can run. `ctrl-c` is read as a keystroke rather than a
signal (`ISIG` is cleared while the browser runs), which is why it cancels a
running command and exits cleanly instead of killing the process mid-paint. Raw
mode is best-effort: a terminal that refuses it still works, because the decoder
accepts a typed key followed by Enter.

A lone `esc` is a key like any other. Decoding an escape sequence means looking
at the byte after it, and a terminal sends ESC and then waits for you, so the
decoder keeps the escape pending and decides it after 50 ms rather than when the
next keystroke arrives. (`os.Stdin` on a Linux terminal cannot carry a read
deadline — "file type does not support deadline" — which is why the bound is a
timer.)

`SIGKILL` cannot be caught, so a browser killed that way cannot restore anything:
the terminal keeps the alternate screen and no echo until you run `reset` (or
`stty sane`) in it. That is the only exit path that needs you, and it is why a
running command is cancellable — killing the browser is never the right way to
stop one.

The keys come from stdin, so `printf 'q' | ccresu sessions --tui` is a legal way
to script it: the browser draws and then quits at end of input.

### Removing a session

`ccresu sessions delete <id>` forgets a session. It removes ccresu's own records
— the row, its attempts, its state transitions, its usage limits, any pending
reset wait and any queued notifications — and nothing else. The conversation is
not touched: ccresu never deletes a file under `~/.claude`.

Deleting a row alone would not last, because discovery reads the transcripts on
disk and would simply upsert the session back on its next pass. The delete writes
a tombstone, and discovery skips every id it names, so the session stays gone
until you bring it back:

```console
$ ccresu sessions delete 3f9d1c07-1111-4222-8333-444455556666
session:                3f9d1c07-1111-4222-8333-444455556666
state at deletion:      WAITING_FOR_RESET
project:                /home/you/projects/demo
transcript:             /home/you/.claude/projects/-home-you-projects-demo/3f9d1c07-...jsonl
attempts removed:       3
transitions removed:    7
usage limits removed:   1
notifications removed:  2
pending wait:           yes
conversation:           kept: nothing under ~/.claude is touched
session 3f9d1c07-...: deleted; discovery will skip it (undo with 'ccresu enable 3f9d1c07-...')
```

Deleting the same session twice succeeds and says it was already deleted; a
session ccresu has never seen is a not-found (exit 3), so a typo cannot look like
a clean delete. The JSON form (`--json`) reports the same facts, including
`"transcript_kept": true` and the `restore` command.

If the session is running or waiting at the time, the process itself is not
stopped: ccresu deletes its own bookkeeping, and a Claude Code process that is
already running keeps running.

To undo a delete, enable the session again. ccresu rebuilds the row from the
tombstone in `DISCOVERED` state, enabled, and the next discovery pass refreshes
it from the transcript:

```console
$ ccresu enable 3f9d1c07-1111-4222-8333-444455556666
session 3f9d1c07-...: enabled=yes
session 3f9d1c07-...: restored after a delete; discovery will refresh it
```

### Create the configuration

```console
$ ccresu config init
configuration /home/you/.config/ccresu/config.yaml created (mode 0600)

Next steps:
  1. edit /home/you/.config/ccresu/config.yaml
  2. ccresu config validate
  3. ccresu doctor
```

`config init` writes the example configuration embedded in the binary — the same
bytes the release archive and the installer ship — to
`~/.config/ccresu/config.yaml` (or `$XDG_CONFIG_HOME/ccresu/config.yaml`) with
mode `0600`, creating the parent directory `0700` if needed. `config init PATH`,
the global `--config PATH` and `CCRESU_CONFIG` choose a different file.

It refuses to replace an existing file, and says how to proceed:

```console
$ ccresu config init
ccresu: configuration /home/you/.config/ccresu/config.yaml already exists; it was left untouched
to keep it:          edit it, then run 'ccresu config validate'
to replace it:       ccresu config init /home/you/.config/ccresu/config.yaml --force
to use another file: ccresu --config PATH config init
```

The exit code is `1` and the file is untouched. `--force` replaces it with the
shipped example and sets the mode back to `0600`. `--json` reports `path`,
`created`, `replaced`, `mode`, `valid` and `next_steps`.

### Check the environment

```console
$ ccresu doctor
STATUS  CHECK     DETAIL
ok      config    /home/you/.config/ccresu/config.yaml
ok      database  /home/you/.local/share/ccresu/ccresu.db
...
```

`config` and `database` failing together usually means one problem: the database
could not be opened because the configuration could not be loaded. Fix the
configuration first; the remaining checks report `warn  not checked` until then.

### See what is waiting

```console
$ ccresu status
$ ccresu sessions
$ ccresu inspect 4d1f9c2a-7e35-4b18-9a6f-2c8d0e1f3a45
```

`inspect` leads with **when the next attempt is due and why** — an absolute UTC
instant plus a countdown (`at: 2026-11-04 19:29:00 UTC (in 45s)`), or `none` with
the reason. The reasons are the daemon's own words: `session is not enabled`,
`no usage limit detected yet`, `returned to discovery; ccresu will re-read the
transcript and schedule the next reset` (where a failed attempt and a spent
attempt budget both leave it), `recovery in progress`, `already resumed`,
`session completed`, `session was terminated`, `session cannot be recovered`,
`waiting for the reset but no schedule is stored` and `waiting to retry but the
next instant is not stored` — the last two mean the row holding the instant is
gone from the store, so there is nothing to count down to.
A session waiting on a reset shows the instant it was given, with `why:` either
`waiting for the usage limit to reset` — the reset the CLI named — or
`polling until the usage limit clears` when the banner carried no time, which is
why the instant is a poll rather than the reset you read in the message. A
session parked by a failed attempt shows its stored instant with
`why: retrying after a failed attempt` — it is `WAITING_FOR_RESET` like a session
waiting on a reset, and the column that tells them apart is the instant's origin:
`retry_at` for a backoff, the stored schedule for a reset.

Above that block are the session's own columns (`state`, `enabled`,
`recoverable`, `reason`, `last error`, `project`, `transcript`, `type`,
`last seen`); below it, in order, come `Latest usage limit` (`type`, `detected`,
`resets`, `raw` — the phrase as the CLI wrote it, zone included — and
`confidence`), `Latest attempt` (`number`, `state`, `started`, `finished`,
`command`, `error`), `Schedule` (`due`, `unknown reset`, `polls`, `phrase`) and
the last five `Recent transitions` (`WHEN`, `TRANSITION`, `REASON`).

Below the stored records comes the **Supervisor** block. It is the one part of
`inspect` that is not read from ccresu's database: it runs `claude daemon status`
and, when the latest attempt recorded the state id the handoff printed, `claude
logs <state-id>` — on demand, only for `inspect`, and never polled, because the
supervisor owns the process and a second reader of its socket would answer with a
different snapshot one second later.

```console
Supervisor
status:        running
state:         running
version:       2.1.300
workers:       3
socket dir:    /run/user/1000/claude
command:       claude daemon status
supervisor log: last 2 line(s) of `claude daemon logs`
  supervisor started
  respawned worker 1
recent output: last 20 line(s) of `claude logs ab12cd34`
  …
```

`supervisor log` is the tail of `claude daemon logs` — the supervisor's own log,
which is where a restart, a failed handoff or a rotated worker explains itself.
It is read only while the supervisor is running (a stopped supervisor has no log
to follow), and a failed read is reported with its own words rather than shown as
an empty log.

`status` is one of four words, because four different facts lead to four
different actions:

| `status` | what it means | what to do |
|---|---|---|
| `running` | the supervisor answered; its `state`, `version`, `workers` and socket directory follow | nothing |
| `not running` | the documented exit-1 answer: this machine has no supervisor yet | nothing — the next handoff starts one |
| `unavailable` | this build's provider has no supervisor surface (a stub, or `--force` print mode) | nothing to ask; the rest of the page is still accurate |
| `unreadable` | the command was run and failed for another reason (timeout, crash, a `claude` without the `daemon` subcommand); `error` carries its own words | read `error`; the supervisor's state is unknown, not empty |

`recent output` is the last 20 lines of `claude logs <state-id>`, where
`<state-id>` is the short id the handoff printed and ccresu stored on the
attempt. When the latest attempt carries no supervisor state id, the block says
so instead of printing an empty log, and when the session is not hosted by the
supervisor at all (`status: not running`) no log read is attempted. With
`--json`, the same read appears as a `supervisor` object: `status`, `state`,
`version`, `workers`, `socket_dir`, `argv`, `log_target`, `recent_output`,
`daemon_output`, `daemon_error` and `error` (all omitted when empty).

`sessions` carries the same answer as its `NEXT ATTEMPT` column: `in 2h14m` while
a wait is pending, `none (<reason>)` when nothing is scheduled. Both read the
stored schedule or the stored retry instant rather than re-deriving a time, so
what is printed is what the daemon will do. With `--json`, `sessions` adds
`next_attempt_at` (RFC 3339, `null` when nothing is scheduled) and
`next_attempt_reason` to each row, and `inspect` adds a `next_attempt` object
holding `scheduled`, `at`, `reason` and a ready-made `human` countdown.

### Resume now

```console
$ ccresu recover 4d1f9c2a-7e35-4b18-9a6f-2c8d0e1f3a45
$ ccresu recover 4d1f9c2a-... --message "finish the migration and stop"
$ ccresu recover 4d1f9c2a-... --force      # budget spent, try anyway
```

A manual recovery goes through the same state machine, locking and verification
as an automatic one: it is not a bypass.

A resume can also be refused because the session is still attached to Claude
Code's background agent
(`That session is running in the background (837eaaea) …`). ccresu treats that as
"the conversation is busy", not as "the conversation is gone": inside the same
attempt it runs `claude stop <job-id>`, then repeats the *identical*
`claude --bg --resume <session-id> …` handoff once. `<job-id>` is the eight lower-case
hex job name the refusal itself prints (`837eaaea`), not the session UUID:
`claude stop` resolves job names only, and answers a UUID with
`No job matching` and exit 1. The stop keeps the conversation — same
session id, same transcript — and it goes to the log rather than to the attempt
row, so `ccresu history` still shows one resume command. If the stop fails or the
second resume refuses again, the original error is reported unchanged, exactly as
before. See [recovery.md](recovery.md).

#### When the limit has not reset yet

The other refusal is the one the limit itself causes: a
`--bg --resume` handoff exits 1 with the banner the CLI prints.

```console
Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin).
```

ccresu reads that as a wait, not as a failure — and never as
`claude executable unavailable`. The attempt is finished with the banner, the
banner is recorded as a usage limit (so `Latest usage limit` shows the CLI's own
phrase next to the resolved instant), the next attempt is scheduled at the reset
instant the banner named, and the session stays in `WAITING_FOR_RESET` until
then. Nothing is retried in the meantime: five reconcile passes inside one window
issue exactly one `--bg --resume` handoff, and the next one is issued after the
reset.

```console
$ ccresu inspect 1347e5db-a9fb-42eb-b92a-82256bd5aa68
state:         WAITING_FOR_RESET
last error:    Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin).
…

Next attempt
at:            2026-01-06 09:00:00 UTC (in 2h1m)
why:           waiting for the usage limit to reset

Latest usage limit
type:          other
resets:        2026-01-06 17:00:00 +08
raw:           10am (Europe/Berlin)
confidence:    0.95
…
```

`at` is the reset the banner named, not a backoff, and `why` names the wait, so
the `NEXT ATTEMPT` column of `ccresu sessions` (`in 2h1m`) and the detail page
tell the same story. When the banner names no reset time, the schedule falls back
to the usual poll with backoff — still without resuming in the meantime.
`ccresu recover <id>` asks the CLI again right away; if the limit has not cleared
it refuses, and the session is parked again with the same banner. See
[recovery.md](recovery.md).

### Stop a session from being resumed

```console
$ ccresu disable 4d1f9c2a-...
$ ccresu enable  4d1f9c2a-...
```

### Audit a wait

```console
$ ccresu history 4d1f9c2a-... --limit 20
```

Each transition carries the reason it was recorded with, which is the audit
trail for a wait. The proof of a recovery lives on the attempt, not in the table:
`ccresu recover` prints it as
`verification: proven: the continuation was handed to the background supervisor`
— or `proven: the supervisor published state "working" for the conversation`
when a published state is what established it — and `not proven: …`, naming what
is missing, otherwise. `ccresu history --json` carries the same evidence per
attempt: `HandoffAccepted`/`HandoffStateID`, `PublishedState`, and the recorded
observation (`ObservedState`, `ObservedStatus`, `ObservedWaitingFor`). The human
attempt table shows `RESULT` (`in progress`, `succeeded`, `failed`) instead.

### Machine-readable output

```console
$ ccresu --json status
$ ccresu sessions --json --state WAITING_FOR_RESET
```

`--json` emits a single JSON document per command so a script can branch on
fields instead of parsing tables.

### Run the API by hand

```console
$ ccresu serve --addr 127.0.0.1:8787
```

The token is created on first start at `<configuration directory>/api-token` with
mode `0600` — the directory of the configuration file in use, so
`ccresu --config /elsewhere/config.yaml serve` writes `/elsewhere/api-token`, and
the default configuration puts it in `~/.config/ccresu`. Set `server.token_path`
to name the file explicitly.

```bash
TOKEN=$(cat ~/.config/ccresu/api-token)
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8787/api/v1/status
```

See [api.md](api.md).

## Configuration and logging flags

```console
$ ccresu -c ./testdata/config.yaml status
$ ccresu --log-level debug --log-format text daemon --once
```

`--log-format text` is for a terminal; `json` (the default) is for journald and
log shippers. Secrets are redacted in both.

## Supervising long runs

`ccresu daemon` is meant to be supervised. Use the shipped user unit
(`--systemd` in the installer), or run it under your own supervisor; see
[deployment.md](deployment.md).
