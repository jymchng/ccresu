# Troubleshooting

Every symptom below is something the code can actually produce, with the check
that distinguishes it from the neighbours. Start with `ccresu doctor`: it
exercises the real paths and its table usually names the failing layer.

```bash
ccresu doctor
ccresu version
```

`doctor` exits `1` when any check is `fail` (a `warn` does not fail it), and
prints the failing check names on stderr. Journald users:

```bash
journalctl --user -u ccresu.service -n 100 --no-pager
```

## The CLI

| Symptom | Cause and fix |
| --- | --- |
| `ccresu: unknown command "bogus"` (exit 2) | The command table is exact; check for a typo. `ccresu --help` lists every command. |
| `flag provided but not defined: -json` (exit 2) | A flag was misspelled or does not belong to that command. Global flags work in either position (`ccresu --json status` and `ccresu status --json`); `ccresu <command> --help` lists the per-command flags, which include the global ones. |
| `ccresu: configuration ... is not readable` | No file at the resolved path. `ccresu config validate` prints the path it tried; run `ccresu config init` to write the shipped example there (or the installer, which does the same). |
| `invalid configuration: <field>: <reason>` | Validation is strict and never silent. The message names the field — an unknown key, wrong type, negative duration, unparsable timezone, relative path where an absolute one is required, or out-of-range value. |
| Exit `3` from `inspect`/`history` | Unknown session id (`recovery: session not found`). `ccresu sessions --all` lists what exists. |
| Exit `4` from `recover` | The session is not eligible: settled (`RESUMED`, `COMPLETED`, `USER_TERMINATED`, `DISABLED`, `UNRECOVERABLE`), disabled, or out of attempts. `ccresu inspect <id>` shows the state and the last transition reason. |
| `recovery: maximum attempts reached` | The budget for this discovery cycle (default 10) is spent. The session has been returned to `DISCOVERED`; the next discovery pass finds its reset time and tries again. `ccresu retry <id>` or `ccresu recover <id> --force` pushes it now. |
| `recovery: usage limit is still in force` | The recheck found the limit still active. This is correct behaviour: the session goes back to `WAITING_FOR_RESET` and waits for a new reset time. |
| `recovery: a recovery attempt is already running` | Per-session lock held by the daemon or another CLI invocation. Wait, or check for a stuck child with the identity recorded in the attempt. |
| `recovery: concurrency limit reached` | `recovery.max_concurrent_recoveries` (default 3) is saturated; the session stays in `RESET_TIME_REACHED` until a slot frees. |

## Discovery

| Symptom | Cause and fix |
| --- | --- |
| `sessions` prints nothing, `doctor` says `warn sessions no sessions discovered yet` | Nothing to see yet, or the wrong store. Check `claude agents --json` by hand, then `ccresu sessions --all`. |
| Sessions from another directory are missing | `claude.discover_all_sessions` is `false`, or the session lives outside the transcript root. |
| `claude: claude executable unavailable` | `claude.executable` is not on `PATH` (and not absolute). The daemon does not guess a path. |
| `claude: unsupported claude code version` | The CLI is older than 2.0.0 (`claude.MinimumVersion`). Upgrade Claude Code. |
| `claude: unrecognized claude code version` | `claude --version` printed something the parser does not know. Run it and compare with `2.1.223 (Claude Code)`. |
| State `UNRECOVERABLE` with a session type in the reason | `claude agents --json` reported a type ccresu does not know how to resume. It is reported, never invented into a command. |
| `--bg and --print conflict` in your shell | You combined `--bg` with `-p`. The CLI forbids it: `--print` never starts the session `claude agents` attaches to. ccresu passes `--bg` and never `-p`, for exactly this reason — drop `-p` if you want a background agent by hand, and expect a *new* session id for the continuation and no JSON result. |

## Detection and waiting

| Symptom | Cause and fix |
| --- | --- |
| A real limit was missed | `ccresu inspect <id>` shows the normalised text and the matched rule (or none). Lower `detector.threshold`, or add a `detector.custom_patterns` rule — patterns are matched against normalised text: lowercase, single-spaced, curly quotes and dashes folded. |
| Prose *about* limits was treated as a limit | Raise `detector.threshold` toward the canonical scores (0.95–0.99), or add a guard pattern. Note that raising it above 0.99 disables the built-in rules. |
| An auth error, network error or cancellation was resumed | It cannot be: those are explicit non-classifications, and `COMPLETED`/`USER_TERMINATED` are settled. If you believe otherwise, check `ccresu history <id>` for what was actually classified. |
| Reset time looks wrong | The stored instant is the phrase's own zone, not the host's. `ccresu inspect` prints the resolved instant, the zone and the original phrase. A message with no reset time uses the re-poll backoff (5m doubling to 30m), so the resume is later than the banner suggests — by design. |
| Resume never happens, state stuck at `WAITING_FOR_RESET` | The schedule row is in the future, or `monitoring.enabled`/`claude.resume_enabled` is off. `ccresu status` prints the next due time. Restarting is safe: the wait is in the database. |
| Resume happened but the state did not move to `RESUMED` | One of the proofs failed. `ccresu history <id>` shows `continuation_confirmed` and `transcript_grew` per attempt, and the reason names which one broke: `the background job reported no continuation session id`, `the background job <job> is still working on the continuation` when the job never left `working`, or `the continuation transcript did not grow`. The attempt is reported as unverified, never as a success. |

