# REST and SSE API

ccresu ships **no web UI**. The API exists so scripts, editors and your own
tooling can inspect and drive the watchdog.

- Base URL: `http://127.0.0.1:8787/api/v1` (host and port from
  `server.host`/`server.port`).
- Loopback by default. If you bind it wider, read [security.md](security.md)
  first.
- Responses are JSON documents; errors are always `{"error": {...}}`.

## Authentication

Every **state-changing** route requires a bearer token:

```bash
TOKEN=$(cat ~/.config/ccresu/api-token)
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8787/api/v1/status
```

The token is generated on first start, written `0600` (any looser mode is
tightened), and found at `<config dir>/api-token` unless `server.token_path`
says otherwise. It is read **only** from the `Authorization` header — never from
a query parameter or a cookie — so a cross-site request cannot supply it. To
rotate it, delete the file and restart; the next start creates a new one.

Read-only routes need no token; a browser caller is still subject to the Origin
allowlist (`server.allowed_origins`).

## Routes

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| GET | `/health` | — | Liveness; no configuration access. |
| GET | `/version` | — | Version and build metadata. |
| GET | `/status` | — | Summary of sessions, schedules and notifications. |
| GET | `/sessions` | — | Discovered sessions. |
| GET | `/sessions/{id}` | — | One session. |
| GET | `/sessions/{id}/history` | — | Attempts and transitions. |
| POST | `/sessions/{id}/recover` | token | Resume now; optional `{"message": "..."}`. |
| POST | `/sessions/{id}/enable` | token | Allow recovery of this session. |
| POST | `/sessions/{id}/disable` | token | Stop recovering this session. |
| GET | `/recoveries` | — | Pending recoveries plus `max_attempts`. |
| GET | `/events` | — | Server-sent events stream. |
| GET | `/settings` | — | Persisted settings. |
| PUT | `/settings` | token | Upsert one setting (`{"key": ..., "value": ...}`). |
| GET | `/telegram/status` | — | Delivery state of the notification queue. |
| GET | `/config` | — | Effective configuration, with secrets redacted. |
| POST | `/test` | token | Send a test notification. |

A path that exists but is called with the wrong method answers `405` with a
correct `Allow` header rather than a misleading `404`.

There is no endpoint that accepts something to execute: mutations take a
session id and, at most, a short JSON body of data.

## Examples

```bash
TOKEN=$(cat ~/.config/ccresu/api-token)

curl -s localhost:8787/api/v1/health
curl -s localhost:8787/api/v1/sessions
curl -s localhost:8787/api/v1/sessions/4d1f9c2a-7e35-4b18-9a6f-2c8d0e1f3a45

curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"message":"finish the migration and stop"}' \
  localhost:8787/api/v1/sessions/4d1f9c2a-7e35-4b18-9a6f-2c8d0e1f3a45/recover

curl -s -X PUT -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"key":"telegram.enabled","value":"true"}' \
  localhost:8787/api/v1/settings
```

## Errors

```json
{
  "error": {
    "code": "unauthorized",
    "message": "this request changes state and needs an Authorization: Bearer <token> header"
  }
}
```

`code` is a stable string to branch on; `message` is for humans and may change.
An optional `details` array carries field-level messages.

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid_argument` | Bad body, missing field, message too long. |
| 401 | `unauthorized` | Missing or wrong bearer token. |
| 403 | `origin_not_allowed` | Origin not in `server.allowed_origins`. |
| 404 | `not_found` | Unknown route, session or resource. |
| 405 | `method_not_allowed` | Path exists, method does not. |
| 409 | `not_eligible` | Session is settled, disabled or unrecoverable. |
| 413 | `payload_too_large` | Body above the limit (1 MiB by default). |
| 415 | `unsupported_media_type` | Missing or wrong `Content-Type`. |
| 429 | `too_many_requests` | Rate limited. |
| 500 | `internal` | Unexpected failure; the log has the detail. |
| 503 | `unavailable` | The capability is not present in this process (e.g. recovery in `serve`-only mode). |

## Limits and headers

- **Body size** — capped at 1 MiB (`DefaultMaxBodyBytes`); anything larger is
  rejected with `413` before a handler sees it.
- **Rate limit** — a per-client token bucket, 50 requests/second by default with
  a burst allowance; exceeding it answers `429` with `too_many_requests`.
- **Secure headers** — every response carries `X-Content-Type-Options: nosniff`,
  `Cross-Origin-Resource-Policy: same-origin`,
  `Cross-Origin-Opener-Policy: same-origin` and `Cache-Control: no-store`.
- **CORS** — an allowed origin is echoed in `Access-Control-Allow-Origin` with
  `Vary: Origin`; allowed methods are `GET, POST, PUT, OPTIONS` and allowed
  headers `Authorization, Content-Type`.

## Server-sent events

`GET /events` streams `text/event-stream`. The stream opens with a `retry: 3000`
hint, then frames like:

```text
id: 42
event: transition
data: {"id":42,"session_id":"4d1f9c2a-...","from":"RESUMING","to":"RESUMED","reason":"recovery verified"}

```

Event types are `ready`, `transition`, `recovery` and `enable`. Each frame has an
id, so a reconnecting client can send `Last-Event-ID` and receive only what it
missed.

```bash
curl -N localhost:8787/api/v1/events
```
