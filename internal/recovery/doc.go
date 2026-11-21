// Package recovery implements the persisted recovery state machine, the retry
// policy and the measurement taken after a resume.
//
// The package owns three things the rest of ccresu must not reimplement:
//
//   - Which state transitions are legal. storage persists a state and an audit
//     row for every change, but it deliberately accepts any known state; the
//     ordering rules live here so there is exactly one authoritative table.
//   - Whether a failure is worth repeating on the current schedule, and where
//     it leaves the session. An authentication refusal, a missing CLI or a
//     session id the CLI will not accept cannot be helped by trying again in a
//     moment, so the session returns to DISCOVERED and the next pass re-reads
//     its transcript and decides afresh. A network hiccup is worth another
//     attempt, so it is parked on the backoff. A session ccresu has proved
//     unrecoverable, or one an operator has disabled, is not retried at all.
//   - How a resume is judged. The verdict is the resume's exit status: one
//     blocking `claude --resume <session-id> "<message>"` runs the whole turn,
//     so exit 0 means the conversation continued and any other status is a
//     failed attempt carrying its exit code and stderr. The transcript is
//     measured once afterwards, and that measurement is a diagnostic —
//     recorded for an operator to read, and unable either to confirm or to
//     overturn the exit status.
//
// Everything is driven through injected dependencies — a clock, a
// claude.SessionProvider and the SQLite store — so the whole engine is
// deterministic under test and a daemon restart resumes from persisted state
// instead of starting over.
package recovery
