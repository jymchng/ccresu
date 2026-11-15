import type { DocPage } from "./types";

export const recoveryDoc: DocPage = {
  slug: "recovery",
  title: "How recovery works",
  summary:
    "From the limit banner to a continuation the background supervisor accepts, through an explicit persisted state machine.",
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
        ["DISCOVERED", "The attempt budget is spent, or a resume attempt failed with an error classified as non-retryable."],
        ["COMPLETED", "The session finished normally before the limit, so there is nothing to resume."],
        ["USER_TERMINATED", "The user cancelled the session."],
        ["DISABLED", "Recovery was switched off with `ccresu disable`."],
        ["UNRECOVERABLE", "The session type or the observed error cannot be handed off with the documented CLI."],
      ],
    },
    {
      kind: "diagram",
      id: "state-machine",
      label: "The chain, the retry loop and the two ways out",
      description:
        "The solid line is the recorded chain: discovery, the detected limit, the wait, the reset instant, the session check, the limit check and the resume. The dashed line is a failed attempt with budget left, which returns to the session check after a backoff. An attempt that fails with the budget spent, or with an error classed as non-retryable, ends in DISCOVERED; `ccresu retry <id>` is the only operator edge from there, and it lands in DISABLED.",
    },
    {
      kind: "h2",
      text: "What a recovery actually runs",
    },
    {
      kind: "p",
      text: "A pass reads three things and issues one command. It reads discovery (`claude agents --json`), the state the supervisor publishes for the conversation (`claude agents --json --all`), and the supervisor's own status (`claude daemon status`, once per pass); it reads the session transcript and the per-session environment directories under `~/.claude` to detect a limit; and it hands the continuation over with the command below. Nothing else is invoked, and no new session is ever started in place of the original one.",
    },
    {
      kind: "code",
      label: "The handoff command",
      code: `claude --bg --resume <session-id> --name ccresu-<first 8 hex of the id> "<continuation message>"`,
    },
    {
      kind: "ul",
      items: [
        "`--bg` hands the continuation to Claude Code's background supervisor. It cannot be combined with `-p`/`--print`, so print mode is not a fallback here: one invocation cannot carry both.",
        "`--resume <session-id>` so the original conversation continues instead of a new one starting.",
        "`--name ccresu-<first 8 hex>` is ccresu's own label, derived from the session id, so a recovered session is identifiable in agent view without consulting ccresu's database.",
        "The continuation message defaults to a request to continue the existing active goal, and can be overridden per session.",
        "The handoff runs in the session's recorded working directory \u2014 a git worktree included \u2014 never in the daemon's, so relative paths and project configuration still apply.",
      ],
    },
    {
      kind: "h2",
      text: "Why the message is the last argument",
    },
    {
      kind: "p",
      text: "The prompt is a positional argument: no flag introduces it. Position is the only thing that says which invocation it belongs to, and the documented form is `claude --bg --resume <session-id> --name <label> <prompt>`, so the message follows the session and the label it is said to. ccresu keeps it last, after every flag, and builds the vector in one place.",
    },
    {
      kind: "code",
      label: "internal/claude/handoff.go",
      code: `func HandoffArgs(sessionID, message string, guardrails ...Guardrails) []string {\n\targs := []string{\n\t\t"--bg",\n\t\t"--resume", sessionID,\n\t\t"--name", HandoffLabel(sessionID),\n\t}\n\tif len(guardrails) > 0 {\n\t\targs = append(args, guardrails[0].flags()...)\n\t}\n\treturn append(args, message)\n}`,
    },
    {
      kind: "ul",
      items: [
        "`TestHandoffArgsExact` asserts the joined vector, so a reordering is a failing test rather than a puzzling runtime refusal, and `TestHandoffArgsNeverCarriesPrintMode` fails if a print-mode flag is ever added back.",
        "The guardrail flags \u2014 `--model`, `--effort`, `--fallback-model`, `--permission-mode` \u2014 ride between the label and the message, and an unset knob adds nothing, so a configuration without them produces exactly the vector above.",
        "The attempt's `command` column, rendered by `commandPreview`, prints the same order: `claude --bg --resume <session-id> --name ccresu-<hex> <N-byte continuation message>`, with the placeholder where the message really sits.",
        "Only a summary is stored, never the text: the column says how many bytes were sent, and the conversation stays in the transcript.",
      ],
    },
    {
      kind: "note",
      tone: "info",
      title: "Why the audit trail has to match",
      text: "`ccresu history` is an audit trail. The preview used to print the flags after the `<message>` placeholder, describing an invocation ccresu never ran \u2014 worse than no preview, because it looks authoritative. It now renders exactly the order `claude.HandoffArgs` executes.",
    },
    {
      kind: "h2",
      text: "What the supervisor answers",
    },
    {
      kind: "p",
      text: "A handoff is not a run. The CLI prints one block when it has taken the session \u2014 `backgrounded \u00b7 <short-id> \u00b7 <name>` \u2014 and returns. `claude.ParseHandoff` reads that block, and it is the only place the short state id exists: it is not the session UUID, and `claude attach`, `claude logs` and `claude stop` all take it. (`claude stop` resolves job names only, and answers a UUID with `No job matching` and exit 1.)",
    },
    {
      kind: "ul",
      items: [
        "The block is the acceptance, and the acceptance is the proof. Without it the handoff is not recorded as one (`claude.ErrHandoffNotAcknowledged`), and it is not retried, because the same arguments would produce the same silence.",
        "A workspace the CLI does not trust is its own outcome (`claude.ErrWorkspaceNotTrusted`): the remedy is the operator's decision, not another attempt.",
        "If the CLI's `note:` line says it started a copy, the recovery is reported against the copy's session id and the note is kept verbatim \u2014 a copy is an expected outcome, not a failure.",
      ],
    },
    {
      kind: "table",
      head: ["What the CLI prints", "What ccresu records"],
      rows: [
        ["`backgrounded \u00b7 <short-id> \u00b7 <name>`", "the attempt's state id and label, so `claude attach <short-id>` works without a lookup"],
        ["`Starting background service\u2026`, when it had to start a supervisor first", "a note on the attempt: the first handoff on a machine starts the supervisor"],
        ["a `note:` line naming a copy", "the copy's session id, and the recovery reported against it"],
      ],
    },
    {
      kind: "diagram",
      id: "handoff",
      label: "One invocation, one answer",
      description:
        "The handoff runs once, in the session's own directory. The supervisor answers with a `backgrounded \u00b7 <short-id> \u00b7 <name>` block and the command returns; the work continues under the supervisor, outlives ccresu, and is followed with `claude attach` and `claude logs` rather than by waiting inside the attempt.",
    },
    {
      kind: "note",
      tone: "warning",
      title: "The handoff returns; the work does not",
      text: "`claude.DefaultHandoffTimeout` (two minutes) bounds the invocation, not the work: a handoff comes back as soon as the supervisor accepts it, which the CLI does in seconds. The conversation then runs under the supervisor and outlives ccresu \u2014 that is the point of handing it over. To follow it, `claude attach <short-id>`; to read what it did, `claude logs`; to stop it, `claude stop <short-id>` (the eight-hex id from the block, never the session UUID). None of the three is part of a recovery.",
    },
    {
      kind: "h2",
      text: "The operator exit from DISCOVERED",
    },
    {
      kind: "p",
      text: "A session that ended `DISCOVERED` \u2014 for any reason \u2014 is not lost. `ccresu retry <session-id>` is how an operator says so, and it is deliberately narrow.",
    },
    {
      kind: "ul",
      items: [
        "It moves `DISCOVERED` to `DISABLED` and nothing else: any other state is refused with exit code `4` and a message that names the state and the command to use instead.",
        "It clears the stored error, so the session no longer looks broken.",
        "It keeps the attempt row, its reason and the counter, so `ccresu history` still explains what happened.",
        "It never resumes anything and never refunds an attempt. `ccresu retry <id>` refills the budget, `ccresu recover <id> --force` tries once more now, and `ccresu enable <id>` makes the session recoverable again with whatever budget it has left.",
      ],
    },
    {
      kind: "h2",
      text: "Proving it end to end",
    },
    {
      kind: "p",
      text: "This is not only asserted against fakes. `make e2e` builds the real binary and puts a stub `claude` on `PATH`, so the handoff, the acceptance block it is judged on and the audit row it leaves are exercised as one run rather than three claims.",
    },
    {
      kind: "capture",
      id: "e2e",
    },
    {
      kind: "note",
      tone: "info",
      title: "What the recorded run is evidence of",
      text: "The figure above is the whole harness, verbatim. One `--bg --resume` invocation carries the session id, the `ccresu-<first-8-hex>` label and the message; the supervisor answers with the `backgrounded` block; and the call log at the end is the record that nothing else was sent \u2014 no print-mode flag, no `claude stop`, no second resume. The two refused sessions are there as well: a handoff that fails authentication and one that prints no block both end `DISCOVERED`, because a continuation that failed is not a verdict on the conversation.",
    },
    {
      kind: "h2",
      text: "Verification",
    },
    {
      kind: "p",
      text: "A handoff is judged on the CLI's own acknowledgement, because there is nothing else to measure yet: the work belongs to the supervisor from the moment the block is printed, so growth arrives on someone else's schedule and would not say which conversation it landed in. ccresu therefore records two facts:",
    },
    {
      kind: "ol",
      items: [
        "The block was printed and parsed: the attempt carries the state id and the label the supervisor gave it.",
        "The session id is unchanged \u2014 or, when the CLI says it started a copy, the copy's id is recorded and the recovery is reported against it.",
      ],
    },
    {
      kind: "p",
      text: "A continuation ccresu did not hand off is proven differently: the supervisor publishes state for the conversation, read through `claude agents --json --all`, and that published state is the evidence. A session that is `blocked`, waiting for a human, is never recorded as `RESUMED` \u2014 it is not a recovered session, it is one waiting for an answer.",
    },
    {
      kind: "note",
      tone: "info",
      title: "Why acceptance, and not growth",
      text: "The transcript is still read and growth is still recorded, but as a diagnostic. It is not proof: when the verifier sees growth it writes `the transcript grew, but growth alone is not proof that the conversation continued`. Only the supervisor's acceptance, or the state it publishes, is evidence.",
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
    {
      kind: "capture",
      id: "history",
    },
  ],
};
