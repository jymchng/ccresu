// Package claude implements discovery of Claude Code sessions and hands a
// paused session to the background supervisor.
//
// Discovery relies only on interfaces verified against the installed CLI
// (Claude Code 2.1.257 or newer -- see [MinimumVersion]):
//
//	claude --version
//	claude agents --json [--all] [--cwd <path>]
//	~/.claude/projects/<slugified-cwd>/<session-id>.jsonl
//	~/.claude/session-env/<session-id>/
//
// Resumption is a handoff, not a run:
//
//	claude --bg --resume <session-id> --name <label> "<continuation message>"
//
// That command returns as soon as the supervisor accepts the session, and the
// CLI reports the acceptance by printing a `backgrounded · <short-id> · <name>`
// block. [ParseHandoff] reads that block, including the `note:` line the CLI adds
// when the continuation landed in a copy rather than in place, and `--name`
// makes the recovered session identifiable in agent view without consulting
// ccresu's database.
//
// Acceptance is not completion. The supervisor owns the turn, so nothing here
// waits for it, counts transcript growth or claims the work happened; what the
// session is actually doing is read later from its published state.
//
// Print mode is retired rather than optional. `--bg` and `-p`/`--print` cannot
// be combined, so [HandoffArgs] has no flag that could switch into it and there
// is no second resume vector. `claude stop <job-id>` is the one remaining
// verified command that acts on a running agent; its argument is the eight
// lower-case hex job name the CLI prints, never the session UUID, which
// `claude stop` answers with "No job matching" and exit 1.
//
// A refusal that is not a crash is named: an untrusted workspace is
// [ErrWorkspaceNotTrusted] and output without a `backgrounded` block is
// [ErrHandoffNotAcknowledged]. Neither is retried, because repeating the same
// handoff cannot change either answer.
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
//		// The supervisor accepted the work; the session is identifiable by
//		// StateID and Name from now on, and nothing waits for it to finish.
//		log.Printf("handed off %s as %s (%s)", result.SessionID, result.StateID, result.Name)
//	}
//
// Tests use [FakeProvider] and [FakeExecer] so no real CLI is required.
package claude
