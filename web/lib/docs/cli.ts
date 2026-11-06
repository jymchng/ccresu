import type { DocPage } from "./types";

export const cliDoc: DocPage = {
  slug: "cli",
  title: "CLI reference",
  summary:
    "Fourteen commands, five global flags, and exit codes a script can branch on.",
  blocks: [
    {
      kind: "code",
      label: "Usage",
      code: `ccresu [--config PATH] [--json] [--log-level LEVEL] <command> [flags]\nccresu --help | --version`,
    },
    {
      kind: "capture",
      id: "help",
    },
    {
      kind: "h2",
      text: "Global flags",
    },
    {
      kind: "table",
      head: ["Flag", "Meaning"],
      rows: [
        ["-c, --config PATH", "Configuration file. Default: ~/.config/ccresu/config.yaml; `CCRESU_CONFIG` overrides the default, `--config` overrides both."],
        ["--json", "Machine-readable output where the command supports it."],
        ["--log-level LEVEL", "debug, info, warn or error."],
        ["--log-format FMT", "json or text."],
        ["-v, --verbose", "Shorthand for --log-level debug."],
        ["-q, --quiet", "Shorthand for --log-level error."],
      ],
    },
    {
      kind: "p",
      text: "The configuration file is chosen in this order, first match wins: `-c/--config PATH`, then `CCRESU_CONFIG` in the environment, then `$XDG_CONFIG_HOME/ccresu/config.yaml` (else `~/.config/ccresu/config.yaml`). A leading `~` is expanded, and paths derived from the file — such as the API token beside it — follow the file actually in use.",
    },
    {
      kind: "h2",
      text: "Commands",
    },
    {
      kind: "table",
      head: ["Command", "What it does"],
      rows: [
        ["daemon", "Run the watchdog loop until interrupted. `--once` runs a single reconcile pass; `--interval` overrides the reconcile interval."],
        ["status", "Summarise configuration path, database, session counts, pending recoveries, schedules and Telegram state. Its `sessions` line is the total row count; its `listing` line says how many a bare `sessions` shows."],
        ["sessions", "List discovered sessions. A bare listing is enabled only and says so in the footer; `--all` includes disabled and settled ones, `--state` filters (repeatable), `--limit` caps the output and says when it truncated."],
        ["sessions status <session-id>", "Show everything known about one session: identity, state, schedule, attempts and recent transitions. `inspect <session-id>` is the same command."],
        ["sessions delete <session-id>", "Remove the session from ccresu \u2014 its row, attempts, transitions, usage limits, pending wait and notifications. Nothing under ~/.claude is touched; a tombstone keeps discovery from re-creating it, and `enable <session-id>` undoes the delete."],
        ["inspect <session-id>", "Alias of `sessions status`."],
        ["recover <session-id>", "Resume one session now. `--force` ignores a spent attempt budget; `--message` overrides the continuation message."],
        ["retry <session-id>", "Spend one attempt now instead of waiting for the next discovery pass."],
        ["enable <session-id>", "Allow ccresu to recover a session. Also the undo for `sessions delete`: the tombstone is cleared and the row re-created before it is enabled."],
        ["disable <session-id>", "Stop ccresu from recovering a session without forgetting its history."],
        ["history <session-id>", "Print the state transitions and attempts; `--limit` caps how many are shown."],
        ["config init", "Write the shipped example configuration to the resolved path with mode 0600. `--force` replaces an existing file; without it the command refuses and writes nothing."],
        ["config validate", "Validate the file, the environment overrides and every default."],
        ["config show", "Print the effective configuration with secrets redacted."],
        ["doctor", "Check the config, timezone, data directory, database schema, Claude Code CLI, transcript access, Telegram and discovery."],
        ["serve", "Run the REST + SSE API. `--addr HOST:PORT` overrides `server.host:server.port`."],
        ["version", "Print version, commit and build date."],
      ],
    },
    {
      kind: "h2",
      text: "Exit codes",
    },
    {
      kind: "table",
      head: ["Code", "Meaning"],
      rows: [
        ["0", "Success."],
        ["1", "Failure: the operation ran and did not succeed."],
        ["2", "Usage error: unknown command, unknown flag or bad arguments."],
        ["3", "Not found: no such session."],
        ["4", "Not eligible: the session exists but cannot be recovered right now."],
        ["130", "Interrupted (SIGINT)."],
      ],
    },
    {
      kind: "h2",
      text: "Examples",
    },
    {
      kind: "code",
      label: "A typical session",
      code: `ccresu status\nccresu sessions --state WAITING_FOR_RESET\nccresu inspect 4d1f9c2a-... \nccresu history 4d1f9c2a-... --limit 20\nccresu recover 4d1f9c2a-... --force\nccresu --json sessions | jq '.[].id'`,
    },
    {
      kind: "note",
      tone: "info",
      title: "A background session is stopped, not abandoned",
      text: "When `claude -p --resume` refuses because the session is still attached to Claude Code's background agent (`That session is running in the background (837eaaea) …`), ccresu runs `claude stop <job-id>` — the eight lower-case hex job name the refusal itself prints (`837eaaea`), not the session UUID; `claude stop` resolves job names only and answers a UUID with `No job matching` and exit 1 — and repeats the identical resume once inside the same attempt. The conversation is kept — same session id, same transcript — and the stop is logged rather than recorded as the attempt's command. If the stop fails, or the second resume refuses again, the original error is reported unchanged.",
    },
    {
      kind: "note",
      tone: "info",
      title: "Session counts: total vs listed",
      text: "`ccresu status` counts every stored session row; a bare `ccresu sessions` lists only the enabled ones. Both say so: `status` prints `sessions: 27 total, 1 enabled` followed by `listing: ccresu sessions shows the 1 enabled; ccresu sessions --all shows all 27`, and the listing footer reads `1 session(s) shown: enabled only (of 27 matching; --all includes disabled and settled)`. A `--limit` that truncates adds `limit N reached; use --limit 0 to list every match`. `status --json` exposes `sessions` (total), `enabled_sessions` and `sessions_listed_by_default`, the last matching what a bare `sessions --json` returns.",
    },
    {
      kind: "capture",
      id: "status",
    },
    {
      kind: "capture",
      id: "sessions",
    },
    {
      kind: "note",
      tone: "info",
      title: "Global flags go anywhere",
      text: "Global flags are accepted before or after the command name: `ccresu --json status` and `ccresu status --json` are the same invocation, and flags may be interleaved with positionals (`ccresu recover <id> --json`). A flag value is never mistaken for a global flag, so `ccresu recover <id> --message --json` sends the text `--json`. `ccresu <command> --help` lists every flag that command accepts, global ones included.",
    },
  ],
};
