# Configuration

ccresu reads one YAML file. Everything it does not set falls back to a default,
so a file written for an older release keeps working after an upgrade.

## Where the file lives

Resolution order, first match wins:

1. `--config PATH` / `-c PATH` on the command line,
2. `CCRESU_CONFIG` in the environment,
3. `$XDG_CONFIG_HOME/ccresu/config.yaml`, else `~/.config/ccresu/config.yaml`.

A leading `~` is expanded by ccresu itself. The annotated reference is
`configs/config.example.yaml`; `ccresu config init` writes exactly those bytes —
the copy embedded in the binary — to the resolved path with mode `0600`. The
installer does the same thing, so either route leaves you with the same file.

```console
$ ccresu config init
configuration /home/you/.config/ccresu/config.yaml created (mode 0600)

Next steps:
  1. edit /home/you/.config/ccresu/config.yaml
  2. ccresu config validate
  3. ccresu doctor
```

`config init [PATH]` and the global `--config PATH` both override the resolved
path, and missing parent directories are created (`0700`). An existing file is
left untouched and the command exits `1`; `--force` replaces it with the shipped
example and re-tightens the mode to `0600`. `--json` reports `path`, `created`,
`replaced`, `mode`, `valid` and `next_steps` for scripts.

## Validate, then trust it

```console
$ ccresu config validate
configuration /root/.config/ccresu/config.yaml is valid
app:           ccresu
timezone:      Asia/Singapore
database:      /root/.local/share/ccresu/ccresu.db
monitoring:    yes
recovery:      yes (max attempts 10)
telegram:      no
server:        yes (127.0.0.1:8787)
```

`ccresu config validate PATH` checks a specific file. `ccresu config show`
prints the effective configuration after defaults and environment overrides.

Validation is strict and is *never* silently ignored: an unknown key, a wrong
type, a negative duration, an unparsable timezone, a non-absolute path where one
is required, or an out-of-range value is fatal and the message names the field
(e.g. `server.token_path must resolve to a file path`).

## Environment overrides

Any field can be set with an environment variable: prefix `CCRESU_`, the YAML
path in upper case, with `__` between levels.

```bash
CCRESU_APP__LOG_LEVEL=debug
CCRESU_RECOVERY__MAX_ATTEMPTS=5
CCRESU_TELEGRAM__BOT_TOKEN=123:abc
CCRESU_SERVER__PORT=9000
CCRESU_CLAUDE__MONITORED_SESSIONS=id-one,id-two   # lists are comma separated
```

Environment variables take precedence over the file, which takes precedence over
the defaults. Durations accept Go syntax (`30s`, `5m`, `1h30m`); booleans accept
`true/false/1/0`; a list is split on commas.

For the three Telegram settings the plain variables `TELEGRAM_ENABLED`,
`TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` are honoured as aliases, with the
`CCRESU_TELEGRAM__*` names winning when both are set.

## Sections

### `app`

| Key | Default | Meaning |
| --- | --- | --- |
| `name` | `ccresu` | Shown in logs. |
| `log_level` | `info` | `debug`, `info`, `warn`, `error`. |
| `log_format` | `json` | `json` or `text`. |
| `timezone` | `UTC` | IANA zone used for display only; each reset keeps its own zone. |

### `claude`

| Key | Default | Meaning |
| --- | --- | --- |
| `executable` | `claude` | The CLI to drive; must be on `PATH` or absolute. |
| `discovery_interval` | `30s` | How often the daemon reconciles. |
| `monitored_sessions` | `[]` | Extra session ids to watch beyond auto-discovery. |
| `discover_all_sessions` | `true` | Discover every session, not just the current directory's. |
| `resume_enabled` | `true` | Global switch for resuming. |
| `default_model` | `""` | Empty means inherit the original session's model. |
| `default_effort` | `""` | Empty means inherit the original session's effort. |
| `continuation_message` | see below | The prompt sent when resuming. |

The default continuation message is:

```text
Continue working on the existing active goal from where execution was interrupted
by the usage limit. Review the current state, avoid repeating completed work, and
continue until the goal is completed.
```

It is capped at 8 KiB (`claude.MaxMessageBytes`); a longer value is rejected
rather than handed to a command line.

### `monitoring`

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Master switch for the watchdog. |
| `detect_usage_limits` | `true` | Recognise usage-limit interruptions. |
| `detect_session_completion` | `true` | Recognise normal completion so a finished session is not resumed. |

### `detector`

