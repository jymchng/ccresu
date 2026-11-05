import type { DocPage } from "./types";

export const recoveryDoc: DocPage = {
  slug: "recovery",
  title: "How recovery works",
  summary:
    "From the limit banner to a verified resume, through an explicit persisted state machine.",
  blocks: [
    {
      kind: "p",
      text: "A recovery is a chain of recorded state transitions, not a fire-and-forget command. Each transition is validated against an edge table before it is written, so an impossible step is an error rather than a corrupted history.",
    },
    {
      kind: "code",
      label: "The happy path",
      code: `DISCOVERED -> RUNNING -> USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET\n  -> RESET_TIME_REACHED -> CHECKING_SESSION -> CHECKING_LIMIT\n  -> RESUMING -> RESUMED`,
    },
    {
      kind: "h2",
      text: "Off the happy path",
    },
    {
      kind: "table",
      head: ["State", "When it is recorded"],
      rows: [
        ["RETRY_WAIT", "An attempt failed and the budget still has room, or the pre-resume check failed and no attempt was spent; the loop returns to CHECKING_SESSION after a backoff."],
        ["PERMANENTLY_FAILED", "The attempt budget is spent, or a resume attempt failed with an error classified as non-retryable."],
        ["COMPLETED", "The session finished normally before the limit, so there is nothing to resume."],
        ["USER_TERMINATED", "The user cancelled the session."],
        ["DISABLED", "Recovery was switched off with `ccresu disable`."],
        ["UNRECOVERABLE", "The session type or the observed error cannot be resumed with the documented CLI."],
      ],
    },
    {
      kind: "h2",
      text: "What \u201cresume\u201d actually runs",
    },
    {
      kind: "p",
      text: "ccresu uses five verified interfaces: discovery (`claude agents --json`), the session transcript and per-session environment directories under `~/.claude`, the resume command below, and `claude stop <session-id>` for one narrow case. Nothing else is invoked, and no new session is ever started in place of the original one.",
    },
    {
      kind: "code",
      label: "The resume command",
      code: `claude -p --resume <session-id> "<continuation message>" --output-format json`,
    },
    {
      kind: "ul",
      items: [
        "`-p` (print mode) because the process is not attached to a terminal: ccresu never sends keystrokes and never allocates a pty.",
        "`--resume <session-id>` so the original conversation continues instead of a new one starting.",
        "The continuation message defaults to a request to continue the existing active goal, and can be overridden per session.",
        "The command runs in the session's recorded working directory, so relative paths and project configuration still apply.",
      ],
    },
    {
      kind: "h2",
      text: "When a background agent still holds the session",
    },
    {
      kind: "p",
      text: "`claude -p --resume` refuses outright while the same session is attached to the interactive CLI's background agent: the CLI answers `That session is running in the background (837eaaea). Run claude attach 837eaaea to open it, or claude stop 837eaaea first to resume it here. Add --fork-session to branch off a copy instead.` Nothing about the conversation is lost there, so it must not end the session's life.",
    },
    {
      kind: "ol",
      items: [
        "The refusal is recognised after normalisation — ANSI colour codes stripped, indentation and line wrapping collapsed — so a restyled message still matches, and an unrelated failure is not mistaken for this one.",
        "`claude stop <session-id>` releases the background agent. The stop is written to the log, never to the attempt row: `ccresu history` still shows the one resume command that was run.",
        "The identical resume is issued once more — same arguments, same session id, same continuation message — and its result becomes the attempt's result. It is not a second try against the budget: the attempt keeps its number.",
      ],
    },
    {
      kind: "note",
      tone: "warning",
      title: "Stopped once, and only once",
      text: "If the stop itself fails, or the second resume refuses again (or fails any other way), the original error is returned untouched: nothing loops and the classification is unchanged. `--fork-session` is still never used — it would continue a copy of the conversation — and `claude attach` needs a terminal ccresu does not have, which is exactly why `stop` is the one remedy taken. A session stopped this way resumes normally next time, because the background agent no longer holds it.",
    },
    {
      kind: "h2",
      text: "Proving it end to end",
    },
    {
      kind: "p",
      text: "This is not only asserted against fakes. `make e2e` runs `tests/scripts/background-resume-e2e.sh`, which builds the real binary and puts a stub `claude` on `PATH`. The stub keeps a background-agent marker, refuses the first `-p --resume` with the CLI's text above, clears the marker on `claude stop <id>`, and then returns a JSON result with the same `session_id` after growing the transcript. The run asserts that `ccresu recover <id>` exits 0 and ends `RESUMED` with the verification “proven: session id preserved and transcript grew”, that the stub saw exactly resume, stop, resume with byte-identical resume arguments, and that `ccresu history` still records the single resume command.",
    },
    {
      kind: "table",
      head: ["Binary", "`ccresu recover <id>`", "Stub saw", "Final state"],
      rows: [
        ["before the fix (`00692e3`)", "exits 1, reporting the refusal", "one resume, no stop", "`PERMANENTLY_FAILED`"],
        ["with the fix", "exits 0, verification proven", "resume, stop, resume", "`RESUMED`"],
      ],
    },
    {
      kind: "p",
      text: "The failing session in the same run is also the fixture for `ccresu reset`: `PERMANENTLY_FAILED` becomes `DISABLED`, the session's stored error is cleared, the attempt row and its reason are kept, a second `reset` is refused with exit 4, and `ccresu enable` leaves the session recoverable again.",
    },
    {
      kind: "h2",
      text: "Verification",
    },
    {
      kind: "p",
      text: "A process starting proves nothing: Claude Code may exit immediately, or may create a new conversation. After the resume, the verifier waits up to `recovery.verification_timeout` for two facts:",
    },
    {
      kind: "ol",
      items: [
        "The session id still exists in discovery and is the same id that was interrupted.",
        "The transcript for that id grew: the conversation continued instead of being recreated.",
      ],
    },
    {
      kind: "note",
      tone: "info",
      title: "Why both checks",
      text: "The id proves the conversation was resumed rather than forked; the transcript growth proves the resume actually produced work. Only then is RESUMED written and the success notification sent.",
    },
    {
      kind: "h2",
      text: "Waiting for the reset",
    },
    {
      kind: "p",
      text: "The reset time is parsed out of the limit message itself. `resets 12pm (Europe/Berlin)` becomes noon in Berlin, stored as a UTC instant with the original timezone kept for display, and the wait is unaffected by the host timezone or by a DST change in between. When the message carries no reset time, ccresu polls with exponential backoff starting at `scheduler.unknown_reset_initial_interval` (5m) up to `unknown_reset_max_interval` (30m).",
    },
    {
      kind: "h2",
      text: "Restarts and duplicate prevention",
    },
    {
      kind: "ul",
      items: [
        "Sessions, schedules and attempts are persisted, so a restart or reboot resumes waiting with the original deadline.",
        "One recovery loop per session is enforced by a per-session lock, so two triggers cannot resume the same session twice.",
        "Transitions are idempotent: re-observing a state that is already recorded does not add a duplicate row.",
        "A crash mid-attempt leaves a stale attempt that the next pass reconciles before starting a new one.",
      ],
    },
    {
      kind: "h2",
      text: "Observing a recovery",
    },
    {
      kind: "code",
      label: "Follow one session",
      code: `ccresu inspect <session-id>        # state, schedule, attempts\nccresu history <session-id>        # every recorded transition\ncurl -N http://127.0.0.1:8787/api/v1/events   # live SSE feed`,
    },
  ],
};
