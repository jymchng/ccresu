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
      code: `DISCOVERED -> RUNNING -> USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET\n  -> RESET_TIME_REACHED -> CHECKING_SESSION -> CHECKING_LIMIT\n  -> RESUMING -> WORKING -> RESUMED\n  -> DISCOVERED            (the next cycle: read the continuation again)`,
    },
    {
      kind: "h2",
      text: "Off the happy path",
    },
    {
      kind: "table",
      head: ["State", "When it is recorded"],
      rows: [
        ["WORKING", "The dispatch was acknowledged with a job id and that job has not stopped working yet. The follow stage polls `claude agents --json`; RESUMED is written only if the job names its continuation and that continuation's transcript grew."],
        ["RETRY_WAIT", "An attempt failed and the budget still has room, or the pre-resume check failed and no attempt was spent; the loop returns to CHECKING_SESSION after a backoff."],
        ["DISCOVERED", "The attempt budget is spent, a resume attempt failed, or a resume was verified and the next window is about to start. The next discovery pass reads the transcript again and schedules the next reset, so this is a queue, not a verdict."],
        ["COMPLETED", "The session finished normally before the limit, so there is nothing to resume."],
        ["USER_TERMINATED", "The user cancelled the session."],
        ["DISABLED", "Recovery was switched off with `ccresu disable`."],
        ["UNRECOVERABLE", "The session type or the observed error cannot be resumed with the documented CLI."],
      ],
    },
    {
      kind: "h2",
      text: "A resume starts the next window",
    },
    {
      kind: "p",
      text: "RESUMED is not a finished conversation, so it is not settled either. The resumed conversation keeps running under the continuation session the job reported, and that session can hit a limit of its own, so the daemon's rediscovery stage selects every RESUMED row on its next pass, records why, and moves it back to DISCOVERED. That resets the attempt budget and makes detection re-read the continuation transcript, which is how the next reset time is found without an operator doing anything. Because nothing else would ever move that row again, no migration is applied to sessions already stored as RESUMED: they are picked up by state on the first pass after an upgrade, with no schema change and no manual step. Until then `ccresu sessions` shows the next attempt as `due now`, and `ccresu recover <id>` on such a row answers with exit code 4 and returns it to DISCOVERED rather than reporting it as already done.",
    },
    {
      kind: "diagram",
      id: "state-machine",
      label: "The chain, the retry loop, the next cycle and the two ways out",
      description:
        "The solid line is the recorded chain: discovery, the detected limit, the wait, the reset instant, the session check, the limit check, the dispatch and the job that carries it. The dashed line on the right is a failed attempt with budget left, which returns to the session check after a backoff. The solid line on the left is the next cycle: a verified resume is moved back to DISCOVERED on the daemon's next pass, which resets the attempt budget and re-reads the continuation transcript for the next limit. An attempt that fails with the budget spent, or with an error classed as non-retryable, ends in DISCOVERED too; `ccresu reset <id>` is the operator edge out of a session that cannot be recovered, and it lands in DISABLED.",
    },
    {
      kind: "h2",
      text: "What \u201cresume\u201d actually runs",
    },
    {
      kind: "p",
      text: "ccresu uses five verified interfaces: discovery (`claude agents --json`), the session transcript and per-session environment directories under `~/.claude`, the resume dispatch below, and `claude stop <job-id>` for one narrow case. Nothing else is invoked, and the conversation is never copied: the job continues it, under the session id the job reports, and that id is recorded as the continuation.",
    },
    {
      kind: "code",
      label: "The resume command",
      code: `claude --resume <session-id> --bg "<continuation message>"`,
    },
    {
      kind: "ul",
      items: [
        "`--bg` hands the turn to a background job. The process returns at once and prints `backgrounded \u00b7 <job-id>`; that eight-hex name is the handle ccresu follows. No pty is allocated and no keystrokes are sent.",
        "`--resume <session-id>` so the original conversation continues instead of a new one starting.",
        "The continuation message defaults to a request to continue the existing active goal, and can be overridden per session.",
        "The command runs in the session's recorded working directory, so relative paths and project configuration still apply.",
      ],
    },
    {
      kind: "h2",
      text: "Why the message is the last argument",
    },
    {
      kind: "p",
      text: "The prompt is a positional argument: no flag introduces it. Position is the only thing that says which invocation it belongs to, and the documented form is `claude --resume <session-id> --bg <message>`, so the message follows the session it is said to and the flag that says how it runs. ccresu keeps it last and builds the vector in one place.",
    },
    {
      kind: "code",
      label: "internal/claude/cli.go",
      code: `func ResumeArgs(sessionID, message string) []string {\n\treturn []string{"--resume", sessionID, "--bg", message}\n}`,
    },
    {
      kind: "ul",
      items: [
        "`TestResumeArgsExact` asserts the joined vector, so a reordering is a failing test rather than a puzzling runtime refusal.",
        "`Resume` hands the same slice to the first dispatch and to the repeat after `claude stop`, so the second invocation cannot be a subtly different command.",
        "The attempt's `command` column, rendered by `commandPreview`, prints the same order: `claude --resume <session-id> --bg <N-byte continuation message>`, with the placeholder where the message really sits.",
        "Only a summary is stored, never the text: the column says how many bytes were sent, and the conversation stays in the transcript.",
      ],
    },
    {
      kind: "note",
      tone: "info",
      title: "Why the audit trail has to match",
      text: "`ccresu history` is an audit trail. The preview used to print the flags after the `<message>` placeholder, describing an invocation ccresu never ran \u2014 worse than no preview, because it looks authoritative. It now renders exactly the order `claude.ResumeArgs` executes.",
    },
    {
      kind: "h2",
      text: "Why the resume no longer passes `-p`",
    },
    {
      kind: "p",
      text: "A resume is a **dispatch**, not a synchronous turn. `claude --resume <session-id> --bg '<task>'` prints `backgrounded \u00b7 d48ecad8`, returns at once, and leaves the job running; that eight-hex name is what ccresu follows. The form that used to run the turn synchronously cannot dispatch a job at all, and the CLI is explicit about why:",
    },
    {
      kind: "code",
      label: "Probed against Claude Code 2.1.223",
      code: `$ claude -p --resume 044e2a0a-... --output-format json --bg "say OK"\n--bg and --print conflict: --print never starts the interactive session that\n\`claude agents\` attaches to, so the job would be unattachable. The prompt is the\npositional \u2014 drop --print: \`claude --bg '<task>'\`.\n$ echo $? \n1`,
    },
    {
      kind: "ul",
      items: [
        "The flags are mutually exclusive, so `-p` is gone \u2014 and with it `--output-format json` and the JSON result it carried.",
        "Two things a dispatched resume therefore cannot report: cost, turns and duration (`total_cost_usd`, `num_turns`, `duration_ms`, `subtype`, `is_error`), and whether the id that answered is the id that was asked for. Both stay empty rather than being invented; a zero would be a measurement and there was none.",
        "The job continues the conversation under an id of its own (`d48ecad8-ae57-413b-ba19-f9beac9062d4` for a resume of `044e2a0a-\u2026`), which is why that comparison can no longer be evaluated \u2014 and why the transcript that matters is the continuation's.",
      ],
    },
    {
      kind: "note",
      tone: "info",
      title: "Pinned, not inferred",
      text: "What replaced the JSON blob is a handle: `claude agents --all --json` reports the job, the session id it continues and its `state`, and the continuation's own transcript grows as the turn produces work. `TestResumeArgsExact` pins the exact vector, so the retired print-mode flag cannot quietly come back.",
    },
    {
      kind: "h2",
      text: "When a background agent still holds the session",
    },
    {
      kind: "p",
      text: "`claude --resume <id> --bg \"<message>\"` refuses outright while the same session is attached to the interactive CLI's background agent: the CLI answers `That session is running in the background (837eaaea). Run claude attach 837eaaea to open it, or claude stop 837eaaea first to resume it here. Add --fork-session to branch off a copy instead.` Nothing about the conversation is lost there, so it must not end the session's life.",
    },
    {
      kind: "ol",
      items: [
        "The refusal is recognised after normalisation \u2014 ANSI colour codes stripped, indentation and line wrapping collapsed \u2014 so a restyled message still matches, and an unrelated failure is not mistaken for this one.",
        "`claude stop <job-id>` releases the background agent. The job name is the eight lower-case hex name this very refusal prints (`837eaaea`), never the session UUID: `claude stop` resolves job names only and answers a UUID with `No job matching` and exit 1. A refusal that names no job is not stopped at all \u2014 guessing could stop another session's agent. The stop is written to the log, never to the attempt row: `ccresu history` still shows the one dispatch that was run.",
        "The identical dispatch is issued once more \u2014 same arguments, same session id, same continuation message \u2014 and its job id becomes the attempt's result. It is not a second try against the budget: the attempt keeps its number.",
      ],
    },
    {
      kind: "table",
      head: ["Step", "What the CLI sees", "What ccresu records"],
      rows: [
        ["1", "`claude --resume <id> --bg \"<message>\"` \u2014 refused, the session is in the background", "the attempt, with its single dispatch command as `command`"],
        ["2", "`claude stop <job-id>` \u2014 eight hex characters, never the session UUID", "a log line only: the attempt row still shows one dispatch"],
        ["3", "the same dispatch, byte-identical arguments, acknowledged with `backgrounded \u00b7 <job-id>`", "the attempt's result, once the job is followed and the continuation proven"],
      ],
    },
    {
      kind: "diagram",
      id: "stop-then-resume",
      label: "One attempt, three calls",
      description:
        "The dispatch is refused because the session is still attached to the background agent. ccresu runs `claude stop <job-id>`, then issues the identical dispatch with the same arguments, inside the same attempt. That second job id becomes the attempt's result, and RESUMED is written only after verification.",
    },
    {
      kind: "note",
      tone: "warning",
      title: "Stopped once, and only once",
      text: "If the stop itself fails, or the second dispatch refuses again (or fails any other way), the original error is returned untouched: nothing loops and the classification is unchanged. `--fork-session` is still never used \u2014 it would continue a copy of the conversation \u2014 and `claude attach` needs a terminal ccresu does not have, which is exactly why `stop` is the one remedy taken. A session stopped this way resumes normally next time, because the background agent no longer holds it.",
    },
    {
      kind: "h2",
      text: "The operator exit from DISCOVERED",
    },
    {
      kind: "p",
      text: "A session that ended `DISCOVERED` this way \u2014 or any other way \u2014 is not lost. `ccresu retry <session-id>` is how an operator says so, and it is deliberately narrow.",
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
      text: "This is not only asserted against fakes. `make e2e` runs `tests/scripts/background-resume-e2e.sh`, which builds the real binary and puts a stub `claude` on `PATH`. The stub models the CLI as it behaves now: `--resume <id> --bg <message>` prints `backgrounded \u00b7 <job-id>`, the job appears in `claude agents --all --json` reporting `working` on its first read and `done` afterwards, and the job grows a transcript of its own under a session id of its own. It refuses the retired print-mode flags outright, so an argv drift fails the run instead of quietly taking a fallback. The stop fallback is still proven, because it still happens: a session attached to a background agent is refused, ccresu stops that job by its eight-hex name \u2014 never the session UUID, which the stub answers with `No job matching` and exit 1, exactly as the real CLI does \u2014 and repeats a byte-identical dispatch. The run then asserts that `ccresu recover <id>` exits 0 and ends `RESUMED`, that `ccresu inspect` shows the job and the continuation it reported with the continuation's transcript path, that `ccresu history` records the dispatch, the continuation and the proof, and that a second limit written into the continuation's own transcript is detected and dispatched **at the continuation's id** \u2014 so the loop comes round instead of stopping after one recovery.",
    },
    {
      kind: "capture",
      id: "e2e",
    },
    {
      kind: "p",
      text: "The failing session in the same run is also the fixture for `ccresu retry`: `DISCOVERED` becomes `DISABLED`, the session's stored error is cleared, the attempt row and its reason are kept, a second `reset` is refused with exit 4, and `ccresu enable` leaves the session recoverable again.",
    },
    {
      kind: "h2",
      text: "Verification",
    },
    {
      kind: "p",
      text: "A dispatch that prints `backgrounded \u00b7 <job-id>` proves even less than an exit code did: the process is gone before the turn runs. After the dispatch, the verifier waits up to `recovery.verification_timeout` for three facts, in order:",
    },
    {
      kind: "ol",
      items: [
        "The job leaves `working`. `claude agents --all --json` is polled until the job reports another state; a job that is no longer listed counts as finished, and the last state seen is kept for the log and for `ccresu inspect`.",
        "The job names its continuation. The agent entry carries the session id the job continues; it is recorded on the session and on the attempt, with the continuation transcript's path, and it is what the next cycle resumes.",
        "That continuation's transcript grows \u2014 measured against a baseline taken when the job first reported the id, so growth means the turn produced work rather than merely starting.",
      ],
    },
    {
      kind: "note",
      tone: "info",
      title: "Why all three",
      text: "The job proves the conversation was handed to a real background agent rather than forked or replaced; the continuation id records where the conversation now lives; the transcript growth proves the turn actually produced work. Only then is RESUMED written and the success notification sent, with `continuation_confirmed` and `transcript_grew` on the attempt. The id that was resumed is not required to answer, because it no longer does.",
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
        "A `WORKING` row is left alone by the recovery engine \u2014 the job is already working on the conversation, so a second resume would duplicate the work. The follow stage polls it, and a restarted daemon picks it back up from the database.",
        "A `RESUMED` row is not left alone: the next pass rediscovers it, so the continuation transcript is read again for the next limit. That is also why no migration is applied to sessions already stored as `RESUMED` \u2014 the rediscovery selects them by state.",
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