## Notifications

`ccresu doctor` shows `telegram disabled`, or `chat <id>, token ********abcd`. If a
message does not arrive, the delivery state is on disk:

```bash
TOKEN=$(cat ~/.config/ccresu/api-token)
curl -s -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:8787/api/v1/telegram/status
```

| Symptom | Cause and fix |
| --- | --- |
| Nothing at all, queue empty | `telegram.enabled: false`, or no `bot_token`/`chat_id`. |
| One event missing | Its `notify_*` switch is off. `notify_waiting` is off by default. |
| `401 Unauthorized` | Wrong or revoked bot token — check BotFather. |
| `400 chat not found` | Wrong `chat_id`, or the bot was never added to that chat. |
| `403 bot was blocked by the user` | Unblock the bot and send it `/start`. |
| `429` repeatedly | Rate limited; the queue keeps the row and backs off. |
| Rows marked failed | The attempt budget (5 per row) is spent; the reason is stored with the row. Fix the cause, then trigger a new event. |

Details and the event table are in [telegram.md](telegram.md).

## API

| Symptom | Cause and fix |
| --- | --- |
| `connection refused` on `127.0.0.1:8787` | `server.enabled: false`, a different `server.port`, or a process that is not the daemon (the API runs inside `daemon`, and standalone with `serve`). |
| `401 unauthorized` | Missing or wrong `Authorization: Bearer` header. The token is at `<configuration directory>/api-token` — beside the configuration file in use, so `/elsewhere/api-token` after `--config /elsewhere/config.yaml` — with mode `0600`; it is never accepted as a query parameter or cookie. |
| `403 origin_not_allowed` | A browser caller whose Origin is not in `server.allowed_origins`. `curl` is unaffected. |
| `409 not_eligible` | Same rule as exit code 4: the session is settled, disabled or unrecoverable. |
| `413 payload_too_large` | Body above 1 MiB. |
| `415 unsupported_media_type` | Missing or wrong `Content-Type` on a POST/PUT. |
| `429 too_many_requests` | Above 50 requests/second (burst 100) from your address; the `Retry-After` header says how long. |
| `503 unavailable` | The capability is not wired in this process (for example recovery in `serve`-only mode). |
| `api token file is empty or too short` | The token file was truncated; delete it and restart to regenerate. |

Routes, error codes and the SSE stream are in [api.md](api.md).

## Storage

| Symptom | Cause and fix |
| --- | --- |
| `database is locked` | Another process holds a write longer than `storage.busy_timeout` (default 5s). Two daemons sharing one `storage.path` is the usual cause — run one per user. |
| `unable to open database file` | The data directory does not exist or is not writable; `doctor`'s `data directory` check writes a probe file to prove it. |
| `migration ...` failure on startup | The file is not a ccresu database, or a newer schema written by a newer binary. Back up the file; downgrades need a restore. |
| Permissions look wrong after a copy | The store tightens the file to `0600` and the directory to `0700` on open; a copy made by hand may need `chmod` first. |

## The daemon

| Symptom | Cause and fix |
| --- | --- |
| Unit exits immediately | A broken configuration is fatal on purpose: `ccresu config validate`. |
| Unit stops at logout | Enable lingering: `sudo loginctl enable-linger "$USER"`. |
| Resumes start but never finish | `recovery.startup_timeout` (60s) and `recovery.verification_timeout` (120s) bound each attempt; check the transcript's location and that `~/.claude` is writable. |
| Load spike of `claude` processes | Lower `recovery.max_concurrent_recoveries`. |

Deployment specifics are in [deployment.md](deployment.md).

## Known limitations

Stated plainly, because a watchdog that over-promises is worse than one that
waits:

- **Usage limits only.** A resume is triggered by a recognised usage-limit
  interruption. A crash, a network failure or a rate limit from the API is
  classified as *not* a limit and is left alone.
- **Session types it does not understand are refused.** Anything
  `claude agents --json` reports as a type ccresu does not know becomes
  `UNRECOVERABLE` rather than a guessed command.
- **One process per user, per database.** The wait and the attempt budget live in
  SQLite; two daemons sharing one file will lock each other.
- **No web UI.** The surfaces are the CLI, the daemon and the loopback REST/SSE
  API. `web/` is a static marketing and documentation site that never talks to a
  running daemon.
- **Telegram is the only notification channel.** No email, webhook or desktop
  notification.
- **The reset time is only as good as the banner.** When the message carries no
  reset time, ccresu re-polls with exponential backoff (5m → 30m) instead of
  guessing, so the resume can lag the real reset.
- **Flags and positionals may be interleaved.** `ccresu status --json` and
  `ccresu recover <id> --json` behave exactly like their flag-first forms; use
  `--` when a value itself begins with `-`.
- **`telegram.parse_mode` must be `HTML`.** It is the only mode the templates
  can render; `MarkdownV2` and `Markdown` are rejected by `config validate` and
  by the client rather than sent as markup Telegram cannot read.
- **The website is not a client.** It cannot reach your daemon, and does not try.
