# Tutorial: your first recovered session

A hands-on walkthrough for a new install: create a configuration, run the
watchdog, let it detect a usage limit, watch the automatic resume, then drive the
same state by hand and over the API. Every command below is real output from the
binary on a scratch `HOME`, so paths and session IDs are examples — yours will
differ.

The same walkthrough is in the project README; this page is the long-lived copy.
It assumes you have installed ccresu (`scripts/install.sh`, `go install`, or a
build from a checkout) and that `claude` is on `PATH` with at least one session
under `~/.claude/projects`.

> `--config PATH` selects a configuration file other than
> `~/.config/ccresu/config.yaml` (so does `CCRESU_CONFIG` in the environment),
> and `--json` switches a command to machine-readable output. Both are global
> flags, so they go before the subcommand: `ccresu --config ./test.yaml
> sessions`, not `ccresu sessions --config ./test.yaml`.

## Step 1 — Create the configuration

`config init` writes the example configuration the binary carries — the same
bytes the installer copies — to the default path, readable only by you.

```console
$ ccresu config init
configuration /home/you/.config/ccresu/config.yaml created (mode 0600)

Next steps:
  1. edit /home/you/.config/ccresu/config.yaml
  2. ccresu config validate
  3. ccresu doctor
```

The default path is `~/.config/ccresu/config.yaml`; `XDG_CONFIG_HOME` moves it,
and the global `--config PATH` flag, the `CCRESU_CONFIG` environment variable or
a positional argument names the file explicitly:

```bash
ccresu --config /tmp/ccresu/config.yaml config init
ccresu config init /tmp/ccresu/config.yaml
```

Parent directories are created, and the file is `0600`. An existing file is never
replaced silently:

```console
$ ccresu config init
ccresu: configuration /home/you/.config/ccresu/config.yaml already exists; it was left untouched
to keep it:          edit it, then run 'ccresu config validate'
to replace it:       ccresu config init /home/you/.config/ccresu/config.yaml --force
to use another file: ccresu --config PATH config init
```

That is exit code 1, with the file untouched. `--force` replaces it;
`config init --json` reports `path`, `created`, `replaced`, `mode`, `valid` and
`next_steps` for scripts.

## Step 2 — Validate the configuration

ccresu reads one YAML file. Every field is validated before anything runs, so a
typo is a startup error rather than a silent default.

```console
$ ccresu config validate
configuration /home/you/.config/ccresu/config.yaml is valid
app:           ccresu
timezone:      Asia/Singapore
database:      /home/you/.local/share/ccresu/ccresu.db
monitoring:    yes
recovery:      yes (max attempts 10)
telegram:      no
server:        yes (127.0.0.1:8787)
```

`config validate [PATH]` also takes an explicit path, which is handy in CI:

```bash
ccresu config validate ./configs/config.example.yaml
```

`ccresu config show` prints the *effective* configuration after defaults and
`CCRESU_*` environment overrides are applied. If the file is missing you get a
non-zero exit and a precise reason; [configuration.md](configuration.md) lists
every field.

## Step 3 — Check the environment

`ccresu doctor` is the pre-flight check: the config, the timezone, the data
directory, the database schema, the Claude Code CLI, transcript access, Telegram
and discovery.

```console
$ ccresu doctor
STATUS  CHECK           DETAIL
ok      config          /home/you/.config/ccresu/config.yaml
ok      timezone        Asia/Singapore
ok      data directory  /home/you/.local/share/ccresu
ok      database        /home/you/.local/share/ccresu/ccresu.db (schema version 4)
ok      claude          claude 2.0.30 (claude --version)
ok      transcripts     session transcripts are readable
ok      telegram        disabled
warn    sessions        no sessions discovered yet
```

A `fail` row exits `1`; `warn` does not. `doctor --json` returns
`{"checks": [...]}` with the same names and statuses.

## Step 4 — Run the watchdog

```console
$ ccresu daemon
```

It discovers sessions on an interval (`claude.discovery_interval`), records them,
and sleeps in short ticks so Ctrl-C is immediate. `--once` performs one reconcile
pass and exits, which is what a cron job or an integration test wants:

