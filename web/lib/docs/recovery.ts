import type { DocPage } from "./types";

export const recoveryDoc: DocPage = {
  slug: "recovery",
  title: "How recovery works",
  summary:
    "From the limit banner to a continued turn: what ccresu detects, what it runs, and how the result is decided.",
  blocks: [
    {
      kind: "p",
      text: "ccresu watches the sessions Claude Code is running. When a session prints a limit banner, ccresu parses the reset time out of the banner, waits until that moment has passed, and then continues the same conversation with one `claude` command. The session keeps its own transcript, its own working directory and its own name; nothing is forked, copied or restarted under a new identity.",
    },
    {
      kind: "h2",
      text: "What a recovery actually runs",
    },
    {
      kind: "p",
      text: "A pass reads discovery (`claude agents --json`) and the session transcript to decide whether a live session is blocked on a limit, then issues a single command. Nothing else is invoked, and no new session is ever started in place of the original one.",
    },
    {
      kind: "code",
      label: "The resume command",
      code: `claude --resume <session-id> "<continuation message>"`,
    },
    {
      kind: "ul",
      items: [
        "`--resume <session-id>` continues the conversation that already exists. The identifier is the one discovery reported for the blocked session.",
        "The continuation message is a positional argument and comes last. It defaults to the configured `claude.continuation_message` and can be overridden per session.",
        "The resume runs in the session's own working directory — a git worktree included — so relative paths and project configuration still apply.",
        "The process runs to the end of the turn in the foreground of the pass. ccresu does not background it, name it, or leave another process holding it.",
      ],
    },
    {
      kind: "diagram",
      id: "resume",
      label: "One resume, from the invocation to the verdict",
      description:
        "Two steps and nothing in between: ccresu runs `claude --resume` in the session's own directory, waits for the turn, and reads the exit status as the verdict. There is no acceptance block and no separate observation afterwards.",
    },
    {
      kind: "h2",
      text: "Why the message is the last argument",
    },
    {
      kind: "p",
      text: "The prompt is a positional argument: no flag introduces it. Position is the only thing that says which invocation is being made, so the message closes the vector. Anything appended after it would be read by `claude` as part of the prompt.",
    },
    {
      kind: "h2",
      text: "How the outcome is decided",
    },
    {
      kind: "p",
      text: "The exit status is the verdict. A resume that finishes with status 0 has continued the session; there is no acceptance block to parse and no state to poll afterwards.",
    },
    {
      kind: "ul",
      items: [
        "Exit status 0 marks the attempt as recovered and the session as running again.",
        "A non-zero exit status is a failed attempt. The failure is recorded with its exit code and stderr, and the session is scheduled for another attempt with backoff and jitter.",
        "`recovery.turn_timeout` (30m by default) bounds the whole turn. A resume that overruns it is killed and treated as a failed attempt.",
        "The two spend caps, `recovery.guardrails.max_cost_usd_per_session` and `recovery.guardrails.max_cost_usd_per_attempt`, still apply. A session that reaches the per-session cap is not resumed again; an attempt that would exceed the per-attempt cap is refused before it starts.",
        "A session that is still stopped when the next pass runs is simply eligible again: there is no separate state machine between the resume and the next detection.",
      ],
    },
    {
      kind: "diagram",
      id: "state-machine",
      label: "The chain, the retry loop and the two ways out",
      description:
        "Discovery leads to detection, then to the wait for the reset instant, then to one resume. A failed attempt returns to the session check while budget remains; a settled session, or one an operator disabled, leaves the chain.",
    },
    {
      kind: "h2",
      text: "Proving it end to end",
    },
    {
      kind: "p",
      text: "This is not only asserted against fakes. `make e2e` builds the binary, points it at a stub `claude` executable on `PATH`, and checks the exit status of the process ccresu started and the bytes that were written to the stub's argument log.",
    },
    {
      kind: "ul",
      items: [
        "`TestResumeArgsIsExactlyTheVerifiedOne` (`internal/claude/cli_test.go`) compares the vector against `--resume <session-id> <message>` element by element for several messages — including `-p` and one containing a newline — so a reordering, an added flag or a dropped message fails a test instead of puzzling someone at runtime.",
        "`TestNoRecoveryArgumentCarriesPrintMode` walks that same vector and fails on `-p`, `--print`, any output-format flag, or a leading bare `--`, so print mode cannot come back beside the resume.",
        "The end-to-end run keeps three stub sessions: one that exits 0 and grows the transcript, one that exits 0 without growing anything, and one the API refuses with status 1. All three are judged by the exit status alone — a refusal is recorded as `failed (exit 1)` with the stub's stderr and returns the session to discovery, where the next pass re-reads the transcript instead of reading the failure as a verdict on the conversation.",
      ],
    },
    {
      kind: "capture",
      id: "e2e",
    },
  ],
};
