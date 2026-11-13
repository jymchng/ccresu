# ccresu

ccresu is a CLI and daemon that resumes Claude Code sessions after a usage limit
resets. You point it at the sessions on your machine, it watches for the ones that
have stopped on a limit, waits for the reset window, and resumes them.

Everything is a command or a local API. ccresu deliberately ships no web UI: the
daemon, the CLI and the optional local HTTP API are the whole surface.

## How it works

One interruption passes through seven stages. [recovery.md](recovery.md) is the
mechanism reference for each of them; [architecture.md](architecture.md) shows
how the packages and tables fit together.

1. **Discovery.** The daemon asks Claude Code what is running
   (`claude agents --json`) and also reads the transcripts under
   `~/.claude/projects/` and the per-session directories under
   `~/.claude/session-env/`. Each session is stored with the source that found
   it — `agents`, `transcript` or `session-env` — and starts in `DISCOVERED`.
2. **Detection.** The newest 25 transcript lines are normalised and scored
   against 19 built-in patterns. Guards demote text that is *about* a limit
   rather than a report of one (a quoted, negated, fenced or conditional
   phrase), and a score below `0.5` is not a limit. The explicit non-limit
   verdicts — auth failure, invalid key, network error, user cancellation,
   normal completion — are recognised in their own right, so they are never
   resumed after a reset.
3. **The usage-limit record.** A detection becomes one `usage_limits` row: the
   banner verbatim, the resolved reset instant, the zone and phrase the CLI
   wrote, and the detector's confidence. A reset learned later is written as a
   new row, not as an in-place rewrite.
4. **The schedule.** The reset instant becomes one `schedules` row per session,
   an upsert, so a session that hits its limit twice replaces its stale wait
   with the new one. When the banner named no usable time the row is marked
   `unknown_reset` and ccresu polls instead — 5m, 10m, 20m, then every 30m —
   rather than guessing. Both the instant and the poll count are persisted, so a
   restart continues the same wait instead of starting it again.
5. **The resume.** When the schedule is due the session walks
   `WAITING_FOR_RESET → RESET_TIME_REACHED → CHECKING_SESSION → CHECKING_LIMIT →
   RESUMING`, and ccresu runs
   `claude -p --resume <session-id> --output-format json <message>` — the same
   session id, with the continuation message last.
6. **Verification.** Exit code 0 is not proof. The resume counts only when the
   reported session id matches the stored one **and** the transcript has grown
   past the stored baseline; the wait for that growth is bounded by 120s and
   re-checked every 10s. Only then is the attempt closed as a success and the
   session marked `RESUMED`.
7. **A refusal is a wait, not a failure.** If the CLI answers the resume with
   the limit banner, the attempt is finished with that banner, the banner is
   recorded as a usage limit, and the session returns to `WAITING_FOR_RESET`
   until the reset it named. It is not retried, and the attempt budget is not
   consulted: what ends the wait is the reset, not a count of tries. A failure
   that cannot be retried on this schedule returns the session to `DISCOVERED`,
   where the next discovery pass re-reads the transcript and schedules the next
   reset; a transient one waits in `RETRY_WAIT` and is tried again with backoff.

Every stage appends to the audit trail — a `state_transitions` row per state
change and a `recovery_attempts` row per resume — which is what
`ccresu history` and `ccresu inspect` replay.

Recovery is on by default (`recovery.enabled`). `ccresu disable` switches it off
for one session: the daemon records that as a decision instead of retrying, and
`ccresu enable` undoes it — as it also undoes `ccresu sessions delete`. Retries
within one cycle are bounded by the attempt budget
(`recovery.max_attempts`, ten) with a backoff of 30s doubling to 15m, and a
spent budget returns the session to `DISCOVERED` rather than to a failure state:
there is no terminal failure state.

## Build and install

```sh
go build ./cmd/ccresu
go install github.com/jymchng/ccresu/cmd/ccresu@latest
```

## Quick start

```sh
# create ~/.config/ccresu/config.yaml from the shipped example (mode 0600)
ccresu config init

# check that the environment is usable: config, store, Claude Code, notifier
ccresu doctor

# validate the configuration file and print the effective settings
ccresu config validate
ccresu config show

# list what ccresu has discovered
ccresu sessions

# allow ccresu to resume a session, then run the watchdog
ccresu enable <session-id>
ccresu daemon
```

For the full walkthrough — installation, discovery, a limit being detected, the
scheduled resume and the manual actions — see [tutorial.md](tutorial.md).

`ccresu status` gives a one-screen summary of the persisted state: how many
sessions are known and how many of those are enabled, what state they are in, how
many recoveries are pending and how many schedules exist, and when the next wake
is due. The queued-notification count is not on that screen; it is
`pending_notifications` in `ccresu status --json`.

```sh
ccresu status
ccresu status --json
```

## Documentation

| Document | Read it for |
| --- | --- |
| [tutorial.md](tutorial.md) | A hands-on walkthrough: config, first run, detection, resume, manual actions, API |
| [cli.md](cli.md) | Every command, flag and exit code |
| [configuration.md](configuration.md) | Every setting, its default and its environment override |
| [recovery.md](recovery.md) | Detection, scheduling, resumption and the state machine |
| [architecture.md](architecture.md) | How the pieces fit together |
| [api.md](api.md) | The local REST and SSE API |
| [telegram.md](telegram.md) | Notification setup, delivery guarantees and troubleshooting |
| [deployment.md](deployment.md) | Running it as a service, logs, monitoring |
| [upgrade.md](upgrade.md) | Installing, upgrading and uninstalling |
| [security.md](security.md) | What it can touch, and what it keeps secret |
| [troubleshooting.md](troubleshooting.md) | Symptom-to-cause tables, and known limitations |
| [development.md](development.md) | Layout, seams, tests, conventions |
| [releases.md](releases.md) | Tagging, assets, checksums |
| [website.md](website.md) | The static landing and documentation site |
| [delivery-report.md](delivery-report.md) | Measured gates, coverage and remaining gaps |

Run `ccresu --help` for the command index, and `ccresu <command> --help` for the
flags of one command.