```console
$ ccresu daemon --once
$ echo $?
0
```

For a long-lived install, use a service — see [deployment.md](deployment.md).
To drive ccresu from scripts, run the API server instead of (or alongside) the
daemon:

```console
$ ccresu serve
ccresu API listening on 127.0.0.1:8787
```

`ccresu serve [--addr HOST:PORT]` defaults to `server.host`/`server.port`.

## Step 5 — Find your sessions

```console
$ ccresu sessions --all
SESSION   STATE       NEXT ATTEMPT                    ENABLED  RECOVERABLE  PROJECT                                   LAST SEEN                NOTE
1dd41fc3  DISCOVERED  none (session is not enabled)   no       yes                                                       2026-11-05 03:24:03 +08
4a52c0c7  DISCOVERED  none (session is not enabled)   no       yes                                                       2026-11-05 03:24:03 +08
88dfd995  DISCOVERED  none (session is not enabled)   no       yes                                                       2026-11-05 03:24:03 +08
e1732e3d  DISCOVERED  none (session is not enabled)   no       yes                                                       2026-11-05 03:24:03 +08
e60a3100  DISCOVERED  none (no usage limit detect...  yes      yes          /home/you/projects/demo                   2026-11-05 03:24:03 +08

5 session(s) shown (--all: disabled and settled sessions included)
```

- `SESSION` is the first eight characters of the session ID; commands need the
  **full** ID.
- `STATE` is the state-machine position — `DISCOVERED`, `WAITING_FOR_RESET`,
  `RESUMING`, `RESUMED`, `RETRY_WAIT`, `DISCOVERED` and so on. The full
  diagram is in [recovery.md](recovery.md).
- `NEXT ATTEMPT` is when ccresu will touch this row and why: `in 2h14m` while a
  wait is pending, `none (<reason>)` when nothing is scheduled.
- `RECOVERABLE` and `NOTE` explain why a session is or is not actionable.

Without `--all` the table shows only the **enabled** sessions, which is a
different number from the total `ccresu status` counts, so the footer says which
filter is in force and what it hides:

```console
$ ccresu sessions
no enabled sessions match; 5 match with --all
```

```console
$ ccresu sessions
SESSION   STATE              NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT                 LAST SEEN                NOTE
3f9d1c07  WAITING_FOR_RESET  in 1m20s      yes      yes          /home/you/projects/demo 2026-11-05 03:43:40 +08
1 session(s) shown: enabled only (of 5 matching; --all includes disabled and settled)
```

Flags: `--all`, `--state STATE` (repeatable), `--limit N` (0 means all). A
`--limit` that truncates says so, so a full page never looks like the whole
table.

## Step 6 — Allow recovery of a session

Recovery is opt-in per session: discovery records everything, but ccresu only
resumes what you enable.

```console
$ ccresu enable 3f9d1c07-1111-4222-8333-444455556666
session 3f9d1c07-1111-4222-8333-444455556666: enabled=yes
```

```console
$ ccresu disable 3f9d1c07-1111-4222-8333-444455556666
session 3f9d1c07-1111-4222-8333-444455556666: enabled=no
```

Disabling is honoured immediately and is not the same as forgetting the session —
the row stays, its history stays, and `--state DISABLED` still lists it.

## Step 7 — Inspect one session

`inspect` is the verbose view: identity, paths, when the next attempt is due and
why, the latest detected limit, the stored schedule and the recent transitions.
The `Next attempt` block comes first because it is the question you opened
`inspect` to answer.

