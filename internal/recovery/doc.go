// Package recovery implements the persisted recovery state machine, the retry
// policy and the resume verifier.
//
// The package owns three things the rest of ccresu must not reimplement:
//
//   - Which state transitions are legal. storage persists a state and an audit
//     row for every change, but it deliberately accepts any known state; the
//     ordering rules live here so there is exactly one authoritative table.
//   - Whether a failure is worth retrying. Authentication failures, a missing
//     CLI, an unrecoverable session and a user-disabled session must never be
//     retried in a loop; a network hiccup must.
//   - Whether a resume actually worked. A process that started proves nothing:
//     the verifier requires the original session id to come back and the
//     transcript to have grown.
//
// Everything is driven through injected dependencies — a clock, a
// claude.SessionProvider and the SQLite store — so the whole engine is
// deterministic under test and a daemon restart resumes from persisted state
// instead of starting over.
package recovery
