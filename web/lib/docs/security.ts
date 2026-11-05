import { localApi } from "@/lib/site";

import type { DocPage } from "./types";

export const securityDoc: DocPage = {
  slug: "security",
  title: "Security",
  summary:
    "Loopback-only API, a token for every write, no terminal automation and no telemetry.",
  blocks: [
    {
      kind: "p",
      text: "ccresu runs on the machine that has your Claude Code credentials, so its threat model is the local one: another user on the box, a browser page you happened to open, and anything that can read your home directory.",
    },
    {
      kind: "h2",
      text: "How the resume is executed",
    },
    {
      kind: "ul",
      items: [
        "Exactly one command is ever built: `claude -p --resume <session-id> --output-format json \"<message>\"`, with the session id and message passed as separate arguments, never through a shell.",
        "No pty is allocated and no keystrokes are injected, so ccresu cannot type into a terminal you are using.",
        "A session id is validated before use, so a crafted id cannot become a flag or a path traversal.",
        "The API has no endpoint that executes a command: recovery goes through the recovery manager, and settings are validated configuration fields.",
        "No new session is ever started in place of the original one.",
      ],
    },
    {
      kind: "h2",
      text: "API protections",
    },
    {
      kind: "table",
      head: ["Control", "Behaviour"],
      rows: [
        ["Binding", `Defaults to \`${localApi.address}\`; a bare \`:port\` is rewritten to loopback.`],
        ["Authentication", `Every state-changing request needs \`Authorization: Bearer <token>\` from \`${localApi.tokenFile}\` (mode 0600, created on first start, constant-time comparison).`],
        ["CSRF", "The token is read only from the Authorization header, never from a cookie or a query parameter, so a cross-site request cannot supply it; there is no session cookie to ride on."],
        ["Origins", "Browser requests from an origin outside `server.allowed_origins` are rejected with 403, including preflight."],
        ["Rate limits", "Per-client token buckets, with a hard cap on the bucket table, and `Retry-After` on 429."],
        ["Size limits", "Request bodies are capped, and a mutating request must be JSON with the right content type."],
        ["Headers", "`nosniff`, frame-deny, no-referrer, a restrictive Content-Security-Policy, and a panic in a handler becomes a 500 instead of a crash."],
        ["Redaction", "The API returns the configuration and Telegram state with the bot token redacted, and never returns the API token."],
      ],
    },
    {
      kind: "h2",
      text: "Local files",
    },
    {
      kind: "ul",
      items: [
        "`~/.config/ccresu/config.yaml` may hold the Telegram bot token: keep it 0600, or use the environment instead.",
        "`~/.config/ccresu/api-token` (beside the configuration file in use) is created 0600 and tightened back to 0600 if it was copied around with looser permissions.",
        "`~/.local/share/ccresu/ccresu.db` holds session ids, project paths, schedules and attempt history: it references your work but never stores conversation content.",
        "Logs are structured JSON (or text) with secrets redacted before they are written.",
      ],
    },
    {
      kind: "h2",
      text: "What ccresu never does",
    },
    {
      kind: "ul",
      items: [
        "It sends nothing to a third party except the Telegram messages you enabled.",
        "It ships no embedded web UI: the binary serves only the JSON/SSE API.",
        "It does not bypass or work around Anthropic usage limits, it only waits for the documented reset.",
        "It does not read files outside the Claude Code directories it needs.",
      ],
    },
    {
      kind: "h2",
      text: "Reporting a vulnerability",
    },
    {
      kind: "p",
      text: "Open a private security advisory on the repository rather than a public issue, and include the version from `ccresu version`.",
    },
  ],
};