```console
$ ccresu inspect 3f9d1c07-1111-4222-8333-444455556666
session:       3f9d1c07-1111-4222-8333-444455556666
state:         WAITING_FOR_RESET
enabled:       yes
recoverable:   yes
project:       /home/you/projects/demo
transcript:    /home/you/.claude/projects/-home-you-projects-demo/3f9d1c07-1111-4222-8333-444455556666.jsonl
type:          unknown
last seen:     2026-11-05 03:28:16 +08

Next attempt
at:            2026-11-04 19:29:00 UTC (in 45s)
why:           waiting for the usage limit to reset

Latest usage limit
type:          five_hour
detected:      2026-11-05 03:28:16 +08
resets:        2026-11-05 03:29:00 +08
raw:           7:29pm (UTC)
confidence:    0.99

Schedule
due:           2026-11-05 03:29:00 +08
unknown reset: no
polls:         0
phrase:        7:29pm (UTC)

Recent transitions
WHEN                     TRANSITION                                 REASON
2026-11-05 03:28:16 +08  DISCOVERED -> USAGE_LIMIT_DETECTED         usage limit detected in the transcript
2026-11-05 03:28:16 +08  USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET  waiting until 2026-11-04T19:29:00Z
```

The ID must be complete; a truncated UUID is `not found` with exit code `3`.
Copy the full ID out of the JSON instead of retyping it:

```bash
ccresu --json sessions --all | jq -r '.[].ID'
```

`inspect --json` carries the same answer as `next_attempt.at` (RFC 3339),
`next_attempt.reason` and a ready-made `next_attempt.human` countdown.

## Step 8 — Watch a usage limit be detected and scheduled

Nothing to do here but leave the daemon running and let a session hit its limit.
When the banner appears in a transcript, the daemon logs it and moves the session
to `WAITING_FOR_RESET` with a schedule:

```json
{"time":"2026-11-04T19:28:16.224107958Z","level":"INFO","msg":"usage limit detected","session":"3f9d1c07-1111-4222-8333-444455556666","limit":"five_hour","known_reset":true,"reset_at":"2026-11-04T19:29:00Z"}
```

(`--log-format text` makes that line readable in a terminal; `json` is the
default.)

```console
$ ccresu sessions
SESSION   STATE              NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT                 LAST SEEN                NOTE
3f9d1c07  WAITING_FOR_RESET  in 1m20s      yes      yes          /home/you/projects/demo 2026-11-05 03:43:40 +08

1 session(s) shown: enabled only (--all includes disabled and settled)
```

```console
$ ccresu status
config:        /home/you/.config/ccresu/config.yaml
database:      /home/you/.local/share/ccresu/ccresu.db
sessions:      1 total, 1 enabled
listing:       ccresu sessions shows the 1 enabled; ccresu sessions --all shows all 1
pending:       1
schedules:     1
next wake:     2026-11-04T19:29:00Z
telegram:      no
recovery:      yes (max attempts 10)
now:           2026-11-05 03:28:16 +08

STATE              SESSIONS
WAITING_FOR_RESET  1
```

`next wake` is the moment the daemon will act, in UTC. If the reset time could
not be extracted from the banner, the session is still scheduled: ccresu polls
with a backoff (`scheduler.unknown_reset_initial_interval` up to
`scheduler.unknown_reset_max_interval`) until the limit clears, and `inspect`
shows `unknown reset: yes`.

## Step 9 — Watch the resume happen

When the reset time arrives, ccresu re-checks the session, checks that the limit
has actually cleared, resumes **the same** session ID, and verifies the
conversation grew afterwards. `ccresu history` is the audit trail:

```console
$ ccresu history 3f9d1c07-1111-4222-8333-444455556666
session:       3f9d1c07-1111-4222-8333-444455556666
state:         RESUMED

#  STATE    STARTED                  FINISHED                 RESULT     ERROR
1  RESUMED  2026-11-05 03:29:03 +08  2026-11-05 03:29:03 +08  succeeded

WHEN                     TRANSITION                                 REASON
2026-11-05 03:28:16 +08  DISCOVERED -> USAGE_LIMIT_DETECTED         usage limit detected in the transcript
2026-11-05 03:28:16 +08  USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET  waiting until 2026-11-04T19:29:00Z
2026-11-05 03:29:03 +08  WAITING_FOR_RESET -> RESET_TIME_REACHED    the scheduled reset time has arrived
2026-11-05 03:29:03 +08  RESET_TIME_REACHED -> CHECKING_SESSION     checking the session before resuming
2026-11-05 03:29:03 +08  CHECKING_SESSION -> CHECKING_LIMIT         checking the usage limit has cleared
2026-11-05 03:29:03 +08  CHECKING_LIMIT -> RESUMING                 resuming the original session
2026-11-05 03:29:03 +08  RESUMING -> RESUMED                        the original session resumed and the conversation grew
```

