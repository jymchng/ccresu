// Package telegram delivers optional recovery notifications through the Bot
// API.
//
// Three layers, each usable on its own:
//
//   - Client is the HTTP client. It retries rate limits (429, honouring the
//     API's retry_after), server faults and transport failures with a capped
//     exponential backoff, validates the JSON envelope instead of assuming a 200
//     means success, and applies a timeout and the caller's context to every
//     request. The bot token is registered with a logging.Redactor, so it never
//     appears in an error or a log line even though it is part of the URL.
//   - Render builds the HTML body for each notification event. Every
//     interpolated value goes through Escape, because Telegram rejects the whole
//     message when a project path or CLI error contains a stray "<".
//   - Notifier is the queue. Notifications are persisted before they are sent,
//     deduplicated by a key derived from the event and the row that caused it,
//     and delivered by a background drain that never runs on the recovery
//     goroutine. Notify does no network I/O and signals with a non-blocking
//     send, so a Telegram outage can delay messages but can never stall or fail
//     a recovery.
//
// Delivery state lives in SQLite rather than in memory, which is what makes a
// restart safe: a notification queued before a crash is still pending
// afterwards, and the dedupe key makes a re-observed event a no-op instead of a
// second alert.
package telegram
