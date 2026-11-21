// Package claude discovers Claude Code sessions and resumes a paused one.
//
// Discovery relies only on interfaces verified against the installed CLI
// (Claude Code 2.1.257 or newer -- see [MinimumVersion]):
//
//	claude --version
//	claude agents --json [--all] [--cwd <path>]
//	~/.claude/projects/<slugified-cwd>/<session-id>.jsonl
//	~/.claude/session-env/<session-id>/
//
// Resumption is one blocking command, not a handoff:
//
//	claude --resume <session-id> "<continuation message>"
//
// [ResumeArgs] builds exactly that vector and nothing else. The command runs the
// turn in the session's own working directory and returns when the turn is over,
// so the process exit status is the verdict: zero means the continuation ran,
// and any other status is the failure, carrying its code and the captured
// streams. [DefaultTurnTimeout] bounds the whole turn rather than a handshake,
// because a bound that expired while the work was still going would kill every
// resume that was doing what it was asked to do.
//
// There is no background dispatch left to describe: no background flag, no
// session label, no print mode and no guardrail flags. A label exists to name a
// supervisor's job and a print mode exists to make a run parsable; neither has
// anything to name or parse once the resume is itself the run. Continuing an
// existing session and print mode cannot be combined at all, so there is no
// second resume vector to fall back to.
//
// A refusal that is not a crash is named rather than guessed at: an untrusted
// working directory is [ErrWorkspaceNotTrusted], and a resume asked for with no
// directory at all is [ErrResumeDirectoryMissing]. Neither is retried, because
// repeating the same command cannot change either answer and the remedy is the
// operator's decision.
//
// What a finished resume proves is settled by its exit status and nothing here.
// The transcript can be measured once afterwards to say whether the conversation
// grew, and internal/recovery does exactly that, but the measurement is a
// diagnostic rather than evidence: a session can be continued without the file
// ccresu watches changing shape, and a file can grow without the original
// conversation being the one that advanced.
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
//		// ResumeSession returned after the turn finished, so the exit status
//		// is the outcome: zero is the continuation, anything else is the
//		// failure and the caller decides what to do about it.
//		if result.ExitCode != 0 {
//			log.Printf("%s: resume exited %d", session.ID, result.ExitCode)
//			continue
//		}
//		log.Printf("continued %s with %v", session.ID, result.Command)
//	}
//
// Tests use [FakeProvider] and [FakeExecer] so no real CLI is required.
package claude
