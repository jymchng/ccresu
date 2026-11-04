import type { DocPage } from "./types";

export const cliDoc: DocPage = {
  slug: "cli",
  title: "CLI reference",
  summary:
    "Thirteen commands, five global flags, and exit codes a script can branch on.",
  blocks: [
    {
      kind: "code",
      label: "Usage",
      code: `ccresu [--config PATH] [--json] [--log-level LEVEL] <command> [flags]\nccresu --help | --version`,
    },
    {
      kind: "h2",
      text: "Global flags",
    },
    {
      kind: "table",
      head: ["Flag", "Meaning"],
      rows: [
        ["-c, --config PATH", "Configuration file. Default: ~/.config/ccresu/config.yaml."],
        ["--json", "Machine-readable output where the command supports it."],
        ["--log-level LEVEL", "debug, info, warn or error."],
        ["--log-format FMT", "json or text."],
        ["-v, --verbose", "Shorthand for --log-level debug."],
        ["-q, --quiet", "Shorthand for --log-level error."],
      ],
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
        ["status", "Summarise configuration path, database, session counts, pending recoveries, schedules and Telegram state."],
        ["sessions", "List discovered sessions. `--all` includes disabled and settled ones, `--state` filters (repeatable), `--limit` caps the output."],
        ["inspect <session-id>", "Show everything known about one session: identity, state, schedule, attempts and recent transitions."],
        ["recover <session-id>", "Resume one session now. `--force` ignores a spent attempt budget; `--message` overrides the continuation message."],
        ["retry <session-id>", "Reset the attempt budget of a permanently failed session and let the daemon try again."],
        ["enable <session-id>", "Allow ccresu to recover a session."],
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
      title: "Global flags go anywhere",
      text: "Global flags are accepted before or after the command name: `ccresu --json status` and `ccresu status --json` are the same invocation, and flags may be interleaved with positionals (`ccresu recover <id> --json`). A flag value is never mistaken for a global flag, so `ccresu recover <id> --message --json` sends the text `--json`. `ccresu <command> --help` lists every flag that command accepts, global ones included.",
    },
  ],
};
