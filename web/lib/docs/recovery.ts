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
      text: "ccresu uses one interface, and only one:",
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
