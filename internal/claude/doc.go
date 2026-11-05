// Package claude implements discovery and resumption of Claude Code sessions.
//
// Discovery relies only on interfaces verified against the installed CLI
// (Claude Code 2.1.223):
//
//	claude --version
//	claude agents --json [--all] [--cwd <path>]
//	~/.claude/projects/<slugified-cwd>/<session-id>.jsonl
//	~/.claude/session-env/<session-id>/
//
// Resumption uses exactly two commands:
//
//	claude -p --resume <session-id> --output-format json "<continuation message>"
//	claude stop <job-id>
//
// The second is issued only when the first is refused because the session is
// still attached to a background agent ("That session is running in the
// background"). Its argument is the eight lower-case hex job name that refusal
// prints -- not the session UUID, which `claude stop` answers with "No job
// matching" and exit 1. Stopping keeps the conversation and the session id, so
// the same resume follows immediately; `claude attach` would need a terminal
// ccresu does not have, and `--fork-session` would branch a new conversation.
//
// No TTY is attached, no keystroke is injected, no shell is involved and no new
// session is ever started in place of the original one: the returned session id
// must equal the requested one, otherwise the attempt fails with
// ErrSessionNotPreserved.
//
// Sessions that cannot be resumed are reported as UNRECOVERABLE together with a
// reason; the package never invents a command for them.
//
// # Example
//
//	provider, err := claude.New(claude.Options{})
//	if err != nil {
//		return err
//	}
//	sessions, err := provider.Discover(ctx, claude.DiscoverFilter{})
//	if err != nil {
//		return err
//	}
//	for _, session := range sessions {
//		if session.Unrecoverable() {
//			log.Printf("%s: %s", session.ID, session.Reason)
//			continue
//		}
//		result, err := provider.ResumeSession(ctx, session.ID, "continue the goal")
//		if err != nil {
//			return err
//		}
//		log.Printf("resumed %s (%d turns)", result.SessionID, result.NumTurns)
//	}
//
// Tests use [FakeProvider] and [FakeExecer] so no real CLI is required.
package claude
