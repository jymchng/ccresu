# ccresu

ccresu is a CLI and daemon that resumes Claude Code sessions after a usage limit
resets. You point it at the sessions on your machine, it watches for the ones that
have stopped on a limit, waits for the reset window, and resumes them.

Everything is a command or a local API. ccresu deliberately ships no web UI: the
daemon, the CLI and the optional local HTTP API are the whole surface.

## How it works

1. The daemon scans the sessions Claude Code has written and records them in its
   store.
2. Sessions that stopped on a usage limit are detected, and the reset time is used
   to schedule a recovery.
3. When the schedule is due, ccresu resumes the session with the same session ID.
4. Every attempt is recorded, so you can inspect what happened, retry a session
   whose attempts are spent, or disable a session entirely.

Recovery is opt-in per session (`ccresu enable` / `ccresu disable`) and bounded by
an attempt budget configured globally.

## Build and install

```sh
go build ./cmd/ccresu
go install github.com/jymchng/ccresu/cmd/ccresu@latest
```

## Quick start

```sh
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

`ccresu status` gives a one-screen summary of the persisted state: how many
sessions are known, what state they are in, how many recoveries and notifications
are queued, and when the next schedule is due.

```sh
ccresu status
ccresu status --json
```

## Documentation

| Document | Read it for |
| --- | --- |
| [cli.md](cli.md) | Every command, flag and exit code |
| [configuration.md](configuration.md) | Every setting, its default and its environment override |
| [recovery.md](recovery.md) | Detection, scheduling, resumption and the state machine |
| [architecture.md](architecture.md) | How the pieces fit together |
| [api.md](api.md) | The local REST and SSE API |
| [telegram.md](telegram.md) | Notification setup, delivery guarantees and troubleshooting |
| [deployment.md](deployment.md) | Running it as a service, logs, monitoring |
| [upgrade.md](upgrade.md) | Installing, upgrading and uninstalling |
| [security.md](security.md) | What it can touch, and what it keeps secret |

Run `ccresu --help` for the command index, and `ccresu <command> --help` for the
flags of one command.
