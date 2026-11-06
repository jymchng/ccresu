import type { DocPage } from "./types";

export const troubleshootingDoc: DocPage = {
  slug: "troubleshooting",
  title: "Troubleshooting",
  summary:
    "Diagnose with doctor, inspect and history, then match the symptom.",
  blocks: [
    {
      kind: "h2",
      text: "Start with the two diagnostic commands",
    },
    {
      kind: "code",
      label: "Environment and per-session state",
      code: `ccresu doctor\nccresu inspect <session-id>\nccresu history <session-id> --limit 50`,
    },
    {
      kind: "p",
      text: "`doctor` checks the configuration, timezone, data directory, database schema version, the Claude Code CLI version, transcript readability, Telegram and discovery. `inspect` shows what ccresu believes about one session; `history` shows every transition it recorded.",
    },
    {
      kind: "h2",
      text: "Symptoms",
    },
    {
      kind: "table",
      head: ["Symptom", "Likely cause and fix"],
      rows: [
        ["`sessions: 0` in status", "Discovery found nothing: check that `claude agents --json` works as your user, then run `ccresu doctor` and look at the `claude` and `transcripts` rows."],
        ["The limit message was seen but no session is waiting", "The banner scored below `detector.threshold`, or the wording is unknown. Add a `detector.custom_patterns` rule, or lower the threshold and re-run `ccresu config validate`."],
        ["Waiting for a reset time that looks wrong", "The message's own timezone wins over the host timezone, and a message without one falls back to polling. Check `ccresu inspect` for the timezone it parsed and the deadline it stored."],
        ["`state: DISCOVERED`", "The attempt budget is spent or the error was classified as non-retryable. `ccresu history` shows the classified error; `ccresu retry <session-id>` resets the budget once the cause is fixed."],
        ["`state: UNRECOVERABLE`", "The session type or the observed error cannot be resumed through `claude --resume <id> --bg`. This is reported instead of inventing an undocumented command."],
        ["Recovery ran but the transcript did not grow", "The resume process started and exited without producing work. The verifier refuses to record RESUMED; check the recorded error and the resumes' output in the logs."],
        ["Exit code 2", "Usage error: an unknown flag, or a flag that command does not define. Global flags work before or after the command name; `ccresu <command> --help` lists the command's flags."],
        ["Exit code 3", "No such session id. `ccresu sessions --all` lists every id ccresu knows."],
        ["Exit code 4", "The session exists but is not eligible: it is disabled, its reset time has not arrived, or it is already RESUMED \u2014 in which case ccresu returns it to DISCOVERED so the continuation transcript is read again for the next limit."],
        ["The API returns 401", "The request changes state and needs `Authorization: Bearer <token>`. Read the token from `~/.config/ccresu/api-token`; it is not the Telegram token."],
        ["The API returns 403", "The browser Origin is not in `server.allowed_origins`. Add it, or call the API from a script, which sends no Origin."],
        ["The API returns 429", "The rate limit was hit. Wait for the `Retry-After` seconds, or reduce the request rate."],
        ["Telegram messages never arrive", "Check `ccresu doctor` for the Telegram row, the event switches in the configuration, and the chat id; a wrong chat id is rejected by the Bot API."],
        ["`database is locked`", "Another process holds a long write. Ensure only one daemon runs per database, and consider raising `storage.busy_timeout`."],
      ],
    },
    {
      kind: "h2",
      text: "Reading the logs",
    },
    {
      kind: "code",
      label: "Debug logging as JSON",
      code: `ccresu --log-level debug --log-format json daemon\nsystemctl --user status ccresu\njournalctl --user -u ccresu -n 100 --no-pager`,
    },
    {
      kind: "p",
      text: "Every line is a structured record with a stable level and message; secrets are redacted before the line is written, so a token never appears in the output.",
    },
    {
      kind: "h2",
      text: "Resetting state",
    },
    {
      kind: "note",
      tone: "warning",
      title: "Deleting the database forgets every schedule",
      text: "The database is the only record of pending recoveries. Back it up before deleting it: `cp ~/.local/share/ccresu/ccresu.db ~/ccresu.db.bak`, then stop the daemon first so the WAL is flushed.",
    },
    {
      kind: "p",
      text: "For a single session, prefer `ccresu retry` (reset the attempt budget), `ccresu retry` (clear a rediscovered session, leaving it disabled), `ccresu enable`/`disable` (stop recovery without forgetting history), or `ccresu recover --force` (try again now) over deleting state.",
    },
  ],
};
