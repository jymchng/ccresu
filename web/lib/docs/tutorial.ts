import type { DocPage } from "./types";

export const tutorialDoc: DocPage = {
  slug: "tutorial",
  title: "Tutorial: your first recovered session",
  summary:
    "Thirteen steps from an empty machine to a session that was resumed for you, then the same state driven by hand and over the API.",
  blocks: [
    {
      kind: "p",
      text: "Every command below is real output from the binary run on a scratch HOME, so paths and session IDs are examples — yours will differ. Install ccresu first (see Installation), and make sure `claude` is on PATH with at least one session under `~/.claude/projects`.",
    },
    {
      kind: "note",
      tone: "info",
      title: "Global flags go before the command",
      text: "`--config PATH` selects a configuration file other than `~/.config/ccresu/config.yaml` (so does `CCRESU_CONFIG` in the environment), and `--json` switches a command to machine-readable output. Both are global flags: `ccresu --config ./test.yaml sessions`, not `ccresu sessions --config ./test.yaml`.",
    },
    { kind: "h2", text: "Step 1 — Create the configuration" },
    {
      kind: "p",
      text: "`config init` writes the example configuration the binary carries — the same bytes the installer copies — to the default path, readable only by you.",
    },
    {
      kind: "code",
      label: "Create the configuration",
      code: `$ ccresu config init
configuration /home/you/.config/ccresu/config.yaml created (mode 0600)

Next steps:
  1. edit /home/you/.config/ccresu/config.yaml
  2. ccresu config validate
  3. ccresu doctor`,
    },
    {
      kind: "p",
      text: "The default path is `~/.config/ccresu/config.yaml`; `XDG_CONFIG_HOME` moves it, and the global `--config PATH` flag, the `CCRESU_CONFIG` environment variable or a positional argument names the file explicitly. Parent directories are created and the file is 0600. An existing file is never replaced silently — that is exit code 1 with the file untouched:",
    },
    {
      kind: "code",
      label: "Explicit paths, and the refusal",
      code: `ccresu --config /tmp/ccresu/config.yaml config init
ccresu config init /tmp/ccresu/config.yaml

$ ccresu config init
ccresu: configuration /home/you/.config/ccresu/config.yaml already exists; it was left untouched
to keep it:          edit it, then run 'ccresu config validate'
to replace it:       ccresu config init /home/you/.config/ccresu/config.yaml --force
to use another file: ccresu --config PATH config init

ccresu config init --force   # replace it with the shipped example
ccresu --json config init    # path, created, replaced, mode, valid, next_steps`,
    },
    { kind: "h2", text: "Step 2 — Validate the configuration" },
    {
      kind: "p",
      text: "ccresu reads one YAML file. Every field is validated before anything runs, so a typo is a startup error rather than a silent default.",
    },
    {
      kind: "code",
      label: "Validate",
      code: `$ ccresu config validate
configuration /home/you/.config/ccresu/config.yaml is valid
app:           ccresu
timezone:      Asia/Singapore
database:      /home/you/.local/share/ccresu/ccresu.db
monitoring:    yes
recovery:      yes (max attempts 10)
telegram:      no
server:        yes (127.0.0.1:8787)

$ ccresu config validate ./configs/config.example.yaml   # an explicit path
$ ccresu config show                                    # effective values`,
    },
    { kind: "h2", text: "Step 3 — Check the environment" },
    {
      kind: "p",
      text: "`ccresu doctor` is the pre-flight check: the config, the timezone, the data directory, the database schema, the Claude Code CLI, transcript access, Telegram and discovery.",
    },
    {
      kind: "code",
      label: "Doctor",
      code: `$ ccresu doctor
STATUS  CHECK           DETAIL
ok      config          /home/you/.config/ccresu/config.yaml
ok      timezone        Asia/Singapore
ok      data directory  /home/you/.local/share/ccresu
ok      database        /home/you/.local/share/ccresu/ccresu.db (schema version 4)
ok      claude          claude 2.0.30 (claude --version)
ok      transcripts     session transcripts are readable
ok      telegram        disabled
warn    sessions        no sessions discovered yet`,
    },
    {
      kind: "p",
      text: "A `fail` row exits 1; `warn` does not. `doctor --json` returns `{\"checks\": [...]}` with the same names and statuses.",
    },
    { kind: "h2", text: "Step 4 — Run the watchdog" },
    {
      kind: "code",
      label: "Daemon, or the API",
      code: `$ ccresu daemon        # discovers on claude.discovery_interval and reconciles
$ ccresu daemon --once # a single reconcile pass, then exit 0

$ ccresu serve
ccresu API listening on 127.0.0.1:8787`,
    },
    {
      kind: "p",
      text: "For a long-lived install, run the daemon as a service (see Deployment). `ccresu serve [--addr HOST:PORT]` defaults to `server.host`/`server.port` and is the scripting route used in step 13.",
    },
    { kind: "h2", text: "Step 5 — Find your sessions" },
    {
      kind: "code",
      label: "sessions --all",
      code: `$ ccresu sessions --all
SESSION   STATE       NEXT ATTEMPT                    ENABLED  RECOVERABLE  PROJECT                 LAST SEEN                NOTE
1dd41fc3  DISCOVERED  none (session is not enabled)   no       yes                                   2026-11-05 03:24:03 +08
4a52c0c7  DISCOVERED  none (session is not enabled)   no       yes                                   2026-11-05 03:24:03 +08
88dfd995  DISCOVERED  none (session is not enabled)   no       yes                                   2026-11-05 03:24:03 +08
e1732e3d  DISCOVERED  none (session is not enabled)   no       yes                                   2026-11-05 03:24:03 +08
e60a3100  DISCOVERED  none (no usage limit detect...  yes      yes          /home/you/projects/demo 2026-11-05 03:24:03 +08

5 session(s) shown (--all: disabled and settled sessions included)`,
    },
    {
      kind: "ul",
      items: [
        "`SESSION` is the first eight characters of the session ID; commands need the full ID.",
        "`STATE` is the state-machine position — DISCOVERED, WAITING_FOR_RESET, RESUMING, RESUMED, RETRY_WAIT, PERMANENTLY_FAILED and so on.",
        "`NEXT ATTEMPT` is when ccresu will touch the row and why: `in 2h14m` while a wait is pending, `none (<reason>)` when nothing is scheduled.",
        "`RECOVERABLE` and `NOTE` explain why a session is or is not actionable.",
      ],
    },
    {
      kind: "p",
      text: "Without `--all` the table shows only the enabled sessions, which is a different number from the total `ccresu status` counts, so the footer says which filter is in force and what it hides. A `--limit` that truncates adds `limit N reached; use --limit 0 to list every match`.",
    },
    {
      kind: "code",
      label: "The default listing states its filter",
      code: `$ ccresu sessions
no enabled sessions match; 5 match with --all

$ ccresu sessions
SESSION   STATE              NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT                 LAST SEEN                NOTE
3f9d1c07  WAITING_FOR_RESET  in 1m20s      yes      yes          /home/you/projects/demo 2026-11-05 03:43:40 +08
1 session(s) shown: enabled only (of 5 matching; --all includes disabled and settled)`,
    },
    { kind: "h2", text: "Step 6 — Allow recovery of a session" },
    {
      kind: "p",
      text: "Recovery is opt-in per session: discovery records everything, but ccresu only resumes what you enable. Disabling is honoured immediately and is not the same as forgetting the session — the row stays, its history stays, and `--state DISABLED` still lists it.",
    },
    {
      kind: "code",
      label: "enable / disable",
      code: `$ ccresu enable 3f9d1c07-1111-4222-8333-444455556666
session 3f9d1c07-1111-4222-8333-444455556666: enabled=yes

$ ccresu disable 3f9d1c07-1111-4222-8333-444455556666
session 3f9d1c07-1111-4222-8333-444455556666: enabled=no`,
    },
    { kind: "h2", text: "Step 7 — Inspect one session" },
    {
      kind: "p",
      text: "`inspect` is the verbose view: identity, paths, when the next attempt is due and why, the latest detected limit, the stored schedule and the recent transitions. The Next attempt block comes first because it is the question you opened `inspect` to answer. The ID must be complete; a truncated UUID is `not found` with exit code 3.",
    },
    {
      kind: "code",
      label: "inspect",
      code: `$ ccresu inspect 3f9d1c07-1111-4222-8333-444455556666
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
2026-11-05 03:28:16 +08  USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET  waiting until 2026-11-04T19:29:00Z`,
    },
    {
      kind: "code",
      label: "Getting the full ID",
      code: `ccresu --json sessions --all | jq -r '.[].ID'`,
    },
    {
      kind: "p",
      text: "`inspect --json` carries the same answer as `next_attempt.at` (RFC 3339), `next_attempt.reason` and a ready-made `next_attempt.human` countdown.",
    },
    { kind: "h2", text: "Step 8 — Watch a usage limit be detected and scheduled" },
    {
      kind: "p",
      text: "Nothing to do here but leave the daemon running and let a session hit its limit. When the banner appears in a transcript, the daemon logs it and moves the session to WAITING_FOR_RESET with a schedule. `--log-format text` makes the log line readable in a terminal; `json` is the default.",
    },
    {
      kind: "code",
      label: "The detection log line",
      code: `{"time":"2026-11-04T19:28:16.224107958Z","level":"INFO","msg":"usage limit detected","session":"3f9d1c07-1111-4222-8333-444455556666","limit":"five_hour","known_reset":true,"reset_at":"2026-11-04T19:29:00Z"}`,
    },
    {
      kind: "code",
      label: "The table and the summary pick it up",
      code: `$ ccresu sessions
SESSION   STATE              NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT                 LAST SEEN                NOTE
3f9d1c07  WAITING_FOR_RESET  in 1m20s      yes      yes          /home/you/projects/demo 2026-11-05 03:43:40 +08

1 session(s) shown: enabled only (--all includes disabled and settled)

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
WAITING_FOR_RESET  1`,
    },
    {
      kind: "p",
      text: "`next wake` is the moment the daemon will act, in UTC. If the reset time could not be extracted from the banner, the session is still scheduled: ccresu polls with a backoff (`scheduler.unknown_reset_initial_interval` up to `scheduler.unknown_reset_max_interval`) until the limit clears, and `inspect` shows `unknown reset: yes`.",
    },
    { kind: "h2", text: "Step 9 — Watch the resume happen" },
    {
      kind: "p",
      text: "When the reset time arrives, ccresu re-checks the session, checks that the limit has actually cleared, resumes the same session ID, and verifies the conversation grew afterwards. `ccresu history` is the audit trail; `--limit N` caps how many transitions are shown (default 50, newest kept).",
    },
    {
      kind: "code",
      label: "history",
      code: `$ ccresu history 3f9d1c07-1111-4222-8333-444455556666
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
2026-11-05 03:29:03 +08  RESUMING -> RESUMED                        the original session resumed and the conversation grew`,
    },
    { kind: "h2", text: "Step 10 — Act manually" },
    {
      kind: "p",
      text: "Three commands bypass the wait, with different rules: `recover` resumes now (still respecting the attempt budget), `recover --force` tries even when the budget is spent, and `retry` gives a spent or terminal session a fresh budget.",
    },
    {
      kind: "code",
      label: "recover / retry",
      code: `ccresu recover 3f9d1c07-1111-4222-8333-444455556666
ccresu recover <id> --message "Pick up where the limit interrupted you." --force
ccresu retry <id>`,
    },
    {
      kind: "p",
      text: "`recover` refuses when the session cannot be resumed, and says why. Exit code 4 means not eligible; 3 means not found.",
    },
    {
      kind: "code",
      label: "A refusal, verbatim",
      code: `$ ccresu recover 3f9d1c07-1111-4222-8333-444455556666
ccresu: recover 3f9d1c07-1111-4222-8333-444455556666: recovery: session is not eligible for recovery: session 3f9d1c07-1111-4222-8333-444455556666 is RESUMED and cannot become CHECKING_SESSION
$ echo $?
4

$ ccresu recover e60a3100-123a-4deb-b146-54705ff80268
ccresu: recover e60a3100-123a-4deb-b146-54705ff80268: recovery: invalid state transition: DISCOVERED -> CHECKING_SESSION (legal targets: DISABLED, PERMANENTLY_FAILED, RUNNING, UNRECOVERABLE)`,
    },
    { kind: "h2", text: "Step 11 — When the resume fails" },
    {
      kind: "p",
      text: "Failures are retried with exponential backoff, and every attempt is numbered against `recovery.max_attempts`. The delays come from `recovery.initial_delay` (30s), `recovery.multiplier` (2.0), `recovery.max_delay` (15m) and `recovery.jitter` (0.15), which is why the values are not exactly 30s, 60s, 2m. Between attempts the session sits in RETRY_WAIT and the wait is a stored instant, not just prose.",
    },
    {
      kind: "code",
      label: "The budget being spent",
      code: `2026-11-05 03:27:03 +08  RESUMING -> RETRY_WAIT  attempt 1/10 failed (retryable); next attempt in 28.99625...
2026-11-05 03:27:03 +08  RESUMING -> RETRY_WAIT  attempt 2/10 failed (retryable); next attempt in 1m4.2447...

$ ccresu sessions
SESSION   STATE       NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT                 LAST SEEN                NOTE
3f9d1c07  RETRY_WAIT  in 58s        yes      yes          /home/you/projects/demo 2026-11-05 03:45:04 +08  resume session 3f9d1c07-1111-4222-833...

$ ccresu inspect 3f9d1c07-1111-4222-8333-444455556666 | head -13
...
Next attempt
at:            2026-11-04 19:46:02 UTC (in 58s)
why:           retrying after a failed attempt`,
    },
    {
      kind: "code",
      label: "When the budget is exhausted",
      code: `$ ccresu sessions --all
SESSION   STATE               NEXT ATTEMPT                    ENABLED  RECOVERABLE  PROJECT                 LAST SEEN                NOTE
7c1b2a44  PERMANENTLY_FAILED  none (attempt budget spent;...  yes      yes          /home/you/projects/demo 2026-11-05 03:28:24 +08  resume session 7c1b2a44-0000-4000-800...

$ ccresu retry 7c1b2a44-0000-4000-8000-abcdefabcdef
ccresu: retry 7c1b2a44-0000-4000-8000-abcdefabcdef: recovery: session 7c1b2a44-0000-4000-8000-abcdefabcdef ended in PERMANENTLY_FAILED: resume session 7c1b2a44-0000-4000-8000-abcdefabcdef: claude: malformed claude output: empty resume output`,
    },
    { kind: "h2", text: "Step 12 — Turn on Telegram notifications (optional)" },
    {
      kind: "p",
      text: "Off by default; recovery never depends on it. Put the token in the environment so it stays out of backups and shell history, and the chat in the configuration file.",
    },
    {
      kind: "code",
      label: "Configuration",
      code: `export CCRESU_TELEGRAM__BOT_TOKEN='123456789:AA...'

# config.yaml
telegram:
  enabled: true
  chat_id: "-1001234567890"
  parse_mode: HTML`,
    },
    {
      kind: "code",
      label: "Confirm both halves are seen — the token is masked",
      code: `$ ccresu config validate
configuration /home/you/.config/ccresu/config.yaml is valid
telegram:      yes
$ ccresu doctor
ok      telegram        chat -1001234567890, token ********abcd`,
    },
    {
      kind: "p",
      text: "Each event is a separate switch (`notify_limit_detected`, `notify_recovery_started`, `notify_recovery_success`, `notify_recovery_failure`, `notify_permanent_failure`, `notify_session_completed`, and `notify_waiting` which defaults to false). Delivery is queued in SQLite by a sender goroutine, so a slow Telegram cannot delay a resume.",
    },
    { kind: "h2", text: "Step 13 — Script it: the REST and SSE API" },
    {
      kind: "p",
      text: "`ccresu serve` exposes the same state over HTTP on 127.0.0.1:8787 under `/api/v1`. Read-only routes need no credentials; state-changing routes want a bearer token, generated on first start beside the configuration file in use — `~/.config/ccresu/api-token` by default, `/elsewhere/api-token` after `--config /elsewhere/config.yaml` — with mode 0600 (or wherever `server.token_path` points).",
    },
    {
      kind: "code",
      label: "The API",
      code: `$ ccresu serve
ccresu API listening on 127.0.0.1:8787

$ curl -s localhost:8787/api/v1/health
{"api":"/api/v1","now":"2026-11-04T19:24:45.110691574Z","status":"ok","uptime_seconds":3.033467921}

TOKEN=$(cat ~/.config/ccresu/api-token)
curl -s localhost:8787/api/v1/sessions
curl -s localhost:8787/api/v1/sessions/$ID
curl -s localhost:8787/api/v1/sessions/$ID/history
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8787/api/v1/sessions/$ID/enable
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8787/api/v1/sessions/$ID/disable
curl -s -X POST -H "Authorization: Bearer $TOKEN" \\
  -d '{"message":"continue the goal"}' localhost:8787/api/v1/sessions/$ID/recover

$ curl -s -o /dev/null -w '%{http_code}\\n' -X POST localhost:8787/api/v1/sessions/$ID/enable
401`,
    },
    {
      kind: "p",
      text: "For a live feed, subscribe to the SSE stream. It opens with a `retry:` hint and a ready event, then pushes state changes, so a script can react without polling.",
    },
    {
      kind: "code",
      label: "SSE",
      code: `$ curl -N localhost:8787/api/v1/events
retry: 3000

event: ready
data: {"type":"ready","at":"2026-11-04T19:24:46.122116919Z","data":{"api":"/api/v1","heartbeat_seconds":15}}`,
    },
    { kind: "h2", text: "Where to go next" },
    {
      kind: "ul",
      items: [
        "CLI reference — every command, flag and exit code.",
        "Configuration — every setting, its default and its environment override.",
        "Recovery — detection, scheduling and the state machine.",
        "Deployment — running the daemon as a service.",
        "Troubleshooting — symptom-to-cause tables.",
      ],
    },
  ],
};