`--limit N` caps the number of transitions shown (default 50; the newest are
kept). `status` then reads:

```console
sessions:      1 total, 1 enabled
listing:       ccresu sessions shows the 1 enabled; ccresu sessions --all shows all 1
pending:       0
schedules:     0
```

## Step 10 — Act manually

Three commands bypass the wait, with different rules:

```bash
# resume now, ignoring the schedule (still respects the attempt budget)
ccresu recover 3f9d1c07-1111-4222-8333-444455556666

# same, with your own continuation message and even with the budget spent
ccresu recover <id> --message "Pick up where the limit interrupted you." --force

# put a spent/terminal session back to work with a fresh budget
ccresu retry <id>

# ask for one more attempt right now
ccresu retry <id>
```

`recover` refuses when the session cannot be resumed, and says why:

```console
$ ccresu recover 3f9d1c07-1111-4222-8333-444455556666
ccresu: recover 3f9d1c07-1111-4222-8333-444455556666: recovery: session is not eligible for recovery: session 3f9d1c07-1111-4222-8333-444455556666 is RESUMED and cannot become CHECKING_SESSION
$ echo $?
4
```

Exit code `4` means "not eligible"; `3` means "not found". A `recover` on a
session that never hit a limit is refused the same way, because there is nothing
to recover:

```console
$ ccresu recover e60a3100-123a-4deb-b146-54705ff80268
ccresu: recover e60a3100-123a-4deb-b146-54705ff80268: recovery: invalid state transition: RESUMED -> CHECKING_SESSION (legal targets: COMPLETED, DISABLED, RUNNING, USER_TERMINATED)
```

## Step 11 — When the resume fails

Failures are retried with exponential backoff, and every attempt is numbered
against `recovery.max_attempts`. The transitions show the budget being spent:

```console
2026-11-05 03:27:03 +08  RESUMING -> RETRY_WAIT  attempt 1/10 failed (retryable); next attempt in 28.99625...
2026-11-05 03:27:03 +08  RESUMING -> RETRY_WAIT  attempt 2/10 failed (retryable); next attempt in 1m4.2447...
2026-11-05 03:27:08 +08  RESUMING -> RETRY_WAIT  attempt 3/10 failed (retryable); next attempt in 2m14.063...
```

The delays come from `recovery.initial_delay` (30s), `recovery.multiplier`
(2.0), `recovery.max_delay` (15m) and `recovery.jitter` (0.15), which is why the
values are not exactly 30s, 60s, 2m. Between attempts the session sits in
`RETRY_WAIT` and the wait is a stored instant:

```console
$ ccresu sessions
SESSION   STATE       NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT                 LAST SEEN                NOTE
3f9d1c07  RETRY_WAIT  in 58s        yes      yes          /home/you/projects/demo 2026-11-05 03:45:04 +08  resume session 3f9d1c07-1111-4222-833...
```

```console
$ ccresu inspect 3f9d1c07-1111-4222-8333-444455556666 | head -13
...
Next attempt
at:            2026-11-04 19:46:02 UTC (in 58s)
why:           retrying after a failed attempt
```

When the budget for a cycle is exhausted the session is returned to
`DISCOVERED`, with the last error kept on the row:

```console
$ ccresu sessions --all
SESSION   STATE       NEXT ATTEMPT                    ENABLED  RECOVERABLE  PROJECT    LAST SEEN                NOTE
7c1b2a44  DISCOVERED  none (returned to discovery;…  yes      yes                     2026-11-05 03:28:24 +08  resume session 7c1b2a44-0000-4000-800...
```

`DISCOVERED` is not a dead end, and it is not a verdict about the session: it is
the queue the daemon reads from. The next discovery pass re-reads the transcript,
finds the next reset time and schedules another attempt with a fresh budget, so
recovery resumes on its own. `ccresu retry <id>` and
`ccresu recover <id> --force` are there if you would rather it happened now.

