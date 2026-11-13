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
All seven are produced today. The daemon queues `limit_detected` and
`waiting_for_reset` from one place (`internal/app/daemon.go:345-347`); the four
recovery events come from the recovery engine, which reports each milestone to an
observer that renders and queues it (`internal/app/notifications.go`); and
`session_completed` comes from the daemon's own `complete` stage
(`internal/app/daemon.go:505-533`), because a completed session never enters the
recovery engine at all.

| Event | YAML key | Subject | Queued today |
| --- | --- | --- | --- |
| Usage limit detected | `notify_limit_detected` | Usage limit detected | yes, on the first sighting of a banner |
| Waiting for the reset | `notify_waiting` (default `false`) | Waiting for the usage limit to reset | yes, when the banner named no reset |
| Recovery started | `notify_recovery_started` | Recovery started | yes, once per attempt, when the attempt row is written |
| Recovery succeeded | `notify_recovery_success` | Recovery succeeded | yes, once per proven resume |
| Attempt failed | `notify_recovery_failure` | Recovery attempt failed | yes, once per attempt that goes to `RETRY_WAIT` |
| Permanent failure | `notify_permanent_failure` | Automatic recovery stopped | yes, once per failure classified `recovery.ClassPermanent` |
| Session completed | `notify_session_completed` | Session completed | yes, once per session that settles as `COMPLETED` |

`notify_waiting` is off by default, and it is not a second copy of the detection
message: the daemon skips it whenever the reset time is known
(`internal/app/daemon.go:352-355`), so it only arrives when the banner carried no
time and ccresu has to poll for the window to clear. Turn it on to hear about
that wait as it begins.

The recovery events are rendered from the milestone the engine reports, so a
successful recovery looks like this (`internal/telegram/template.go:116-124`):

```text
Recovery succeeded
Session: 4d1f9c2a-7e35-4b18-9a6f-2c8d0e1f3a45
Attempt: 1/10
The original conversation resumed and its transcript grew.
Duration: 2m14s
```

Two things are deliberately not announced. A usage-limit refusal is a wait with a
known end rather than a failure, so it raises no `recovery_failure`: the
`limit_detected` and `waiting_for_reset` messages already described it. And
`notify_permanent_failure` is worth calling out because its name invites the
wrong reading: it belongs to a failure classified permanent
(`recovery.ClassPermanent`), and a spent attempt budget is not one — that returns
the session to `DISCOVERED`, where the daemon re-reads the transcript and plans
the next reset (`internal/recovery/manager.go:353-359`), so it is a pause in a
cycle rather than a verdict about the conversation.

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

`POST /api/v1/test` with `{"telegram": true}` does not send a message: it reports
whether Telegram is enabled and how many delivery attempts each row gets
(`internal/server/handlers.go:609-620`), which is why its own detail reads
"no message sent by this test". It proves the configuration, not the delivery.
To prove delivery, watch `pending` in the status route above while a real event
is queued: it drops as the sender drains the row. A row that stays pending is
the delivery failing, and `GET /api/v1/sessions/{id}` lists that session's
notifications with `attempts` and `last_error`
(`internal/server/dto.go:240-249`), so the reason is readable rather than
guessed at.

Both routes are documented in [api.md](api.md).

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `401 Unauthorized` in the log | The bot token is wrong or was revoked in BotFather. |
| `400 chat not found` | Wrong `chat_id`, or the bot was never added to the chat. |
| `403 bot was blocked by the user` | The chat blocked the bot; unblock it and send `/start`. |
| `429` repeatedly | Telegram is rate limiting the bot; the queue keeps the message and backs off. |
| Nothing arrives, `enabled: false` | The switch is off: `ccresu config validate` prints `telegram: no`. |
| Nothing arrives, one event missing | Check that event's switch first: `notify_waiting` is off by default, and it stays silent even when on if the banner named a reset time. `notify_recovery_failure` is deliberately not sent for a usage-limit refusal (that is a wait, reported by `notify_limit_detected`), and `notify_permanent_failure` only fires for a failure classified `recovery.ClassPermanent` — a spent attempt budget returns the session to `DISCOVERED` and announces nothing. `notify_session_completed` needs a session that has actually settled as `COMPLETED`. |

The token is a credential: it is registered with the log redactor at startup, so
it is scrubbed from every log line and error string, and it is masked in
`ccresu doctor`. It is never written to the database or returned by the API.

## Turning it off

Set `telegram.enabled: false` (or drop `TELEGRAM_BOT_TOKEN`) and restart. Pending
rows stay on disk; they are not sent while the switch is off, and delivery
resumes where it stopped if you turn it back on.
