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

The one exception is a key this release has *retired*: a file written for an
older version still loads, the key is dropped before the strict decode, and the
load prints a warning naming it (`telegram.notify_needs_input is no longer a
configuration key: it is ignored, not rejected …`) so the stale line can be
deleted. A file carrying several of them gets one warning that lists them all.
The list is closed and permanent:

| Retired key | Withdrawn in | Why it is gone |
| --- | --- | --- |
| `telegram.notify_needs_input` | 0.9.0 | A `blocked` session is resumed rather than announced, so there is no needs-input message to switch on. |
| `scheduler.recheck_before_resume` | 0.10.0 | A usage window can only be learned from the resume itself, so the resume *is* the recheck. |
| `recovery.startup_timeout` | 0.10.0 | It bounded watching a child start; a resume is one blocking command. |
| `recovery.verification_timeout` | 0.10.0 | It bounded polling a transcript for growth; the transcript is now measured once, after the turn, as a diagnostic. |
| `recovery.health_check_interval` | 0.10.0 | It was the gap between those polls, which no longer happen. |
| `recovery.guardrails.model` | 0.10.0 | It rode the retired handoff vector as a CLI flag, and a resume carries no flags at all. |
| `recovery.guardrails.effort` | 0.10.0 | Same: it rode the handoff vector. |
| `recovery.guardrails.fallback_model` | 0.10.0 | Same: it rode the handoff vector. |
| `recovery.guardrails.permission_mode` | 0.10.0 | Same: it rode the handoff vector. |

The two spend caps in the same `recovery.guardrails` section —
`max_cost_usd_per_session` and `max_cost_usd_per_attempt` — are **not** retired:
they are ccresu's own checks over the series of attempts rather than CLI flags, so
a value left in an old file still applies. Strictness is unchanged everywhere
else: a typo of a retired key (`notify_needs_inputt`) and a key that never existed
both still fail.

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
| `default_model` | `""` | Accepted and validated, but not applied: a resume carries no model flag, so the session's own model stands. |
| `default_effort` | `""` | Accepted and validated, but not applied, for the same reason. |
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
| `rescan_resumed` | `true` | Extends the transcript scan to the one state no other pass reads again: `RESUMED` is Settled, so nothing revisits that transcript, and a limit that lands after a resume would otherwise be invisible for ever. A limit found there goes `RESUMED → DISCOVERED → WAITING_FOR_RESET` on the reset the banner names — through `DISCOVERED`, because the machine has no `RESUMED → WAITING_FOR_RESET` edge — so a rescan can only ever park a session. It has no edge to `RESUMING`, so it cannot spend, and cannot resume a finished conversation twice. `DISCOVERED` is scanned regardless. Set it to `false` (or `CCRESU_MONITORING__RESCAN_RESUMED=false`) to restore the pre-0.8.0 behaviour, in which a limit that landed after a resume was only ever found by an operator running `ccresu search-limit`. |

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

There is no pre-resume recheck switch. A usage window can only be learned from a
`claude` run, so the resume is the recheck: a refusal parks the session on the
reset its banner named and spends no attempt, which is why nothing needs to ask
the question twice. `scheduler.recheck_before_resume`, documented before 0.10.0,
was never read by any release and is now retired.

### `recovery`

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Global switch. |
| `max_attempts` | `10` | Attempt budget for one discovery cycle; a session that spends it returns to `DISCOVERED` and gets a fresh budget on the next cycle. |
| `initial_delay` | `30s` | First retry delay. |
| `max_delay` | `15m` | Retry delay ceiling. |
| `multiplier` | `2.0` | Exponential growth factor. |
| `jitter` | `0.15` | ±15% randomisation so retries do not synchronise. |
| `turn_timeout` | `30m` | The one timeout a resume has: `claude --resume <session-id> "<continuation message>"` is a blocking turn, and this bounds the whole of it (`claude.DefaultTurnTimeout`). A turn that overruns the bound is killed and recorded as a failed attempt with its exit code and stderr, then retried on the backoff schedule like any other failure. |
| `max_concurrent_recoveries` | `3` | Parallel resumes. |

These five retry-policy values are validated, never silently clamped: a
`multiplier` below 1, a `jitter` outside `[0,1)`, a negative delay, or a
`max_delay` shorter than `initial_delay` makes `config validate` fail and the
daemon refuse to start. The same rules apply to the policy struct the recovery
manager is constructed with, so a caller that builds a `Backoff` in code gets the
error rather than a quietly repaired schedule.

#### `recovery.guardrails`

PRD 5's guardrails. Every key is optional, and an unset key means the behaviour
ccresu had before the section existed: no flag is added and no check runs.

| Key | Default | Meaning |
| --- | --- | --- |
| `max_cost_usd_per_session` | `0` (off) | Before an attempt starts, ccresu sums the costs the CLI reported for this session's attempts and refuses to start when the sum has reached this figure. The refusal spends nothing: the check runs before an attempt row exists. |
| `max_cost_usd_per_attempt` | `0` (off) | After an attempt resolves, a recorded cost above this figure stops automatic recovery for the session. It guards the case the session cap cannot see: one very expensive turn. |

These two caps are the whole section, and ccresu enforces both itself: the
session cap before an attempt is started, the attempt cap after one resolves. No
guardrail option is passed to the CLI — the resume command is
`claude --resume <session-id> "<continuation message>"` and nothing else.

A session stopped by either cap is `DISABLED`, which no reconcile pass resumes —
and a spend stop and an operator's `ccresu disable` are deliberately the same
state, because both mean "nothing automatic happens until a human decides". The
way back is `ccresu enable <id>` once the cap is raised or reset; an explicit
`ccresu recover <id>` or `ccresu retry <id>` is allowed past the cap without
enabling the session.
The caps apply to automatic recovery only: an explicit `ccresu recover` or
`ccresu retry` is a human act and is allowed past the cap, though its cost is
still recorded and the override is logged.

A cost the CLI did not report is recorded as *unknown*, not as zero: it neither
counts toward a cap nor blocks a resume. The figure is the CLI's own client-side
estimate the CLI itself records, so it can differ from the
bill and spend can pass the cap — leave headroom.

### `telegram`

See [telegram.md](telegram.md). Keys: `enabled` (`false`), `bot_token`, `chat_id`,
`parse_mode` (`HTML` — the only accepted value; `MarkdownV2` and `Markdown` are
rejected at config validation because the templates emit HTML), and the
`notify_*` toggles (`limit_detected`,
`waiting`
(default `false`), `recovery_started`, `recovery_success`,
`recovery_failure`, `permanent_failure`, `session_completed` — the rest `true` by
default). There is no needs-input switch: a session that stops waiting on a human is
resumed rather than announced.

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
