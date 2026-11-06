import { defaultPaths } from "@/lib/site";

import type { DocPage } from "./types";

export const configurationDoc: DocPage = {
  slug: "configuration",
  title: "Configuration",
  summary:
    "One YAML file, validated at startup, with environment variables that always win.",
  blocks: [
    {
      kind: "p",
      text: `ccresu reads \`${defaultPaths.config}\` by default, or the path given with \`--config\`. The full annotated example lives in \`configs/config.example.yaml\`; every field in it is validated, and an invalid value is a startup error rather than a silent fallback.`,
    },
    {
      kind: "p",
      text: "Resolution order, first match wins: `--config PATH` (or `-c PATH`), then the `CCRESU_CONFIG` environment variable, then the default path. A leading `~` is expanded in all three, and everything derived from the file — such as the API token beside it — follows the file actually in use.",
    },
    {
      kind: "h2",
      text: "Environment overrides",
    },
    {
      kind: "p",
      text: "Every field can be overridden with a `CCRESU_` prefixed variable: upper-case the section and field, and join nesting with a double underscore. The environment always takes precedence over the file, which keeps secrets out of it.",
    },
    {
      kind: "code",
      label: "Environment",
      code: `CCRESU_APP__LOG_LEVEL=debug\nCCRESU_RECOVERY__MAX_ATTEMPTS=5\nCCRESU_TELEGRAM__BOT_TOKEN=123456:...\nCCRESU_SERVER__PORT=8787\n\nccresu config validate`,
    },
    {
      kind: "h2",
      text: "Sections",
    },
    {
      kind: "table",
      head: ["Section", "What it controls"],
      rows: [
        ["app", "Log level and format, and the IANA timezone used for display."],
        ["claude", "Executable name, discovery interval, extra monitored sessions, and the continuation message sent on resume."],
        ["monitoring", "Which detections are on: usage limits distinguish from session completion."],
        ["detector", "Confidence threshold and custom regular expressions for phrasings ccresu does not know yet."],
        ["scheduler", "Poll interval, and the exponential backoff used when a limit message carries no reset time."],
        ["recovery", "Attempt budget, backoff with jitter, startup and verification timeouts, and the concurrency limit."],
        ["telegram", "Bot token, chat id, parse mode and one switch per notification event."],
        ["server", "Loopback host, port, and the browser origins allowed to make read-only requests."],
        ["storage", "SQLite path and busy timeout."],
      ],
    },
    {
      kind: "h2",
      text: "Detection patterns",
    },
    {
      kind: "p",
      text: "Custom patterns are Go regular expressions matched against normalised text: lower-cased, single-spaced, with curly quotes and dashes folded to ASCII. A rule can require a reset time and carries its own confidence, which is compared with `detector.threshold`.",
    },
    {
      kind: "code",
      label: "Teaching ccresu a new phrasing",
      code: `detector:\n  threshold: 0.5\n  custom_patterns:\n    - id: internal-proxy-limit\n      kind: five_hour        # five_hour | weekly | other\n      pattern: 'proxy quota exhausted'\n      confidence: 0.9\n      requires_reset: false\n      description: our corporate proxy reports the limit differently`,
    },
    {
      kind: "h2",
      text: "Recovery tuning",
    },
    {
      kind: "table",
      head: ["Field", "Default", "Meaning"],
      rows: [
        ["max_attempts", "10", "Resume attempts before a session is marked DISCOVERED."],
        ["initial_delay", "30s", "First retry delay after a failed attempt."],
        ["max_delay", "15m", "Ceiling for the exponential backoff."],
        ["multiplier", "2.0", "Growth factor between attempts."],
        ["jitter", "0.15", "Random fraction added so retries do not synchronise."],
        ["startup_timeout", "60s", "How long a resume process may take to start producing output."],
        ["verification_timeout", "120s", "How long the verifier waits for the transcript to grow."],
        ["max_concurrent_recoveries", "3", "Recovery loops allowed to run at once."],
      ],
    },
    {
      kind: "h2",
      text: "Showing the effective configuration",
    },
    {
      kind: "code",
      label: "Validate, then print",
      code: `ccresu config init          # write the shipped example, mode 0600, never overwriting\nccresu config init --force  # replace an existing file with the shipped example\nccresu config validate      # exits non-zero on the first invalid field\nccresu config show          # effective values after defaults and env overrides\nccresu --json config show   # the same, as JSON`,
    },
    {
      kind: "note",
      tone: "warning",
      title: "Secrets",
      text: "Prefer `CCRESU_TELEGRAM__BOT_TOKEN` over writing the token into the file, and keep the file readable only by you: `chmod 600 ~/.config/ccresu/config.yaml`. The API never returns the token, and logs redact it.",
    },
  ],
};
