import type { DocPage } from "./types";

export const telegramDoc: DocPage = {
  slug: "telegram",
  title: "Telegram notifications",
  summary:
    "Optional, deduplicated, off the recovery path: a Telegram outage can never block a resume.",
  blocks: [
    {
      kind: "h2",
      text: "Create a bot and find the chat",
    },
    {
      kind: "ol",
      items: [
        "Talk to @BotFather, run `/newbot`, and copy the token it returns.",
        "Send the bot a message so the chat exists.",
        "Read your chat id from `curl https://api.telegram.org/bot<token>/getUpdates` (the `chat.id` field), or use a public id such as `@channelname` for a channel.",
      ],
    },
    {
      kind: "h2",
      text: "Configure",
    },
    {
      kind: "code",
      label: "Prefer the environment for the token",
      code: `export CCRESU_TELEGRAM__BOT_TOKEN="123456:ABC-DEF..."\n\n# ~/.config/ccresu/config.yaml\ntelegram:\n  enabled: true\n  chat_id: "123456789"\n  parse_mode: HTML\n  notify_limit_detected: true\n  notify_waiting: false\n  notify_recovery_started: true\n  notify_recovery_success: true\n  notify_recovery_failure: true\n  notify_permanent_failure: true\n  notify_session_completed: true`,
    },
    {
      kind: "p",
      text: "`ccresu doctor` reports `telegram disabled` or `telegram configured`, and the API exposes the same state at `GET /api/v1/telegram/status` with the token redacted.",
    },
    {
      kind: "h2",
      text: "Events",
    },
    {
      kind: "table",
      head: ["Event", "Sent when"],
      rows: [
        ["limit detected", "A usage-limit banner was matched and recorded."],
        ["waiting", "The session is waiting for its reset time (off by default: it is the noisiest event)."],
        ["recovery started", "A resume attempt is about to run."],
        ["recovery success", "The resume was verified: the job named its continuation and that continuation's transcript grew."],
        ["recovery failure", "An attempt failed and another one is scheduled."],
        ["permanent failure", "The attempt budget is spent or the error is not retryable."],
        ["session completed", "The session finished normally, so no recovery is needed."],
      ],
    },
    {
      kind: "h2",
      text: "Delivery behaviour",
    },
    {
      kind: "ul",
      items: [
        "Messages are HTML-escaped: a project path or session id containing `<` or `&` cannot break the message or inject markup.",
        "The templates are HTML, so `parse_mode: HTML` is the only accepted value; `MarkdownV2` and `Markdown` are rejected at config validation rather than arriving as literal markup.",
        "HTTP 429 responses honour `retry_after`, and transient failures retry with backoff.",
        "Delivery state is persisted per event, so the same event is not sent twice across restarts.",
        "Sending happens off the recovery path: a slow or failing Telegram API cannot delay or fail a resume.",
        "The bot token is redacted from every log line and error message, including request errors that embed the URL.",
      ],
    },
    {
      kind: "note",
      tone: "warning",
      title: "Bot tokens are credentials",
      text: "Anyone with the token can post as your bot. Keep it in the environment or a 0600 file, and revoke it with @BotFather if it leaks. ccresu never logs it and never returns it from the API.",
    },
  ],
};
