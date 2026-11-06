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

Exit codes: `0` ok, `1` failure, `2` usage, `3` not found, `4` not eligible,
`130` interrupted.

## Commands

| Command | Usage | What it does |
| --- | --- | --- |
| `daemon` | `daemon [--once] [--interval DURATION]` | Runs the watchdog loop until interrupted. `--once` performs a single reconcile pass and exits; `--interval` overrides `claude.discovery_interval`. |
| `status` | `status` | Summarises session, schedule and notification state. Its `sessions` line is the **total** row count, and its `listing` line says how many of those a bare `sessions` shows. |
| `sessions` | `sessions [--all] [--state STATE]... [--limit N] [--tui\|--no-tui]` | Lists discovered sessions. On a terminal it opens the interactive browser (see [Browsing the sessions](#browsing-the-sessions)); a bare listing is **enabled only** and says so in the footer; `--all` includes disabled and settled ones; `--state` is repeatable; `--limit 0` means all. Redirected output, `--json` and `--no-tui` always print the table. |
| `sessions status` | `sessions status <session-id>` | Everything known about one session: state, paths, the next attempt, the latest usage limit, the latest attempt, any pending wait and recent transitions. Identical to `inspect`, which is kept as an alias. |
| `sessions delete` | `sessions delete <session-id>` | Removes the session from ccresu: the row, its attempts, transitions, usage limits, pending wait and notifications. Nothing under `~/.claude` is touched — the conversation stays where it is. A tombstone keeps discovery from re-creating the session; `ccresu enable <id>` undoes it. Deleting twice is a success, not an error. |
| `inspect` | `inspect <session-id>` | Everything known about one session. The same code as `sessions status`. |
| `recover` | `recover <session-id> [--message TEXT] [--force]` | Resumes one session now, optionally with a different continuation message. `--force` tries even when the attempt budget is spent. |
| `retry` | `retry <session-id>` | Retries a session whose attempt budget is spent. |
| `enable` | `enable <session-id>` | Allows ccresu to recover a session. It is also the undo for `sessions delete`: a deleted session is restored (tombstone removed, row re-created in `DISCOVERED`) before it is enabled. |
| `disable` | `disable <session-id>` | Stops ccresu from recovering a session. |
| `history` | `history <session-id> [--limit N]` | Attempts and state transitions (default `--limit 50`). |
| `config` | `config init [PATH] [--force]` / `config validate [PATH]` / `config show` | Creates the configuration from the shipped example, validates a file, or prints the effective one. |
| `doctor` | `doctor` | Checks the environment ccresu runs in. |
| `serve` | `serve [--addr HOST:PORT]` | Runs the local REST/SSE API (default `server.host:server.port`). |
| `version` | `version` | Prints version and build metadata. |

`ccresu --version` and `ccresu --help` work without a subcommand.

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
state at deletion:      RETRY_WAIT
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
the reason (`session is not enabled`, `attempt budget spent; use 'ccresu retry'`,
`no usage limit detected yet`). It then shows the last usage limit, the resolved
reset instant with the zone it was written in, the schedule and the notification
state — enough to explain why a wait is where it is.

`sessions` carries the same answer as its `NEXT ATTEMPT` column: `in 2h14m` while
a wait is pending, `none (<reason>)` when nothing is scheduled. Both read the
stored schedule or the stored retry instant rather than re-deriving a time, so
what is printed is what the daemon will do. With `--json`, `sessions` adds
`next_attempt_at` (RFC 3339, `null` when nothing is scheduled) and
`next_attempt_reason` to each row, and `inspect` adds a `next_attempt` object
holding `scheduled`, `at`, `reason` and a ready-made `human` countdown.

### Browsing the sessions

```console
$ ccresu sessions            # a terminal: opens the browser
$ ccresu sessions --no-tui   # a terminal: prints the table anyway
$ ccresu sessions | cat      # a pipe: prints the table, automatically
$ ccresu sessions --json     # JSON, on a terminal or not
```

The browser is chosen only when stdout is a terminal. Redirecting it, piping it or
asking for `--json` prints exactly the table and the JSON document described
above, byte for byte, so a script that versions `ccresu` never has to learn about
the browser. `--tui` and `--no-tui` are the explicit overrides; `--tui` is useful
inside an emulator or a wrapper, and `--tui --json` and `--tui --no-tui` are
usage errors rather than a silent pick.

```console
ccresu sessions — 3 shown (enabled only; 'a' shows all)
  SESSION   NAME        STATE              NEXT ATTEMPT  ENABLED  PROJECT        LAST SEEN                NOTE
> 3f9d1c07  flaky build WAITING_FOR_RESET  in 1m20s      yes      /work/project  2026-11-05 03:43:40 UTC  -
  1a2b3c4d  -           DISCOVERED         none (sessio…  yes      /work/api      2026-11-05 03:24:03 UTC  -

? help  / filter  i inspect  e enable  d disable  r retry  R recover  x delete  a all  q quit
```

| Key | What it does |
| --- | --- |
| `j` / `down` | next session |
| `k` / `up` | previous session |
| `g` / `home` | first session |
| `G` / `end` | last session |
| `/` | filter by text (`enter` applies, `esc` cancels) |
| `c` | clear the filter |
| `a` | toggle `--all` (disabled and settled sessions) |
| `i` / `enter` | inspect the selected session |
| `e` | enable the selected session |
| `d` | disable the selected session |
| `r` | retry the selected session |
| `R` | recover the selected session now |
| `x` | delete the selected session (asks `y`/`N`) |
| `?` | this key map |
| `q` / `ctrl-c` | quit |

The browser is a *view*, not a second implementation. `i` renders the page
`sessions status` prints (same function), and every action is the call the
matching command makes: `e`/`d` are `enable`/`disable`, `r` is `retry`, `R` is
`recover`, `x` is `sessions delete`. So:

- it never creates a session, and it only ever shows rows ccresu has already
  discovered — `claude` sessions on disk that discovery has not imported are not
  visible here, exactly as they are not visible in the table;
- `x` forgets ccresu's own row (and leaves a tombstone so discovery will not
  re-create it). Nothing under `~/.claude` is read, written or deleted — the
  conversation stays where it is, and `ccresu enable <id>` restores the row;
- a refused action (`recover` of a disabled session, `retry` with the budget
  spent) is reported on the status line and the browser stays open on the same
  row, since quitting on a refusal would throw away your place in the list.

The filter matches the session id, the name `claude agents` reported, the project
path, the state, the job id and the note, so `/flaky` finds a row by any of them.

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
`claude --resume <session-id> --bg …` dispatch once. `<job-id>` is the eight
lower-case hex job name the refusal itself prints (`837eaaea`), not the session
UUID: `claude stop` resolves job names only, and answers a UUID with
`No job matching` and exit 1. The stop keeps the conversation, and it goes to the
log rather than to the attempt row, so `ccresu history` still shows one dispatch
command. If the stop fails or the second dispatch refuses again, the original
error is reported unchanged, exactly as before.

A dispatch succeeds by acknowledging, not by answering: `claude --resume <id> --bg`
prints `backgrounded · <job-id>` and returns immediately. ccresu follows that job
through `claude agents --all --json` until it stops working, records the session
id the job continues as the row's **continuation**, and proves the resume by that
continuation's transcript growth. Cost, turns and duration are not reported — the
dispatch cannot report them. See [recovery.md](recovery.md).

### Stop a session from being resumed

```console
$ ccresu disable 4d1f9c2a-...
$ ccresu enable  4d1f9c2a-...
```

### Audit a wait

```console
$ ccresu history 4d1f9c2a-... --limit 20
```

Attempts come with the two proofs (`continuation_confirmed`, `transcript_grew`)
and the transitions come with their recorded reason.

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
