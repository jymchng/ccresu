# Security

ccresu runs as you, reads the Claude Code transcripts in `~/.claude`, and drives
`claude`. This page states exactly what it can touch and what it keeps secret, so
you can decide how much you trust it on your machine.

## Threat model in one paragraph

The assets are (a) your session transcripts, (b) the Telegram bot token, (c) the
API bearer token, and (d) your shell. The untrusted inputs are transcript
content, usage-limit banners, project paths and configuration text — all of them
end up in log lines, notification bodies and terminal output. ccresu treats every
one of those as data, never as code.

## What ccresu never does

- **No shell.** A resume is `exec.Command("claude", "-p", "--resume", <id>, ...)`
  — one argv element per value. There is no `sh -c`, so a session id or
  continuation message containing `; rm -rf ~` is just an argument.
- **No TTY, no keystrokes.** No pseudo-terminal is allocated and nothing is typed
  into your terminal.
- **No arbitrary execution from the API.** No endpoint takes a command, a path or
  a script. A mutation carries a session id and, at most, a short message.
- **No network listener beyond loopback.** The API binds `127.0.0.1` by default;
  both `server.host` and `server.allowed_origins` must be changed deliberately to
  expose it.
- **No privilege escalation.** The installer refuses to run as root (override
  with `CCRESU_ALLOW_ROOT=1` at your own risk) and the systemd unit is a *user*
  unit with `NoNewPrivileges=yes`.

## Files and permissions

| Path | Mode | Why |
| --- | --- | --- |
| `~/.config/ccresu/config.yaml` | `0600` | May contain the bot token. |
| `~/.config/ccresu/` | `0700` | Keeps the config and token private. |
| `~/.config/ccresu/api-token` | `0600` | Bearer credential. |
| `~/.local/share/ccresu/ccresu.db` | `0600` | Holds transcript excerpts. |
| `~/.local/share/ccresu/` | `0700` | Owner-only. |
| `~/.local/bin/ccresu` | `0755` | Executable. |

The database is created `0600` in a `0700` directory, and the containing
directory is tightened if it already existed with a looser mode. The token file
is created `0600`, and a pre-existing file with a looser mode is tightened on
start rather than trusted.

Do it by hand if you prefer:

```bash
install -d -m 0700 ~/.config/ccresu ~/.local/share/ccresu
install -m 0600 configs/config.example.yaml ~/.config/ccresu/config.yaml
```

## The API token

- 32 bytes from `crypto/rand`, hex-encoded — 256 bits of entropy, generated on
  first start.
- Compared in constant time (`crypto/subtle.ConstantTimeCompare`), so a wrong
  guess learns nothing from timing.
- Read **only** from `Authorization: Bearer <token>`. Never from a query
  parameter or a cookie, so a cross-site request cannot smuggle it in.
- A file shorter than 16 characters is rejected rather than accepted as a weak
  token.
- Rotate by deleting the file and restarting; the next start writes a new one.

Only state-changing routes require it; read-only routes are open on loopback
(and still subject to the origin allowlist for browser callers).

```bash
rm ~/.config/ccresu/api-token
systemctl --user restart ccresu.service
cat ~/.config/ccresu/api-token     # new token
```

## Binding the API wider

Loopback is not a limitation to work around, it is the design: the token is a
convenience for local scripts, not an authentication system for the internet.
If you must expose it, put it behind a real reverse proxy with TLS and its own
authentication, and set `server.allowed_origins` to the exact origin you serve.
Remember that:

- the token travels in a plain header over plain HTTP unless you terminate TLS in
  front of it;
- the rate limiter keys on the transport address and deliberately ignores
  `X-Forwarded-For`, so behind a proxy every client shares one bucket;
- `server.enabled: false` is the only safe setting for a machine other people can
  reach.

## Browser callers and CORS

`server.allowed_origins` lists the origins that may read responses. An allowed
origin is echoed in `Access-Control-Allow-Origin` with `Vary: Origin`; an
unlisted one is answered `403 origin_not_allowed`. Allowed methods are
`GET, POST, PUT, OPTIONS` and allowed headers `Authorization, Content-Type`, so
an arbitrary browser header cannot be used to carry a credential.

Every response carries `X-Content-Type-Options: nosniff`,
`Cross-Origin-Resource-Policy: same-origin`,
`Cross-Origin-Opener-Policy: same-origin` and `Cache-Control: no-store`.

## Request limits

- **Body size** — 1 MiB maximum, enforced before a handler runs (`413`).
- **Rate** — 50 requests/second per client address with a burst of 100 (`429`
  with the wait time).
- **Continuation message** — capped at 8 KiB and validated, so an over-long
  value is refused instead of being handed to a command line.
- **Telegram text** — capped at the Bot API's 4096 characters after escaping.

## Log and error redaction

Secrets are registered with a single redactor at startup and scrubbed from every
log record:

- attribute keys containing `token`, `secret`, `password`, `passwd`,
  `authorization`, `api_key`, `apikey`, `credential`, `cookie` or `chat_id` are
  replaced wholesale with `[REDACTED]`;
- registered secret *values* are replaced wherever they appear — in messages, in
  error text, and inside a rendered struct or map, where a nested secret would
  otherwise be printed verbatim;
- secrets shorter than 6 bytes are not registered: masking them would mangle
  ordinary words without protecting anything.

Errors from the Telegram client never embed the token; the token only lives in
the request URL, which is not part of the error value. `ccresu doctor` prints the
masked form (`********abcd`), and the API returns notes instead of the token or
chat id.

## Transcripts and privacy

The database stores what ccresu needs to explain a wait: the session id, project
path, the matched limit banner, the resolved reset instant, attempt results and
short error extracts. It is not a copy of your conversation. Because those fields
are still personal, the file is `0600` and is kept on your machine — ccresu has
no telemetry and makes no network call except to the Telegram Bot API, and only
when Telegram notifications are enabled.

## Telegram specifics

- The token is a credential. Prefer `CCRESU_TELEGRAM__BOT_TOKEN` over the YAML
  file so it stays out of backups, and revoke it in BotFather if it leaks.
- Every interpolated value is HTML-escaped in a single pass, so a hostile project
  path or CLI error cannot inject markup or break a message.
- Messages contain session ids, project paths and one-line failure reasons. If
  that is too much for the chat you use, turn the individual `notify_*` events
  off — see [telegram.md](telegram.md).

## Supervising the daemon

The shipped unit runs with `NoNewPrivileges=yes`, `PrivateTmp=yes`,
`ProtectControlGroups=yes`, `ProtectKernelTunables=yes` and `RestrictSUIDSGID=yes`.
It deliberately does *not* set `ProtectHome` or `ProtectSystem=strict`: the
daemon must write `~/.claude` and your project directories, so a home-directory
sandbox would break the thing it exists to do. See [deployment.md](deployment.md).

## Reporting a problem

If you find a security issue, please do not open a public issue with a working
exploit. Contact the maintainer privately (see the repository page) and allow
time for a fix before disclosure.
