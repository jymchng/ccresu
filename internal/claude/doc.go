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
//	claude --resume <session-id> --bg "<continuation message>"
//	claude stop <job-id>
//
// A resume is a background dispatch, not a print-mode call: `--bg` and `--print`
// conflict, so `-p` and `--output-format json` are not used and the dispatch
// cannot report the result, the turn count or the cost. It answers with
// `backgrounded · <job-id>` (exit 0) and the job continues the conversation under
// its own session id, which the caller reads back through [SessionProvider.BackgroundJob]
// and the transcript.
//
// The second command is issued only when the first is refused because the session
// is still attached to a background agent ("That session is running in the
// background"). Its argument is the eight lower-case hex job name that refusal
// prints -- not the session UUID, which `claude stop` answers with "No job
// matching" and exit 1. Stopping keeps the conversation and the session id, so
// the same resume follows immediately; `claude attach` would need a terminal
// ccresu does not have, and `--fork-session` would branch a new conversation.
//
// No TTY is attached, no keystroke is injected, no shell is involved and no new
// session is ever started in place of the original one: the dispatch is followed
// by polling the job it created, and a resume that cannot be shown to have
// continued the conversation is reported as unproven rather than as a success.
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
//		job, err := provider.BackgroundJob(ctx, result.JobID)
//		if err != nil {
//			return err
//		}
//		log.Printf("dispatched %s, continues as %s (state %s)", result.JobID, job.SessionID, job.State)
//	}
//
// Tests use [FakeProvider] and [FakeExecer] so no real CLI is required.
package claude
