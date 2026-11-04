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
// Resumption uses exactly one command:
//
//	claude -p --resume <session-id> "<continuation message>" --output-format json
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
