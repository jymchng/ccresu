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
  generated: "2026-11-15",
  commit: "2ed3fc0",
  binary: "ccresu (built from this commit)",
} as const;

export const captures = {
  help: {
    id: "help",
    label: "The command surface",
    command: "ccresu --help",
    description: "The whole command surface in one screen: fourteen commands, the global flags and the exit-code contract.",
    output: "ccresu dev \u2014 resume Claude Code sessions after a usage limit resets\n\nUsage:\n  ccresu [--config PATH] [--json] [--log-level LEVEL] <command> [flags]\n  ccresu --help | --version\n\nCommands:\n  config    init, validate or show the configuration\n  daemon    run the watchdog loop until interrupted\n  disable   stop ccresu from recovering a session\n  doctor    check the environment ccresu runs in\n  enable    allow ccresu to recover a session\n  history   show the attempts and state transitions of a session\n  hook      consume a Claude Code Notification hook payload (always exits 0)\n  inspect   show everything known about one session (same as 'sessions status')\n  recover   resume one session now\n  retry     retry a session whose attempt budget is spent\n  serve     run the local REST/SSE API\n  sessions  list, inspect or delete sessions\n  status    summarise session, schedule and notification state\n  version   print version and build metadata\n\nGlobal flags:\n  -c, --config PATH      configuration file (default: ~/.config/ccresu/config.yaml)\n      --json             emit machine-readable JSON where the command supports it\n      --log-level LEVEL  debug, info, warn or error\n      --log-format FMT   json or text\n  -v, --verbose          shorthand for --log-level debug\n  -q, --quiet            shorthand for --log-level error\n\nExit codes: 0 ok, 1 failure, 2 usage, 3 not found, 4 not eligible, 130 interrupted.\nRun 'ccresu <command> --help' for the flags of one command.\n",
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
    output: "SESSION   STATE               NEXT ATTEMPT  ENABLED  RECOVERABLE  PROJECT                      RUNS IN  LAST SEEN                NOTE\n4d1f9c2a  RESET_TIME_REACHED  due now       yes      yes          <project>                    -        (captured-at)\n\n1 session(s) shown (--all: disabled and settled sessions included)\n",
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
    description: "The end-to-end proof, verbatim: the stub claude CLI accepts one `--bg --resume` handoff, prints the `backgrounded` block ccresu judges it on, and the call log shows that single invocation -- no print mode, no stop, no second resume.",
    output: "\n== pass 1: discover, opt in, then `ccresu recover` each session\nreconcile pass complete (3 sessions)\nreconcile pass complete (3 sessions)\n--- recover aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa\nsession:       aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa\nstate:         RESUMED\nresumed:       yes\nattempt:       1\nverification:  proven: the continuation was handed to the background supervisor\nwhen:          (captured-at)\n--- recover bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb\nccresu: recover bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb: recovery: session bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb ended its cycle in DISCOVERED: resume session bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb: claude --bg --resume failed (exit 1): Invalid API key \u00b7 Please run /login: claude: exit status 1\n--- recover cccccccc-3333-4333-8333-cccccccccccc\nccresu: recover cccccccc-3333-4333-8333-cccccccccccc: recovery: session cccccccc-3333-4333-8333-cccccccccccc ended its cycle in DISCOVERED: resume session cccccccc-3333-4333-8333-cccccccccccc: claude: the handoff printed no backgrounded block: Starting background service\u2026\nok    the handoff vector is --bg --resume <id> --name <label> <message>\nok    no call carried a print-mode flag\nok    no stop was issued\nok    recover A exits 0\nok    session A ends RESUMED\nok    recover A is proven by the supervisor's acceptance\nok    the handoff ran exactly once for A\nok    recover B is refused (exit 1)\nok    session B is rediscovered\nok    recover C is refused (exit 1)\nok    session C is rediscovered\nok    the silent handoff ran exactly once for C\nok    history records the handoff as the success\nok    history does not credit transcript growth for the handoff\nok    the audit row keeps the handoff command\nok    the audit row keeps the supervisor's state id\nok    the audit row keeps the handoff label\nok    the audit row keeps the session id\nok    the audit row does not claim growth proved the handoff\nok    the reason names the acceptance, not the growth\nok    C records the missing block verbatim\n\n== the stub claude call log\nCALL --version\nCALL agents --json --all\nCALL --version\nCALL agents --json --all\nCALL daemon status\nCALL --version\nCALL agents --json --all\nCALL agents --json --all\nCALL agents --json --all\nCALL --bg --resume aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa --name ccresu-aaaaaaaa continue the existing goal\nCALL agents --json --all\nCALL daemon status\nCALL --version\nCALL agents --json --all\nCALL agents --json --all\nCALL agents --json --all\nCALL --bg --resume bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb --name ccresu-bbbbbbbb continue the existing goal\nCALL agents --json --all\nCALL daemon status\nCALL --version\nCALL agents --json --all\nCALL agents --json --all\nCALL agents --json --all\nCALL --bg --resume cccccccc-3333-4333-8333-cccccccccccc --name ccresu-cccccccc continue the existing goal\nCALL agents --json --all\nCALL daemon status\nCALL daemon status\nCALL daemon status\n\n== pass 2: the retired commands are gone\nok    ccresu reset is an unknown command\nok    ccresu sessions clear is an unknown subcommand\n\nPASSED: the session was handed to the supervisor once, and the block it printed is the proof\n",
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
