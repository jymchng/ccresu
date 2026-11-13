# ccresu

[![Website](https://img.shields.io/badge/website-jymchng.github.io%2Fccresu-2ea44f)](https://jymchng.github.io/ccresu/)
[![Docs](https://img.shields.io/badge/docs-read%20the%20manual-blue)](https://jymchng.github.io/ccresu/docs/)
[![CI](https://github.com/jymchng/ccresu/actions/workflows/ci.yml/badge.svg)](https://github.com/jymchng/ccresu/actions/workflows/ci.yml)

**Website and documentation: <https://jymchng.github.io/ccresu/>**

Automatically resume your original Claude Code session after a usage limit resets.

`ccresu` watches Claude Code sessions, detects usage-limit interruptions, extracts
the reset time and its timezone, waits for the reset, and resumes **the same**
session — preserving conversation history, working directory and active goal —
then verifies the recovery actually happened. Telegram notifications are optional.

```text
You've hit your 5-hour limit · resets 12pm (Europe/Berlin)
Goal paused · usage limit reached · send a message after it resets
```

## Install

One line, on Linux or macOS (`amd64`/`arm64`). Preview it first — `--dry-run`
prints every action and downloads nothing:

```bash
curl -fsSL https://raw.githubusercontent.com/jymchng/ccresu/main/scripts/install.sh | bash -s -- --dry-run
```

Then, when the plan looks right, the install itself:

```bash
curl -fsSL https://raw.githubusercontent.com/jymchng/ccresu/main/scripts/install.sh | bash
```

On Windows (`amd64`/`arm64`) the equivalent is a PowerShell one-liner, run in a
normal (non-elevated) PowerShell window:

```powershell
irm https://raw.githubusercontent.com/jymchng/ccresu/main/scripts/install.ps1 | iex
```

The Windows installer does the same work: it resolves the release, downloads
`ccresu_<version>_windows_<arch>.zip` plus `checksums.txt`, verifies the sha256
**before** extracting, installs `ccresu.exe` into `%LOCALAPPDATA%\Programs\ccresu\bin`,
adds that directory to your user `PATH`, and writes `%APPDATA%\ccresu\config.yaml`
from the example shipped in the archive. A piped script has no arguments, so
`CCRESU_VERSION` (pin a release), `CCRESU_PREFIX` and `CCRESU_BASE_URL` (a mirror)
are read from the environment. From a checkout the script takes flags — preview
with `-DryRun`, which changes nothing either:

```powershell
pwsh -File scripts/install.ps1 -Version 0.1.0 -DryRun
```

Reinstall a version you already have with `-Force`:

```powershell
pwsh -File scripts/install.ps1 -Force
```

The script downloads the release matching your OS/architecture, verifies it
against the published checksums **before** anything is extracted or executed,
installs the binary into `~/.local/bin`, creates `~/.config/ccresu/config.yaml`
with mode `0600`, and validates that configuration with the binary it just
installed. Re-running it is the supported upgrade path. It needs nothing from a
checkout: everything it reads comes from the release it resolves.

Flags go after `-s --`, for example
`| bash -s -- --version 0.1.0 --systemd`. `bash -s -- --help` lists them:

| Option | Meaning |
| --- | --- |
| `--version VER` | Install a specific release (e.g. `0.1.0` or `v0.1.0`). Default: the newest published release. |
| `--prefix DIR` | Install the binary here. Default: `$HOME/.local/bin`. |
| `--systemd` | Also install and enable the user-level systemd unit. |
| `--dry-run` | Print the actions without downloading or changing anything. |
| `--force` | Reinstall even when the same version is already present. |
| `-h`, `--help` | Show the help. |

It also reads `CCRESU_VERSION`, `CCRESU_PREFIX`, `CCRESU_BASE_URL` (a mirror or
an air-gapped test) and `CCRESU_ALLOW_ROOT`. The Windows script reads
`CCRESU_VERSION`, `CCRESU_PREFIX` and `CCRESU_BASE_URL` for the same reasons, and
`-Version`, `-Prefix`, `-BaseUrl`, `-Force`, `-DryRun`, `-NoPathUpdate` from a
checkout.

Read the scripts before running them, if you prefer: they are
[`scripts/install.sh`](scripts/install.sh) and
[`scripts/install.ps1`](scripts/install.ps1) in this repository.

The last thing it prints is what to do next:

```console
Done. Next steps:
  1. edit /home/you/.config/ccresu/config.yaml
  2. ccresu doctor
  3. ccresu daemon   # or rerun with --systemd for a user service
```

### Or: `go install` (builds from the module)

```bash
CGO_ENABLED=0 go install github.com/jymchng/ccresu/cmd/ccresu@latest && "$(go env GOPATH)/bin/ccresu" version
```

`go install` puts the binary in `$(go env GOPATH)/bin`, which is not on `PATH`
by default — that is the only reason for the second line. Append it to
`~/.profile` if you want it to survive a new shell.

### Or: build from a source checkout

```bash
git clone https://github.com/jymchng/ccresu.git
cd ccresu
go build -o ~/.local/bin/ccresu ./cmd/ccresu
```

Those three lines are one procedure — the `go build` only means anything inside
the clone. If you would rather have the released binary than your own, the
checkout carries the same installer, so run it from the clone instead of
building:

```bash
./scripts/install.sh
```

Then create the configuration once:

```bash
ccresu config init
```

`config init` writes the example configuration the binary carries — the same
bytes the installer would have copied — to `~/.config/ccresu/config.yaml` with
mode `0600`, creating `~/.config/ccresu` if it does not exist. It never replaces
an existing file without `--force`, so it is safe to run at any time. Point it
somewhere else with the global flag or a positional path:

```bash
ccresu --config ./test-config.yaml config init
ccresu config init /etc/ccresu/config.yaml
ccresu config init --force        # replace the file with the shipped example
```

Whatever the route, confirm the binary is the one you expect:

```console
$ ccresu --help
ccresu dev — resume Claude Code sessions after a usage limit resets

Usage:
  ccresu [--config PATH] [--json] [--log-level LEVEL] <command> [flags]
  ccresu --help | --version

Commands:
  config    create, validate (or print) the configuration
  daemon    run the watchdog loop until interrupted
  disable   stop ccresu from recovering a session
  doctor    check the environment ccresu runs in
  enable    allow ccresu to recover a session
  history   show the attempts and state transitions of a session
  inspect   show everything known about one session (same as 'sessions status')
  recover   resume one session now
  retry     retry a session whose attempt budget is spent
  serve     run the local REST/SSE API
  sessions  list, inspect or delete sessions
  status    summarise session, schedule and notification state
  version   print version and build metadata

Global flags:
  -c, --config PATH      configuration file (default: ~/.config/ccresu/config.yaml)
      --json             emit machine-readable JSON where the command supports it
      --log-level LEVEL  debug, info, warn or error
      --log-format FMT   json or text
  -v, --verbose          shorthand for --log-level debug
  -q, --quiet            shorthand for --log-level error

Exit codes: 0 ok, 1 failure, 2 usage, 3 not found, 4 not eligible, 130 interrupted.
Run 'ccresu <command> --help' for the flags of one command.
```

```console
$ ccresu version
ccresu dev
commit:        none
built:         unknown
go:            go1.26.8
platform:      linux/amd64
```

`dev`, `none` and `unknown` are what a `go build` from a checkout reports; a
release build fills them in.

## Tutorial: your first recovered session

Thirteen steps, in order. Each one says what to run and what you should see. The
transcripts below are real output from a scratch `HOME` on Linux, so the paths
and session IDs are examples — yours will differ.

> Throughout, `--config PATH` selects a configuration file other than the
> default `~/.config/ccresu/config.yaml` (so does `CCRESU_CONFIG` in the
> environment), and `--json` switches a command to machine-readable output. Both
> are global flags, so they go before the subcommand: `ccresu --config
> ./test.yaml sessions`, not `ccresu sessions --config ./test.yaml`.

### Step 1 — Create the configuration

Every install route ends the same way: one YAML file describes what ccresu
watches and where it stores state. `config init` writes it for you — the shipped
example, at the default path, readable only by you.

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
# a throwaway configuration for a scratch environment
ccresu --config /tmp/ccresu/config.yaml config init

# the same thing, positionally
ccresu config init /tmp/ccresu/config.yaml
```

Parent directories are created, and the file is `0600` — it is where the Telegram
bot token and the API token end up. An existing file is **never** replaced
silently:

```console
$ ccresu config init
ccresu: configuration /home/you/.config/ccresu/config.yaml already exists; it was left untouched
to keep it:          edit it, then run 'ccresu config validate'
to replace it:       ccresu config init /home/you/.config/ccresu/config.yaml --force
to use another file: ccresu --config PATH config init
```

That is exit code 1, and the file is byte-for-byte untouched. When you really do
want the shipped example back, say so:

```bash
ccresu config init --force
```

`ccresu config init --json` reports the same thing for scripts: `path`, `created`,
`replaced`, `mode`, `valid` and `next_steps`.

### Step 2 — Validate the configuration

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
`CCRESU_*` environment overrides are applied. Anything that looks wrong in
`config validate` can be traced with `ccresu config show --json`.

If the file is missing you get a non-zero exit and a precise reason:

```console
$ ccresu config validate
ccresu: configuration /home/you/.config/ccresu/config.yaml is invalid:
invalid configuration: read /home/you/.config/ccresu/config.yaml: open /home/you/.config/ccresu/config.yaml: no such file or directory
```

### Step 3 — Check the environment

`ccresu doctor` is the pre-flight check. It looks at the config, the timezone,
the data directory, the database schema, the `claude` executable, transcript
readability and the notifier, and it never changes anything.

```console
$ ccresu doctor
STATUS  CHECK           DETAIL
ok      config          /home/you/.config/ccresu/config.yaml
ok      timezone        Asia/Singapore
ok      data directory  /home/you/.local/share/ccresu
ok      database        /home/you/.local/share/ccresu/ccresu.db (schema version 4)
ok      claude          2.1.223 (claude --version)
ok      transcripts     session transcripts are readable
ok      telegram        disabled
warn    sessions        no sessions discovered yet
```

`warn` on `sessions` is expected before the first scan (step 3). A `fail` line
is the thing to fix; `--json` gives the same rows as objects for monitoring.

### Step 4 — Run the watchdog

`ccresu daemon` is the program that does the work: discover, detect, schedule,
resume, verify, repeat. Run it in the foreground the first time so you can watch
the log.

```bash
ccresu daemon                 # until you press Ctrl-C (exit code 130)
ccresu daemon --once          # one reconcile pass, then exit
ccresu daemon --interval 10s  # override claude.discovery_interval
```

```console
$ ccresu daemon --once
reconcile pass complete (5 sessions)
```

For a long-lived install, either `./scripts/install.sh --systemd` or your own
unit is better than a terminal — see [docs/deployment.md](docs/deployment.md).

If you would rather drive ccresu from scripts, run the API server instead of (or
alongside) the daemon:

```console
$ ccresu serve
ccresu API listening on 127.0.0.1:8787
```

`ccresu serve [--addr HOST:PORT]` defaults to `server.host`/`server.port` from
the configuration. Step 13 uses it.

### Step 5 — Find your sessions

```console
$ ccresu sessions --all
SESSION   STATE       NEXT ATTEMPT                    ENABLED  RECOVERABLE  PROJECT                                   LAST SEEN                NOTE
1dd41fc3  DISCOVERED  none (session is not enabled)   no       yes                                                       2026-11-05 03:24:03 +08
4a52c0c7  DISCOVERED  none (session is not enabled)   no       yes                                                       2026-11-05 03:24:03 +08
88dfd995  DISCOVERED  none (session is not enabled)   no       yes                                                       2026-11-05 03:24:03 +08
e1732e3d  DISCOVERED  none (session is not enabled)   no       yes                                                       2026-11-05 03:24:03 +08
e60a3100  DISCOVERED  none (no usage limit detect...  yes      yes          /root/python_projects/python-password...  2026-11-05 03:24:03 +08

5 session(s) shown (--all: disabled and settled sessions included)
```

- `SESSION` is the first eight characters of the session ID; commands need the
  **full** ID (see step 6 for the one-liner that copies it).
- `STATE` is the state-machine position — `DISCOVERED`, `DISABLED`,
  `WAITING_FOR_RESET`, `RESUMING`, `RESUMED`, `RETRY_WAIT`, `DISCOVERED`
  and so on. [docs/recovery.md](docs/recovery.md) has the full diagram.
- `NEXT ATTEMPT` is the answer to "when will ccresu touch this one, and why":
  `in 2h14m` while a wait is pending, `none (<reason>)` when nothing is
  scheduled and the reason is the whole story. It comes from the stored schedule
  or the stored retry instant, never from a re-derivation, so it matches what
  the daemon will actually do.
- `RECOVERABLE` and `NOTE` explain *why* a session is or is not actionable.

Without `--all`, the table shows only the sessions ccresu is currently working
on — the enabled ones. That is a different number from the total `ccresu status`
counts, so the footer says which filter is in force and what it is hiding:

```console
$ ccresu sessions
no enabled sessions match; 5 match with --all
```

The same rule applies when there is something to show:

```console
$ ccresu sessions
SESSION   STATE              NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT  LAST SEEN                NOTE
3f9d1c07  WAITING_FOR_RESET  in 1m20s      yes      yes                   2026-11-05 03:43:40 +08
1 session(s) shown: enabled only (of 5 matching; --all includes disabled and settled)
```

Flags: `--all`, `--state STATE` (repeatable), `--limit N` (0 means all). A
`--limit` that actually truncates the list says so, so a full page never looks
like the whole table.

### Step 6 — Allow recovery of a session

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

Disabling is honoured immediately and is not the same as forgetting the
session — the row stays, its history stays, and `--state DISABLED` still lists
it.

### Step 7 — Inspect one session

`sessions status` (or its alias `inspect`) is the verbose view: identity, paths,
when the next attempt is due and why, the latest detected limit, the stored
schedule and the recent transitions. The `Next attempt` block is first because it
is the question you opened the command to answer; `--json` carries the same
answer as `next_attempt.at` (RFC 3339), `next_attempt.reason` and a ready-made
`next_attempt.human` countdown.

```console
$ ccresu inspect 3f9d1c07-1111-4222-8333-444455556666
session:       3f9d1c07-1111-4222-8333-444455556666
state:         WAITING_FOR_RESET
enabled:       yes
recoverable:   yes
project:       -
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

The ID must be complete. A typo or a truncated UUID is a `not found` with exit
code `3` — copy the full ID out of the JSON instead of retyping it:

```bash
ccresu --json sessions --all | jq -r '.[].ID'
```

### Step 8 — Watch a usage limit be detected and scheduled

Nothing to do here but leave the daemon running and let a session hit its limit.
When the banner appears in a transcript, the daemon logs it and moves the
session to `WAITING_FOR_RESET` with a schedule:

```json
{"time":"2026-11-04T19:28:16.224107958Z","level":"INFO","msg":"usage limit detected","session":"3f9d1c07-1111-4222-8333-444455556666","limit":"five_hour","known_reset":true,"reset_at":"2026-11-04T19:29:00Z"}
```

(`--log-format text` makes that line readable in a terminal; `json` is the
default because it is what log shippers want.)

The table and the summary pick it up straight away:

```console
$ ccresu sessions
SESSION   STATE              NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT  LAST SEEN                NOTE
3f9d1c07  WAITING_FOR_RESET  in 1m20s      yes      yes                   2026-11-05 03:43:40 +08

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
not be extracted from the banner, the session still gets scheduled: ccresu polls
with a backoff (`scheduler.unknown_reset_initial_interval` up to
`scheduler.unknown_reset_max_interval`) until the limit clears, and `inspect`
shows `unknown reset: yes`.

### Step 9 — Watch the resume happen

When the reset time arrives, ccresu re-checks the session, checks the limit has
actually cleared, resumes **the same** session ID, and verifies the conversation
grew afterwards. `ccresu history` is the audit trail:

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

### Step 10 — Act manually

Four commands let you bypass the wait, with different rules:

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

`recover` refuses when the session is not in a state that can be resumed, and
says which states were legal:

```console
$ ccresu recover 3f9d1c07-1111-4222-8333-444455556666
ccresu: recover 3f9d1c07-1111-4222-8333-444455556666: recovery: session is not eligible for recovery: session 3f9d1c07-1111-4222-8333-444455556666 is RESUMED and cannot become CHECKING_SESSION
$ echo $?
4
```

Exit code `4` means "not eligible"; `3` means "not found". A whole `recover` on
a session that never hit a limit is refused the same way, because there is
nothing to recover:

```console
$ ccresu recover e60a3100-123a-4deb-b146-54705ff80268
ccresu: recover e60a3100-123a-4deb-b146-54705ff80268: recovery: invalid state transition: RESUMED -> CHECKING_SESSION (legal targets: COMPLETED, DISABLED, RUNNING, USER_TERMINATED)
```

### Step 11 — When the resume fails

Failures are retried with exponential backoff, and every attempt is numbered
against `recovery.max_attempts`. The transitions show the budget being spent:

```console
2026-11-05 03:27:03 +08  RESUMING -> RETRY_WAIT  attempt 1/10 failed (retryable); next attempt in 28.99625...
2026-11-05 03:27:03 +08  RESUMING -> RETRY_WAIT  attempt 2/10 failed (retryable); next attempt in 1m4.2447...
2026-11-05 03:27:08 +08  RESUMING -> RETRY_WAIT  attempt 3/10 failed (retryable); next attempt in 2m14.063...
2026-11-05 03:27:08 +08  RESUMING -> RETRY_WAIT  attempt 4/10 failed (retryable); next attempt in 4m3.5004...
2026-11-05 03:27:13 +08  RESUMING -> RETRY_WAIT  attempt 5/10 failed (retryable); next attempt in 7m5.4121...
```

The delays come from `recovery.initial_delay` (30s), `recovery.multiplier` (2.0),
`recovery.max_delay` (15m) and `recovery.jitter` (0.15), which is why the values
are not exactly 30s, 60s, 2m. Between attempts the session sits in `RETRY_WAIT`
and the wait is a stored instant, not just prose:

```console
$ ccresu sessions
SESSION   STATE       NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT  LAST SEEN                NOTE
3f9d1c07  RETRY_WAIT  in 58s        yes      yes                   2026-11-05 03:45:04 +08  resume session 3f9d1c07-1111-4222-833...
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

### Step 12 — Turn on Telegram notifications (optional)

Off by default; recovery never depends on it. Put the token in the environment
so it stays out of backups and shell history:

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
`notify_recovery_started`, `notify_recovery_success`,
`notify_recovery_failure`, `notify_permanent_failure`,
`notify_session_completed`, and `notify_waiting` which defaults to `false`).
Delivery is queued in SQLite by a sender goroutine, so a slow Telegram cannot
delay a resume. Full details: [docs/telegram.md](docs/telegram.md).

### Step 13 — Script it: the REST and SSE API

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

$ curl -s localhost:8787/api/v1/status
{"status":"ok","version":"dev","now":"2026-11-04T19:24:45.937214023Z","uptime_seconds":3.859989177,"config_path":"/home/you/.config/ccresu/config.yaml","database_path":"/home/you/.local/share/ccresu/ccresu.db","sessions":5,"enabled_sessions":0,"states":{"DISABLED":1,"DISCOVERED":4},"pending_recoveries":0,"schedules":0,"pending_notifications":0,"telegram_enabled":false,"recovery_enabled":true,"max_attempts":10,"monitoring":true}
```

```bash
TOKEN=$(cat ~/.config/ccresu/api-token)

# every session, and one session with its attempts, notifications and history
curl -s localhost:8787/api/v1/sessions
curl -s localhost:8787/api/v1/sessions/$ID
curl -s localhost:8787/api/v1/sessions/$ID/history

# the same actions as `ccresu enable` / `ccresu disable` / `ccresu recover`
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8787/api/v1/sessions/$ID/enable
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8787/api/v1/sessions/$ID/disable
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"message":"continue the goal"}' localhost:8787/api/v1/sessions/$ID/recover

# what is queued, and what is about to happen
curl -s localhost:8787/api/v1/recoveries
curl -s localhost:8787/api/v1/telegram/status
curl -s localhost:8787/api/v1/config
```

A state-changing call without the header is rejected with `401`, not silently
ignored:

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8787/api/v1/sessions/$ID/enable
401
```

For a live feed, subscribe to the SSE stream. It opens with a `retry:` hint and
a `ready` event, then pushes state changes — so a script can react without
polling:

```console
$ curl -N localhost:8787/api/v1/events
retry: 3000

event: ready
data: {"type":"ready","at":"2026-11-04T19:24:46.122116919Z","data":{"api":"/api/v1","heartbeat_seconds":15}}
```

The full route table, error codes, rate limiting and CORS rules are in
[docs/api.md](docs/api.md).

### Where to go next

| You want to | Read |
| --- | --- |
| Know every flag and exit code | [docs/cli.md](docs/cli.md) |
| Understand detection, scheduling and the state machine | [docs/recovery.md](docs/recovery.md) |
| Change a setting | [docs/configuration.md](docs/configuration.md) |
| Run it as a user service with logs | [docs/deployment.md](docs/deployment.md) |
| Fix a symptom | [docs/troubleshooting.md](docs/troubleshooting.md) |

Every command above supports `--json`, so the same walkthrough can be scripted
end to end without parsing tables.

## Interactive browser

`ccresu sessions` on a terminal is not a printout but a browser: the same table,
a cursor, and every action one keystroke away. That matters because the loop it
replaces is tedious — read a short id, scroll back, retype it into `ccresu
inspect`, `ccresu recover`, `ccresu disable` — and it is the loop you are in at
3am when a night's worth of sessions have all hit a limit.

```console
$ ccresu sessions --all
ccresu sessions — 3 shown (all)
  SESSION   STATE              NEXT ATTEMPT                    ENABLED  PROJECT                  LAST SEEN                NOTE
> 88dfd995  DISABLED           none (session is not enabled)   no       /home/you/projects/site  2026-11-13 09:34:43 +08  -
  3f9d1c07  WAITING_FOR_RESET  in 12m40s                       yes      /home/you/projects/demo  2026-11-13 09:34:43 +08  -
  4d1f9c2a  RUNNING            none (no usage limit detect...  yes      /home/you/projects/api   2026-11-13 09:34:43 +08  -

enter detail  n new  x delete  R resume  e enable  d disable  r retry  / filter  a all  ? help  q quit
```

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
| `x` | delete the selected session (asks y/N) |
| `?` | this key map |
| `q / ctrl-c` | quit |

`?` paints the same table on screen — the help screen and this one are both
rendered from `tuiKeyMap`, so they cannot disagree. Each action is the command of
the same name rather than a second implementation: `enter` shows the page
`sessions status` prints, `R` makes the call `ccresu recover` makes, and `x`
asks first (`delete 88dfd995-…? this forgets ccresu's row, not the
conversation. y/N`) because deleting is the one action that discards history.
The conversation itself is never touched: nothing under `~/.claude` is removed,
and `ccresu enable <id>` restores the row.

The browser is gated on a terminal, and the gate is not a second renderer:

| Situation | What you get |
| --- | --- |
| `--tui` | the browser, even when stdout is a pipe |
| `--no-tui` | the table, even on a terminal |
| `--json` | the JSON document, even on a terminal |
| stdout is a terminal | the browser |
| a pipe, a redirect, a cron job | the table, byte-for-byte as before |

`--tui --no-tui` and `--tui --json` are usage errors (exit `2`). The browser
restores the alternate screen, the cursor and raw mode on every exit path,
including `ctrl-c` and end of input, so it cannot leave your terminal broken.
Full contract and the error messages: [docs/cli.md](docs/cli.md).

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success. |
| 1 | Failure — the operation was attempted and did not succeed. |
| 2 | Usage error — a bad flag, a missing argument, an unknown command. |
| 3 | Not found — no such session. |
| 4 | Not eligible — the session cannot be recovered in its current state. |
| 130 | Interrupted (`Ctrl-C`). |


## Documentation

The documentation is published at **<https://jymchng.github.io/ccresu/docs/>**,
together with the landing page at <https://jymchng.github.io/ccresu/>. The same
material is kept in this repository under [`docs/`](docs/README.md), so it is
readable offline and reviewable in pull requests.

Hosted pages: [installation](https://jymchng.github.io/ccresu/docs/installation/)
· [tutorial](https://jymchng.github.io/ccresu/docs/tutorial/)
· [configuration](https://jymchng.github.io/ccresu/docs/configuration/)
· [CLI](https://jymchng.github.io/ccresu/docs/cli/)
· [recovery](https://jymchng.github.io/ccresu/docs/recovery/)
· [Telegram](https://jymchng.github.io/ccresu/docs/telegram/)
· [deployment](https://jymchng.github.io/ccresu/docs/deployment/)
· [security](https://jymchng.github.io/ccresu/docs/security/)
· [troubleshooting](https://jymchng.github.io/ccresu/docs/troubleshooting/)
· [known limitations](https://jymchng.github.io/ccresu/docs/limitations/)

The in-repository sources:

| Document | Read it for |
| --- | --- |
| [docs/README.md](docs/README.md) | Overview and quick start |
| [docs/tutorial.md](docs/tutorial.md) | A hands-on walkthrough from install to a resumed session |
| [docs/cli.md](docs/cli.md) | Every command, flag and exit code |
| [docs/configuration.md](docs/configuration.md) | Every setting, its default and its environment override |
| [docs/recovery.md](docs/recovery.md) | Detection, scheduling, resumption and the state machine |
| [docs/architecture.md](docs/architecture.md) | How the pieces fit together |
| [docs/api.md](docs/api.md) | The local REST and SSE API |
| [docs/telegram.md](docs/telegram.md) | Notification setup, delivery guarantees and troubleshooting |
| [docs/deployment.md](docs/deployment.md) | Running it as a service, logs, monitoring |
| [docs/upgrade.md](docs/upgrade.md) | Installing, upgrading and uninstalling |
| [docs/security.md](docs/security.md) | What it can touch, and what it keeps secret |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Symptom-to-cause tables, and known limitations |
| [docs/development.md](docs/development.md) | Layout, seams, tests, conventions |
| [docs/releases.md](docs/releases.md) | Tagging, assets, checksums |
| [docs/website.md](docs/website.md) | The static landing and documentation site |

## Verified Claude Code interfaces

ccresu only uses interfaces that exist in the installed CLI (inspected with
`claude --help` / `claude agents --help`, currently Claude Code 2.1.223):

| Purpose | Interface |
|---|---|
| Discover sessions | `claude agents --json` (`--all`, `--cwd <path>`) |
| Session transcripts | `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl` |
| Per-session environment | `~/.claude/session-env/<session-id>/` |
| Resume the original session | `claude -p --resume <session-id> "<continuation message>"` |
| Release a session a background agent still holds | `claude stop <job-id>` (the eight-hex job name the refusal prints) |

`--resume` is the command ccresu lives on. `claude stop` is the one exception, and
it exists for one situation: `claude -p --resume` refuses while the session is
still attached to the interactive CLI's background agent, so ccresu stops that
agent and repeats the identical resume. The job name is taken from the refusal
itself ("or `claude stop 837eaaea` first"); the session UUID is never passed,
because `claude stop` resolves only eight-hex job names and answers anything else
with `No job matching` and exit 1. The conversation is untouched — same
session id, same transcript. There is no `--fork-session` (it would continue a
copy instead of your conversation), no new session id, no TTY and no keystroke
injection, and if the stop fails the original refusal is reported unchanged. See
[docs/recovery.md](docs/recovery.md) for the exact mechanism and its limitations.

## Interfaces

- `ccresu` — CLI and watchdog daemon.
- Local REST + SSE API on `127.0.0.1:8787` for scripting and automation.
- A static landing page and documentation site at
  <https://jymchng.github.io/ccresu/>, deployed to GitHub Pages.

`ccresu` ships **no web UI**: the binary is the CLI, the daemon and the API.

## Development

```bash
go build ./...
go test ./... && go test -race ./...
make cover-check     # enforces the 80% statement-coverage gate
make e2e             # stop-then-resume and `reset` against a stub `claude` CLI
```

`make e2e` runs `tests/scripts/background-resume-e2e.sh`: it builds the real
binary, puts a stub `claude` on `PATH` that refuses the first resume because the
session is still attached to a background agent, and then asserts that `ccresu
recover` stops the session, repeats the identical resume, verifies that the
conversation grew, and reports `RESUMED` — followed by a session that cannot be
resumed and is therefore returned to `DISCOVERED`.

The same script with `--pre-fix` runs the identical scenario against a pre-fix
checkout and expects the old outcome: `PERMANENTLY_FAILED` after one refused
resume and no `stop` call (that binary predates the state's removal; the current
one returns the session to `DISCOVERED`). That contrast is the regression test
for the bug:

```bash
git worktree add /tmp/ccresu-prefix 00692e3
(cd /tmp/ccresu-prefix && go build -o /tmp/ccresu-prefix/ccresu ./cmd/ccresu)
tests/scripts/background-resume-e2e.sh --pre-fix /tmp/ccresu-prefix/ccresu
```

## License

[MIT](LICENSE)
