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
| `sessions` | `sessions [--all] [--state STATE]... [--limit N]` | Lists discovered sessions. A bare listing is **enabled only** and says so in the footer; `--all` includes disabled and settled ones; `--state` is repeatable; `--limit 0` means all. |
| `sessions status` | `sessions status <session-id>` | Everything known about one session: state, paths, the next attempt, the latest usage limit, the latest attempt, any pending wait and recent transitions. Identical to `inspect`, which is kept as an alias. |
| `sessions delete` | `sessions delete <session-id>` | Removes the session from ccresu: the row, its attempts, transitions, usage limits, pending wait and notifications. Nothing under `~/.claude` is touched — the conversation stays where it is. A tombstone keeps discovery from re-creating the session; `ccresu enable <id>` undoes it. Deleting twice is a success, not an error. |
| `inspect` | `inspect <session-id>` | Everything known about one session. The same code as `sessions status`. |
| `recover` | `recover <session-id> [--message TEXT] [--force]` | Resumes one session now, optionally with a different continuation message. `--force` tries even when the attempt budget is spent. |
| `retry` | `retry <session-id>` | Retries a session whose attempt budget is spent. |
| `reset` | `reset <session-id>` | Clears a `PERMANENTLY_FAILED` session: the stored error goes away and the session becomes `DISABLED`. It never resumes anything and never refunds an attempt. |
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
state at deletion:      PERMANENTLY_FAILED
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

## Common workflows

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
`claude -p --resume <session-id> …` once. `<job-id>` is the eight lower-case
hex job name the refusal itself prints (`837eaaea`), not the session UUID:
`claude stop` resolves job names only, and answers a UUID with
`No job matching` and exit 1. The stop keeps the conversation — same
session id, same transcript — and it goes to the log rather than to the attempt
row, so `ccresu history` still shows one resume command. If the stop fails or the
second resume refuses again, the original error is reported unchanged, exactly as
before. See [recovery.md](recovery.md).

### Clear a permanently failed session

```console
$ ccresu reset 7c1b2a44-0000-4000-8000-abcdefabcdef
session:       7c1b2a44-0000-4000-8000-abcdefabcdef
state:         DISABLED
previous state: PERMANENTLY_FAILED
error:         cleared
attempts:      kept (use 'ccresu retry' to spend another)
session 7c1b2a44-0000-4000-8000-abcdefabcdef: reset; run 'ccresu enable 7c1b2a44-0000-4000-8000-abcdefabcdef' to let ccresu recover it again
```

`reset` takes a session out of `PERMANENTLY_FAILED` and nothing else. It clears
the stored error, records the transition with the reason `operator reset a
permanently failed session`, and leaves the session `DISABLED` so nothing resumes
it until you say so. It does **not** resume the session, and it does **not** give
back an attempt: the attempt history and the counter are untouched. That split is
deliberate:

| You want | Command |
| --- | --- |
| Another go now | `ccresu recover <id> --force` |
| A full fresh attempt budget | `ccresu retry <id>` |
| The failure forgotten and the session out of the way | `ccresu reset <id>` |
| It recoverable again, with whatever budget it has left | `ccresu reset <id>` then `ccresu enable <id>` |

Every other state is refused with exit code `4` and a message that names the
state and the command to use instead — `UNRECOVERABLE` has no legal edge at all,
`COMPLETED` and `USER_TERMINATED` carry no failure to clear, and a `DISABLED`
session is already where `reset` would put it. An unknown session is exit `3`
(`not found`), and a missing or extra argument is exit `2` (`usage`). `--json`
reports `session_id`, `state`, `previous_state`, `enabled` and `reason`.

### Stop a session from being resumed

```console
$ ccresu disable 4d1f9c2a-...
$ ccresu enable  4d1f9c2a-...
```

### Audit a wait

```console
$ ccresu history 4d1f9c2a-... --limit 20
```

Attempts come with the two proofs (session id preserved, transcript grew) and
the transitions come with their recorded reason.

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
