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

Exit codes: `0` ok, `1` failure, `2` usage, `3` not found, `4` not eligible,
`130` interrupted.

## Commands

| Command | Usage | What it does |
| --- | --- | --- |
| `daemon` | `daemon [--once] [--interval DURATION]` | Runs the watchdog loop until interrupted. `--once` performs a single reconcile pass and exits; `--interval` overrides `claude.discovery_interval`. |
| `status` | `status` | Summarises session, schedule and notification state. Its `sessions` line is the **total** row count, and its `listing` line says how many of those a bare `sessions` shows. |
| `sessions` | `sessions [--all] [--state STATE]... [--limit N]` | Lists discovered sessions. A bare listing is **enabled only** and says so in the footer; `--all` includes disabled and settled ones; `--state` is repeatable; `--limit 0` means all. |
| `inspect` | `inspect <session-id>` | Everything known about one session. |
| `recover` | `recover <session-id> [--message TEXT] [--force]` | Resumes one session now, optionally with a different continuation message. `--force` tries even when the attempt budget is spent. |
| `retry` | `retry <session-id>` | Retries a session whose attempt budget is spent. |
| `enable` | `enable <session-id>` | Allows ccresu to recover a session. |
| `disable` | `disable <session-id>` | Stops ccresu from recovering a session. |
| `history` | `history <session-id> [--limit N]` | Attempts and state transitions (default `--limit 50`). |
| `config` | `config init [PATH] [--force]` / `config validate [PATH]` / `config show` | Creates the configuration from the shipped example, validates a file, or prints the effective one. |
| `doctor` | `doctor` | Checks the environment ccresu runs in. |
| `serve` | `serve [--addr HOST:PORT]` | Runs the local REST/SSE API (default `server.host:server.port`). |
| `version` | `version` | Prints version and build metadata. |

`ccresu --version` and `ccresu --help` work without a subcommand.

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
and the global `--config PATH`, choose a different file.

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

The token is created on first start at `<config dir>/api-token` with mode `0600`:

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