One failure is not the session's fault at all: `claude -p --resume` refuses while
the session is still attached to Claude Code's background agent
(`That session is running in the background (837eaaea) …`). ccresu treats that as
"busy", not "gone". In the same attempt it runs `claude stop <job-id>` — the
eight-hex job name that refusal prints, not the session UUID; `claude stop`
answers a UUID with `No job matching` and exit 1 — and then repeats the
*identical* resume once, so the session ends up `RESUMED` instead of failing
without you doing anything. The conversation is untouched — same session id, same
transcript — and the stop appears in the log, not in the attempt row. If the stop
fails, or the second resume refuses again, the original error is reported
unchanged and the session returns to `DISCOVERED` like any other failed attempt.


## Step 12 — Turn on Telegram notifications (optional)

Off by default; recovery never depends on it. Put the token in the environment so
it stays out of backups and shell history:

```bash
export CCRESU_TELEGRAM__BOT_TOKEN='123456789:AA...'
```

and the chat in the configuration file:

```yaml
telegram:
  enabled: true
  chat_id: "-1001234567890"
  parse_mode: HTML
```

Then confirm both halves are seen — the token is masked, never printed:

```console
$ ccresu config validate
configuration /home/you/.config/ccresu/config.yaml is valid
telegram:      yes
$ ccresu doctor
ok      telegram        chat -1001234567890, token ********abcd
```

Each event is a separate switch (`notify_limit_detected`,
`notify_recovery_started`, `notify_recovery_success`, `notify_recovery_failure`,
`notify_permanent_failure`, `notify_session_completed`, and `notify_waiting`
which defaults to `false`). Delivery is queued in SQLite by a sender goroutine,
so a slow Telegram cannot delay a resume. Details: [telegram.md](telegram.md).

## Step 13 — Script it: the REST and SSE API

`ccresu serve` exposes the same state over HTTP on `127.0.0.1:8787` under
`/api/v1`. Read-only routes need no credentials; state-changing routes want a
bearer token, generated on first start beside the configuration file in use —
`~/.config/ccresu/api-token` by default, `/elsewhere/api-token` after
`--config /elsewhere/config.yaml` — with mode `0600` (or wherever
`server.token_path` points).

```console
$ ccresu serve
ccresu API listening on 127.0.0.1:8787
```

```console
$ curl -s localhost:8787/api/v1/health
{"api":"/api/v1","now":"2026-11-04T19:24:45.110691574Z","status":"ok","uptime_seconds":3.033467921}
```

```bash
TOKEN=$(cat ~/.config/ccresu/api-token)

# every session, and one session with its history
curl -s localhost:8787/api/v1/sessions
curl -s localhost:8787/api/v1/sessions/$ID
curl -s localhost:8787/api/v1/sessions/$ID/history

# the same actions as `ccresu enable` / `ccresu disable` / `ccresu recover`
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8787/api/v1/sessions/$ID/enable
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8787/api/v1/sessions/$ID/disable
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"message":"continue the goal"}' localhost:8787/api/v1/sessions/$ID/recover
```

A state-changing call without the header is rejected with `401`, not silently
ignored:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8787/api/v1/sessions/$ID/enable
401
```

For a live feed, subscribe to the SSE stream. It opens with a `retry:` hint and a
`ready` event, then pushes state changes, so a script can react without polling:

```console
$ curl -N localhost:8787/api/v1/events
retry: 3000

event: ready
data: {"type":"ready","at":"2026-11-04T19:24:46.122116919Z","data":{"api":"/api/v1","heartbeat_seconds":15}}
```

Every route, field and status code is in [api.md](api.md).

## Where to go next

- [cli.md](cli.md) — every command, flag and exit code.
- [configuration.md](configuration.md) — every setting and its environment
  override.
- [recovery.md](recovery.md) — detection, scheduling and the state machine.
- [deployment.md](deployment.md) — running the daemon as a service.
- [troubleshooting.md](troubleshooting.md) — symptom-to-cause tables.
