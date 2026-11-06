import {
  BellRing,
  CircleCheckBig,
  Cpu,
  Database,
  Radio,
  ScanSearch,
  ShieldCheck,
  TimerReset,
  type LucideIcon,
} from "lucide-react";

/**
 * The eight capabilities shown on the landing page.
 *
 * Each entry names behaviour that exists in the Go implementation, so the
 * marketing copy cannot drift away from the binary.
 */
export type Feature = {
  icon: LucideIcon;
  title: string;
  description: string;
  detail: string;
};

export const features: readonly Feature[] = [
  {
    icon: ScanSearch,
    title: "Recognises the real limit message",
    description:
      "Normalises case, wrapping, ANSI colour codes and unicode punctuation before matching, so a wrapped or restyled banner still counts.",
    detail: "Configurable patterns, per-match confidence, false-positive guards.",
  },
  {
    icon: TimerReset,
    title: "Waits for the actual reset time",
    description:
      "Parses \u201cresets 12pm (Europe/Berlin)\u201d, 24-hour clocks, relative durations, `tomorrow` and explicit dates, keeping the message's own timezone.",
    detail: "IANA timezones, DST-safe, poll backoff when no reset time is given.",
  },
  {
    icon: Cpu,
    title: "Resumes the same session",
    description:
      "Dispatches the documented `claude --resume <session-id> --bg <message>` and follows the job it creates. No forking, no new conversation, no keystroke injection.",
    detail: "Conversation history, working directory and active goal are preserved.",
  },
  {
    icon: CircleCheckBig,
    title: "Proves the resume happened",
    description:
      "A dispatch is not a recovery. ccresu waits for the job to stop working, records the continuation it reports, and requires that continuation's transcript to grow before it records RESUMED.",
    detail: "Verification timeout and health checks are configurable.",
  },
  {
    icon: Database,
    title: "Survives restarts and reboots",
    description:
      "Every session, schedule, attempt and state transition is persisted in SQLite with WAL, so a restart continues where it stopped.",
    detail: "Idempotent transitions, duplicate-recovery prevention, attempt history.",
  },
  {
    icon: BellRing,
    title: "Tells you what happened",
    description:
      "Optional Telegram notifications for limit detected, waiting, recovery started, resumed, failed and session completed.",
    detail: "HTML-escaped templates, deduplicated, queued off the recovery path.",
  },
  {
    icon: Radio,
    title: "Scriptable local API",
    description:
      "A REST API plus an SSE event stream on loopback, so a script or dashboard can follow sessions and trigger recovery.",
    detail: "Bearer token for writes, Origin allowlist, rate limits, size limits.",
  },
  {
    icon: ShieldCheck,
    title: "Safe by default",
    description:
      "Binds 127.0.0.1, ships no web UI, never drives an interactive terminal and redacts secrets from logs and errors.",
    detail: "Token file written 0600, constant-time comparison, no telemetry.",
  },
];

export type WorkflowStep = {
  title: string;
  command: string;
  description: string;
};

export const workflow: readonly WorkflowStep[] = [
  {
    title: "Watch",
    command: "ccresu daemon",
    description:
      "Discovers Claude Code sessions from `claude agents --json` and from transcripts under ~/.claude/projects, then follows their output.",
  },
  {
    title: "Detect",
    command: "ccresu inspect <session-id>",
    description:
      "Matches a usage-limit banner and records a confidence-scored event. Auth errors, network failures and user cancellations are explicitly not limits.",
  },
  {
    title: "Schedule",
    command: "ccresu history <session-id>",
    description:
      "Resolves the reset time in its own timezone, persists it, and polls with exponential backoff when the message carries no time.",
  },
  {
    title: "Resume",
    command: "ccresu recover <session-id>",
    description:
      "After the reset, checks the session and the limit, then dispatches `claude --resume <id> --bg` in the original working directory.",
  },
  {
    title: "Verify",
    command: "ccresu status",
    description:
      "Follows the job until it stops working, confirms the continuation's transcript grew, records RESUMED, and notifies you if Telegram is configured.",
  },
];