| Key | Default | Meaning |
| --- | --- | --- |
| `threshold` | `0.5` | Minimum confidence for a match to count. Canonical banners score 0.95–0.99; raising this above 0.99 disables them. |
| `custom_patterns` | `[]` | Extra rules, matched against normalised text. |

A custom pattern has `id`, `pattern` (a Go regular expression), optional `kind`
(`five_hour`, `weekly`, `other`), `confidence` in `(0,1]`, `requires_reset` and a
human `description`. A rule with an empty id, an invalid expression or a
confidence outside the range fails validation at startup.

```yaml
detector:
  custom_patterns:
    - id: internal-proxy-limit
      kind: five_hour
      pattern: 'proxy quota exhausted'
      confidence: 0.9
      requires_reset: false
      description: our corporate proxy reports the limit differently
```

### `scheduler`

| Key | Default | Meaning |
| --- | --- | --- |
| `poll_interval` | `30s` | How often due schedules are checked. |
| `unknown_reset_initial_interval` | `5m` | First re-poll when the message carried no reset time. |
| `unknown_reset_max_interval` | `30m` | Ceiling for the exponential re-poll. |
| `recheck_before_resume` | `true` | Re-read the session's state immediately before resuming. |

### `recovery`

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Global switch. |
| `max_attempts` | `10` | Attempt budget per session before `PERMANENTLY_FAILED`. |
| `initial_delay` | `30s` | First retry delay. |
| `max_delay` | `15m` | Retry delay ceiling. |
| `multiplier` | `2.0` | Exponential growth factor. |
| `jitter` | `0.15` | ±15% randomisation so retries do not synchronise. |
| `startup_timeout` | `60s` | How long a resumed child may take to show life. |
| `verification_timeout` | `120s` | How long the verifier watches for transcript growth. |
| `health_check_interval` | `10s` | Liveness polling of a running attempt. |
| `max_concurrent_recoveries` | `3` | Parallel resumes. |

These five retry-policy values are validated, never silently clamped: a
`multiplier` below 1, a `jitter` outside `[0,1)`, a negative delay, or a
`max_delay` shorter than `initial_delay` makes `config validate` fail and the
daemon refuse to start. The same rules apply to the policy struct the recovery
manager is constructed with, so a caller that builds a `Backoff` in code gets the
error rather than a quietly repaired schedule.

### `telegram`

See [telegram.md](telegram.md). Keys: `enabled` (`false`), `bot_token`, `chat_id`,
`parse_mode` (`HTML` — the only accepted value; `MarkdownV2` and `Markdown` are
rejected at config validation because the templates emit HTML), and the
`notify_*` toggles (`limit_detected`,
`waiting`
(default `false`), `recovery_started`, `recovery_success`,
`recovery_failure`, `permanent_failure`, `session_completed` — all `true` by
default).

### `server`

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Run the API with the daemon. |
| `host` | `127.0.0.1` | Bind address. Loopback by design. |
| `port` | `8787` | Port. |
| `allowed_origins` | `http://127.0.0.1:8787`, `http://localhost:8787` | Browser origins permitted to read the API. |
| `token_path` | empty → `<configuration directory>/api-token` | Where the bearer token lives. Empty means beside the configuration file in use, so `--config /elsewhere/config.yaml` gives `/elsewhere/api-token`. |

### `storage`

| Key | Default | Meaning |
| --- | --- | --- |
| `driver` | `sqlite` | Only SQLite is supported. |
| `path` | `~/.local/share/ccresu/ccresu.db` | Database file. |
| `busy_timeout` | `5s` | How long to wait for a locked database. |

## A minimal configuration

```yaml
app:
  log_level: info

telegram:
  enabled: true
  bot_token: ""      # prefer CCRESU_TELEGRAM__BOT_TOKEN
  chat_id: ""
```

Everything else keeps its default, including the database location and the
loopback API.

## Permissions

The configuration may contain a bot token, so the installer creates it `0600`
inside a `0700` directory. If you create it by hand:

```bash
install -d -m 0700 ~/.config/ccresu
install -m 0600 configs/config.example.yaml ~/.config/ccresu/config.yaml
```

`ccresu config init` is the shorter equivalent and needs no checkout: the example
is embedded in the binary, so it writes the same bytes with mode `0600`.

## Applying changes

```bash
$EDITOR ~/.config/ccresu/config.yaml
ccresu config validate
systemctl --user restart ccresu.service   # if you installed the unit
```

Restarting is always safe: the wait lives in the database, so the new process
picks up an in-flight schedule instead of losing it.
