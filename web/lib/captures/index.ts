/**
 * GENERATED FILE -- do not edit by hand.
 *
 * Regenerate with `make web-captures` (scripts/web-captures.sh). The script
 * builds ./cmd/ccresu, runs it against a scratch HOME and a stub `claude`,
 * and writes the output here verbatim. Scratch paths and wall-clock values
 * are scrubbed so a regeneration on another machine produces no fake diff;
 * nothing else is rewritten.
 *
 * These strings are what the documentation pages render, and
 * web/tests/captures.test.tsx fails if a rendered figure disagrees with
 * them or if these disagree with the fixtures beside it.
 */

export type Capture = {
  /** Stable id used by the documentation content to reference this figure. */
  readonly id: string;
  /** Human label shown above the figure. */
  readonly label: string;
  /** The command that produced the output, for the caption. */
  readonly command: string;
  /** One sentence saying what the reader is looking at. */
  readonly description: string;
  /** The binary's own output, verbatim. */
  readonly output: string;
};

/** Where these captures came from; regenerated with the file itself. */
export const captureSource = {
  generator: "scripts/web-captures.sh",
  generated: "2026-11-05",
  commit: "a531074",
  binary: "ccresu (built from this commit)",
} as const;

export const captures = {
  help: {
    id: "help",
    label: "The command surface",
    command: "ccresu --help",
    description: "The whole command surface in one screen: fourteen commands, the global flags and the exit-code contract.",
    output: "ccresu dev \u2014 resume Claude Code sessions after a usage limit resets\n\nUsage:\n  ccresu [--config PATH] [--json] [--log-level LEVEL] <command> [flags]\n  ccresu --help | --version\n\nCommands:\n  config    create, validate (or print) the configuration\n  daemon    run the watchdog loop until interrupted\n  disable   stop ccresu from recovering a session\n  doctor    check the environment ccresu runs in\n  enable    allow ccresu to recover a session\n  history   show the attempts and state transitions of a session\n  inspect   show everything known about one session\n  recover   resume one session now\n  reset     clear a permanently failed session (state becomes disabled)\n  retry     retry a session whose attempt budget is spent\n  serve     run the local REST/SSE API\n  sessions  list discovered sessions\n  status    summarise session, schedule and notification state\n  version   print version and build metadata\n\nGlobal flags:\n  -c, --config PATH      configuration file (default: ~/.config/ccresu/config.yaml)\n      --json             emit machine-readable JSON where the command supports it\n      --log-level LEVEL  debug, info, warn or error\n      --log-format FMT   json or text\n  -v, --verbose          shorthand for --log-level debug\n  -q, --quiet            shorthand for --log-level error\n\nExit codes: 0 ok, 1 failure, 2 usage, 3 not found, 4 not eligible, 130 interrupted.\nRun 'ccresu <command> --help' for the flags of one command.\n",
  },
  status: {
    id: "status",
    label: "Where ccresu is right now",
    command: "ccresu status",
    description: "One session, parked after the usage-limit banner was detected: a real database, reported by the real binary.",
    output: "config:        ~/.config/ccresu/config.yaml\ndatabase:      ~/.local/share/ccresu/ccresu.db\nsessions:      1 total, 1 enabled\nlisting:       ccresu sessions shows the 1 enabled; ccresu sessions --all shows all 1\npending:       1\nschedules:     1\nnext wake:     2020-01-01T04:00:00Z\ntelegram:      no\nrecovery:      yes (max attempts 10)\nnow:           (captured-at)\n\nSTATE               SESSIONS\nRESET_TIME_REACHED  1\n",
  },
  sessions: {
    id: "sessions",
    label: "Every session it knows about",
    command: "ccresu sessions --all",
    description: "Every discovered session, including the ones recovery is not allowed to touch.",
    output: "SESSION   STATE               NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT                      LAST SEEN                NOTE\n4d1f9c2a  RESET_TIME_REACHED  due now       yes      yes          <project>                    (captured-at)\n\n1 session(s) shown (--all: disabled and settled sessions included)\n",
  },
  history: {
    id: "history",
    label: "The audit trail for one session",
    command: "ccresu history <session-id>",
    description: "The audit trail: discovery, detection, the wait and the reset instant, each recorded as it happened.",
    output: "session:       4d1f9c2a-0000-4000-8000-1a2b3c4d5e6f\nstate:         RESET_TIME_REACHED\n\nNo recovery attempts recorded.\nWHEN                     TRANSITION                                 REASON\n(captured-at)              DISCOVERED -> USAGE_LIMIT_DETECTED         usage limit detected in the transcript\n(captured-at)              USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET  waiting until 2020-01-01T04:00:00Z\n(captured-at)              WAITING_FOR_RESET -> RESET_TIME_REACHED    the scheduled reset time has arrived\n",
  },
  e2e: {
    id: "e2e",
    label: "The end-to-end proof",
    command: "make e2e",
    description: "The end-to-end proof, verbatim: the stub claude CLI refuses the resume, is stopped, is resumed again, and the call log shows the two byte-identical resumes around one stop.",
    output: "\n== pass 1: discover, opt in, then `ccresu recover` each session\nreconcile pass complete (2 sessions)\nreconcile pass complete (2 sessions)\n--- recover aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa\nsession:       aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa\nstate:         RESUMED\nresumed:       yes\nattempt:       1\nverification:  proven: session id preserved and transcript grew\nwhen:          (captured-at)\n--- recover bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb\nccresu: recover bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb: recovery: session bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb ended in PERMANENTLY_FAILED: resume session bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb: claude: claude executable unavailable: claude -p --resume failed (exit 1): Invalid API key \u00b7 Please run /login: claude -p --resume bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb --output-format json Continue working on the existing active goal from where execution was interrupted by the usage limit. Review the current state, avoid repeating completed work, and continue until the goal is completed.: exit status 1\nok    recover A exits 0\nok    session A ends RESUMED\nok    recover A proves the conversation continued\nok    recover B is refused (exit 1)\nok    session B ends PERMANENTLY_FAILED\nok    the resume ran exactly twice for A\nok    one stop call cleared the background agent\nok    both resumes used byte-identical arguments\nok    the calls run resume, stop, resume\nok    history records the stop-then-resume success\nok    the audit row keeps the one resume command\nok    session id was preserved\nok    the transcript grew\n\n== the stub claude call log (A only)\nCALL\u001f-p\u001f--resume\u001faaaaaaaa-1111-4111-8111-aaaaaaaaaaaa\u001f--output-format\u001fjson\u001fContinue working on the existing active goal from where execution was interrupted by the usage limit. Review the current state, avoid repeating completed work, and continue until the goal is completed.\nCALL\u001fstop\u001faaaaaaaa-1111-4111-8111-aaaaaaaaaaaa\nCALL\u001f-p\u001f--resume\u001faaaaaaaa-1111-4111-8111-aaaaaaaaaaaa\u001f--output-format\u001fjson\u001fContinue working on the existing active goal from where execution was interrupted by the usage limit. Review the current state, avoid repeating completed work, and continue until the goal is completed.\n\n== pass 2: ccresu reset on the permanently failed session\nsession:       bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb\nstate:         DISABLED\nprevious state: PERMANENTLY_FAILED\nerror:         cleared\nattempts:      kept (use 'ccresu retry' to spend another)\nsession bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb: reset; run 'ccresu enable bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb' to let ccresu recover it again\nok    reset moves B to DISABLED\nok    reset clears the session's stored error\nok    the attempt history was kept\nok    the kept attempt still records why it failed\nok    a second reset is refused with exit 4\nok    enable after reset leaves B recoverable\nok    and enabled again\n\nPASSED: the background session was stopped, resumed and verified end to end\n",
  },
} as const satisfies Record<string, Capture>;

/** Every capture, in the order the pages introduce them. */
export const captureList: readonly Capture[] = [
  captures.help,
  captures.status,
  captures.sessions,
  captures.history,
  captures.e2e,
];

/** Look one up by id, for content that references a figure by name. */
export function getCapture(id: string): Capture | undefined {
  return captureList.find((capture) => capture.id === id);
}
