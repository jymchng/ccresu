# Telegram notifications

Notifications are optional. When they are off — the default — ccresu still
recovers sessions; you just find out from `ccresu status` instead of your phone.

Notifications never stand in the recovery path: a message is rendered, written to
SQLite and delivered by a sender goroutine, so a slow or unreachable Telegram
cannot delay or undo a resume.

## Turning it on

```yaml
telegram:
  enabled: true
  chat_id: "-1001234567890"
  parse_mode: HTML
```

Do not put the bot token in the file if you can avoid it — the environment is
easier to keep out of backups and shell history:

```bash
export CCRESU_TELEGRAM__BOT_TOKEN='123456789:AA...'
```

The plain aliases `TELEGRAM_ENABLED`, `TELEGRAM_BOT_TOKEN` and
`TELEGRAM_CHAT_ID` are also read, with the `CCRESU_TELEGRAM__*` names winning
when both are set. See [configuration.md](configuration.md) for the full
precedence rules.

Validate before starting the daemon:

```console
$ ccresu config validate
configuration /home/you/.config/ccresu/config.yaml is valid
telegram:      yes
$ ccresu doctor
ok      telegram  chat -1001234567890, token ********abcd
```

`doctor` masks the token to eight asterisks and its last four characters; the
value itself is never printed, and never returned by the API.

To get the two values:

1. Talk to [@BotFather](https://t.me/BotFather), create a bot, copy the token.
2. Add the bot to the chat you want messages in, send it one message, then read
   `https://api.telegram.org/bot<TOKEN>/getUpdates` and take `chat.id`. A
   supergroup id is negative and starts with `-100`.

## What you get

Every event is a separate switch, all `true` by default except `notify_waiting`.

| Event | YAML key | Subject |
| --- | --- | --- |
| Usage limit detected | `notify_limit_detected` | Usage limit detected |
| Waiting for the reset | `notify_waiting` (default `false`) | Waiting for the usage limit to reset |
| Recovery started | `notify_recovery_started` | Recovery started |
| Recovery succeeded | `notify_recovery_success` | Recovery succeeded |
| Attempt failed | `notify_recovery_failure` | Recovery attempt failed |
| Budget spent | `notify_permanent_failure` | Automatic recovery stopped |
| Session completed | `notify_session_completed` | Session completed |

A successful recovery looks like this:

```text
Recovery succeeded
Session: 4d1f9c2a-7e35-4b18-9a6f-2c8d0e1f3a45
Attempt: 1/10
The original conversation resumed and its transcript grew.
Duration: 2m14s
```

`notify_waiting` is off by default because it arrives immediately after
`notify_limit_detected` with almost the same content — turn it on only if you
want the reminder at the moment the wait begins rather than the moment the limit
lands.

## Escaping and length

`parse_mode` is `HTML` and is the only mode ccresu sends: `MarkdownV2` and
`Markdown` are rejected by config validation and by `NewClient`, because there
is no Markdown escaper or Markdown template behind them — accepting the value
would send literal `<b>` tags and unescaped metacharacters, mangling the message
or getting it rejected outright. Every interpolated
value — session id, project path, the raw limit banner, a CLI error — is escaped
in a single pass for `&`, `<`, `>`, `"`, identifiers are wrapped in `<code>`
and titles in `<b>`. A project directory named `a<b>&c` therefore cannot break
the markup, and a hostile transcript cannot inject formatting.

The Bot API rejects anything above 4096 characters; ccresu checks the length
after escaping and refuses locally with `ErrTooLong` instead of letting Telegram
truncate a message that would then read as if recovery failed.

## Delivery guarantees

- **Persisted first.** A notification is a row in SQLite with a dedupe key
  (limit id, attempt id or event). The database constraint makes a double
  delivery impossible even if the daemon is killed between send and mark.
- **Retried with a budget.** The sender drains pending rows every 30 seconds,
  and immediately on a wake-up signal. Each row gets 5 delivery attempts; the
  HTTP client inside contributes up to 4 attempts per send with exponential
  backoff (2s doubling, capped at 2 minutes, honouring a Telegram `retry_after`).
- **Failures are visible.** When the budget is spent the row is marked failed
  with the reason stored (truncated to 500 characters) rather than silently
  dropped.
- **Restart-safe.** Anything pending before a crash is picked up by the next
  start, because the queue is on disk rather than in memory.

Rate limits (`429`) and server faults (`5xx`) are retried; a bad token (`401`) or
an unknown chat (`400 chat not found`) is not — repeating it cannot help, and the
error is reported with the HTTP status and Telegram's own description.

## Inspecting delivery

The queue state is exposed read-only, so you can see whether the problem is
ccresu or Telegram:

```bash
TOKEN=$(cat ~/.config/ccresu/api-token)
curl -s -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:8787/api/v1/telegram/status
```

Send a test message through the same client, escaping and retry logic the real
events use:

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"telegram":true}' \
  http://127.0.0.1:8787/api/v1/test
```

Both routes are documented in [api.md](api.md).

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `401 Unauthorized` in the log | The bot token is wrong or was revoked in BotFather. |
| `400 chat not found` | Wrong `chat_id`, or the bot was never added to the chat. |
| `403 bot was blocked by the user` | The chat blocked the bot; unblock it and send `/start`. |
| `429` repeatedly | Telegram is rate limiting the bot; the queue keeps the message and backs off. |
| Nothing arrives, `enabled: false` | The switch is off: `ccresu config validate` prints `telegram: no`. |
| Nothing arrives, one event missing | The per-event `notify_*` switch is off for exactly that event. |

The token is a credential: it is registered with the log redactor at startup, so
it is scrubbed from every log line and error string, and it is masked in
`ccresu doctor`. It is never written to the database or returned by the API.

## Turning it off

Set `telegram.enabled: false` (or drop `TELEGRAM_BOT_TOKEN`) and restart. Pending
rows stay on disk; they are not sent while the switch is off, and delivery
resumes where it stopped if you turn it back on.
