# Deployment

One ccresu process per user is the whole deployment. It is a foreground loop
(`ccresu daemon`); everything else is a supervisor and a log destination.

It is a **user** process on purpose: it reads `~/.claude`, writes into your
project directories and drives `claude` with your credentials. Run it as you —
never as root, never as a shared service account.

## Layout

| Path | Contents |
| --- | --- |
| `~/.local/bin/ccresu` | The binary (`0755`). |
| `~/.config/ccresu/config.yaml` | Configuration (`0600`). |
| `~/.config/ccresu/api-token` | API bearer token, created on first `serve`/`daemon` (`0600`). |
| `~/.local/share/ccresu/ccresu.db` | SQLite state: sessions, schedules, attempts, notifications (`0600`). |
| `~/.local/state/ccresu/` | Reserved for local state (`0700`). |
| `~/.config/systemd/user/ccresu.service` | The user unit, if installed. |

Every path is configurable, and `ccresu config show` prints the effective values
together with their sources.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/jymchng/ccresu/main/scripts/install.sh -o install.sh
bash install.sh --systemd
```

Without `--systemd` the script stops after verifying the binary and the
configuration; you then start the loop yourself.

```bash
./scripts/install.sh --dry-run --version 0.1.0   # see every action first
```

Upgrading, pinning, mirrors and uninstalling are covered in
[upgrade.md](upgrade.md).

## Run under systemd (recommended on Linux)

The shipped unit is `deployments/systemd/ccresu.service`, installed to
`~/.config/systemd/user/ccresu.service` by `install.sh --systemd`.

By hand:

```bash
mkdir -p ~/.config/systemd/user
cp deployments/systemd/ccresu.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now ccresu.service
```

```console
$ systemctl --user status ccresu.service
● ccresu.service - ccresu - resume Claude Code sessions after usage limits reset
     Loaded: loaded (/home/you/.config/systemd/user/ccresu.service; enabled; ...)
     Active: active (running) since ...
   Main PID: 4021 (ccresu)
```

```bash
systemctl --user restart ccresu.service    # after a configuration change
systemctl --user stop ccresu.service
systemctl --user disable --now ccresu.service
```

What the unit does, and why:

- `Restart=on-failure` with `RestartSec=10s` — a watchdog that silently dies is
  worse than no watchdog. A broken configuration still stops the unit, because
  systemd's default start limit ends the restart loop instead of spinning.
- `KillSignal=SIGTERM`, `TimeoutStopSec=60s` — an in-flight
  `claude -p --resume` child gets 60 seconds to finish or be killed cleanly
  before SIGKILL.
- `After=network-online.target` — the daemon discovers locally, but benefits from
  DNS being up before it talks to Telegram.
- `Environment=CCRESU_APP__LOG_FORMAT=json` — journald already timestamps and
  rotates; structured lines survive `journalctl -o json` intact.
- Hardening is the cheap, non-breaking subset: `NoNewPrivileges=yes`,
  `PrivateTmp=yes`, `ProtectControlGroups=yes`, `ProtectKernelTunables=yes`,
  `RestrictSUIDSGID=yes`. `ProtectHome` and `ProtectSystem=strict` are
  deliberately **absent**: the daemon must write `~/.claude` and your projects.

### Keep it running after logout

A user manager stops when your last session ends. To let the watchdog survive:

```bash
sudo loginctl enable-linger "$USER"
```

Without lingering the unit still works while you are logged in, and starts at
your next login — but a limit that resets at 3am will not be resumed until then.

## Run without systemd

```bash
ccresu daemon                 # foreground, Ctrl-C to stop
ccresu daemon --interval 15s  # reconcile more often than claude.discovery_interval
ccresu daemon --once          # one reconcile pass, then exit (good for cron and tests)
```

`--once` is the primitive for any scheduler you already trust:

```cron
*/5 * * * * /home/you/.local/bin/ccresu daemon --once >> /home/you/.local/state/ccresu/cron.log 2>&1
```

If you supervise it yourself, forward `SIGTERM` (not `SIGKILL`) so the current
attempt can be recorded, and restart on any non-zero exit.

On macOS there is no shipped LaunchAgent; `ccresu daemon` under `launchd`,
`tmux`, or a shell profile line is the supported path, and the state survives
restarts because it lives in SQLite.

## Logs

The daemon logs one line per meaningful event: discovery counts, detections, the
scheduled reset, each attempt's two proofs, notification delivery failures.

```bash
journalctl --user -u ccresu.service -f            # follow
journalctl --user -u ccresu.service --since '1 hour ago'
journalctl --user -u ccresu.service -o json | jq -r 'select(.PRIORITY<=4) | .MESSAGE'
```

Format is chosen by `--log-format` or `CCRESU_APP__LOG_FORMAT`: `json` for
journald and log shippers (the unit's setting), `text` for a terminal. Level is
`--log-level` / `CCRESU_APP__LOG_LEVEL`, with `-v` and `-q` as shorthands.
Secrets — the bot token, the API token, anything whose key looks like a
credential — are redacted in both formats.

## Verifying a deployment

```bash
ccresu version
ccresu config validate
ccresu doctor
```

`doctor` is the one command that exercises the real paths: configuration load,
database open with its schema version, the Claude Code CLI and its transcripts,
the Telegram settings (token masked), and whether any session has been
discovered. Exits non-zero when a check fails.

```console
$ ccresu doctor
STATUS  CHECK           DETAIL
ok      config          /home/you/.config/ccresu/config.yaml
ok      timezone        Europe/Berlin
ok      data directory  /home/you/.local/share/ccresu
ok      database        /home/you/.local/share/ccresu/ccresu.db (schema version 3)
ok      claude          2.1.223 (claude --version)
ok      transcripts     session transcripts are readable
ok      telegram        chat -1001234567890, token ********abcd
ok      sessions        2 discovered
```

A `warn` (no sessions yet, for example) does not fail the command; only a `fail`
does, and the failing checks are named on stderr.

Then confirm the loop sees work:

```bash
ccresu status             # counts and the next due schedule
ccresu sessions           # what is known
curl -s localhost:8787/api/v1/health
```

## Liveness and monitoring

The daemon exposes the same health surface to your monitoring as to you:

- `GET http://127.0.0.1:8787/api/v1/health` — cheap, reads no configuration.
- `ccresu doctor` — a full check, suitable for a timer or a pre-flight.
- `ccresu status --json` — machine-readable snapshot for a dashboard.

