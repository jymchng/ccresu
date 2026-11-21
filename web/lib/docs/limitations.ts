import type { DocPage } from "./types";

export const limitationsDoc: DocPage = {
  slug: "limitations",
  title: "Known limitations",
  summary:
    "What ccresu deliberately does not do, and where it can still be defeated.",
  blocks: [
    {
      kind: "p",
      text: "This project is honest about its boundaries: it continues a limited session with one blocking `claude` command, and it reports what it cannot do rather than guessing.",
    },
    {
      kind: "h2",
      text: "Scope",
    },
    {
      kind: "ul",
      items: [
        "It does not lift, reset or bypass usage limits. It waits for the reset time the message reports, and nothing more.",
        "It cannot resume a session type the CLI cannot continue. Such a session is marked UNRECOVERABLE and left alone.",
        "A recovery is a blocking run: `claude --resume <session-id> \"<message>\"` runs the whole turn in the foreground of the pass, and ccresu records the attempt only when that process exits. Nothing runs in the background, so there is no second process to attach to or stop afterwards.",
        "It is a local tool: one machine, one user, one home directory. Session discovery reads that machine's Claude Code state.",
        "It ships no web dashboard. The interfaces are the CLI and the REST/SSE API, and this site is documentation only.",
      ],
    },
    {
      kind: "h2",
      text: "The resume itself",
    },
    {
      kind: "ul",
      items: [
        "The CLI refuses to resume in a directory it does not trust. ccresu reports that refusal as its own outcome instead of retrying it, because trusting the directory is your decision.",
        "The resume needs the session's own working directory. ccresu never inherits the daemon's, so a session whose recorded directory has been deleted cannot be resumed.",
        "One resume is bounded by `recovery.turn_timeout`, thirty minutes by default. The bound covers the whole turn, because the turn is what ccresu runs; a resume that overruns it is killed and recorded as a failed attempt.",
        "A resume that exits non-zero is a failed attempt, not an unproven success: the exit code and stderr are recorded and the session is scheduled again with backoff and jitter.",
      ],
    },
    {
      kind: "h2",
      text: "Detection",
    },
    {
      kind: "ul",
      items: [
        "Unknown phrasings are not detected until you add a custom pattern. The built-in rules cover the canonical banners; prose about limits is guarded against, but a very unusual wording can still be missed.",
        "When a message carries no reset time, ccresu cannot know the deadline: it polls with backoff until the limit is gone, which means the resume can happen later than the real reset.",
        "A reset time is only as good as the message: if Claude Code reports it in a timezone the parser does not recognise, the session falls back to polling.",
      ],
    },
    {
      kind: "h2",
      text: "Verification",
    },
    {
      kind: "ul",
      items: [
        "The exit status is the verdict, and it is not the completion of the work. Status 0 says the conversation continued in the same session; it cannot say the model finished the original goal, and ccresu does not pretend otherwise.",
        "Transcript growth is recorded as a diagnostic only: it is measured after a successful exit and never used to decide success, so a resume that grew nothing is still reported as recovered.",
        "A session that exits 0 and is still stopped when the next pass runs is simply eligible again: there is no separate state to reconcile between the resume and the next detection.",
        "A resume killed at the turn timeout may leave the turn half done. The transcript keeps what was written, the attempt is recorded as a failure, and the next pass resumes the same session again.",
        "Nothing is left running after a recovery. There is no background process to attach to, to read logs from, or to stop: the turn ended when the command exited.",
      ],
    },
    {
      kind: "h2",
      text: "Operational",
    },
    {
      kind: "ul",
      items: [
        "The daemon must keep running; a machine that is asleep or off at the reset time recovers on the next start, not at the exact moment.",
        "One database per user: two daemons sharing a database will serialise on SQLite writes and can delay each other.",
        "The API trusts the local user. Anything that can read your home directory can read the bearer token, so loopback binding and file permissions are the real boundary.",
        "Notifications are best-effort: a Telegram outage delays messages, and they are retried, but delivery is not guaranteed.",
      ],
    },
    {
      kind: "note",
      tone: "info",
      title: "Reporting a limitation",
      text: "If a real Claude Code message is not detected, open an issue with the observed banner (redact session ids) so it can become a built-in pattern instead of a local customisation.",
    },
  ],
};