```bash
# a systemd timer, every 15 minutes
ccresu doctor >/dev/null || systemctl --user restart ccresu.service
```

Prometheus scraping is not built in; script it against the endpoints above if you
need it.

## Choosing settings for an unattended host

| Setting | Why it matters unattended |
| --- | --- |
| `recovery.max_concurrent_recoveries` | Keep it low (default `3`) so a burst of limits does not spawn a dozen `claude` children. |
| `recovery.max_attempts` | The budget that ends retries and marks the session `PERMANENTLY_FAILED`. |
| `scheduler.recheck_before_resume` | Leave `true`: re-reads the session before resuming, so a limit that cleared early is not resumed twice. |
| `telegram.enabled` | The only way to learn about a failure on a headless box; see [telegram.md](telegram.md). |
| `server.host` | Leave `127.0.0.1`. Read [security.md](security.md) before changing it. |
| `storage.path` | Point it at a backed-up or larger disk if `~` is small; the schema migrates itself on open. |

## Multiple users and multiple machines

State is per user and per database. Two daemons sharing one machine are fine as
long as each runs as its own user with its own database; they will each discover
their own `~/.claude`. Do not point two daemons at the same `storage.path` —
the wait and the attempt budget are enforced in that file.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Unit exits immediately, `status=78` | `ccresu config validate` — a broken config is fatal by design. |
| Unit stops after logout | `loginctl show-user "$USER" -p Linger` — should say `yes`. |
| `ExecStart` path wrong | The installer rewrites `%h/.local/bin/ccresu` to your `--prefix`; if you moved the binary, fix the unit. |
| Sessions never discovered | `ccresu doctor` → `claude` and `transcripts`; then `ccresu sessions --all`. |
| Resumes never happen | `ccresu status` for the next due time, `ccresu enable <id>` for the opt-in, `ccresu history <id>`. |
| Notifications silent | `ccresu doctor` → `telegram`, then [telegram.md](telegram.md). |
| Database locked | `storage.busy_timeout` (default `5s`); another process is holding a write. |

Restarting is always safe: the wait is a row in the database, so a new process
picks up an in-flight schedule instead of losing it.
