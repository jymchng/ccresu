import type { DocPage } from "./types";

export const limitationsDoc: DocPage = {
  slug: "limitations",
  title: "Known limitations",
  summary:
    "What ccresu deliberately does not do, and where it can still be defeated.",
  blocks: [
    {
      kind: "p",
      text: "This project is honest about its boundaries: it hands a limited session back to Claude Code's background supervisor, and it reports what it cannot do rather than guessing.",
    },
    {
      kind: "h2",
      text: "Scope",
    },
    {
      kind: "ul",
      items: [
        "It does not lift, reset or bypass usage limits. It waits for the reset time the message reports, and nothing more.",
        "It cannot hand off a session type the CLI cannot resume. Such a session is marked UNRECOVERABLE and left alone.",
        "A recovery is a handoff, not a run: `claude --bg --resume` gives the continuation to Claude Code's background supervisor, and `--bg` cannot be combined with `-p`/`--print`, so there is no print-mode fallback to switch to. If you want to keep typing in that conversation, run `claude attach <short-id>` yourself.",
        "It is a local tool: one machine, one user, one home directory. Session discovery reads that machine's Claude Code state.",
        "It ships no web dashboard. The interfaces are the CLI and the REST/SSE API, and this site is documentation only.",
      ],
    },
    {
      kind: "h2",
      text: "The handoff itself",
    },
    {
      kind: "ul",
      items: [
        "The CLI refuses to background a session in a directory it does not trust. ccresu reports that refusal as its own outcome instead of retrying it, because trusting the directory is your decision.",
        "The handoff needs the session's own working directory. ccresu never inherits the daemon's, so a session whose recorded directory has been deleted cannot be handed off.",
        "One handoff invocation is bounded at two minutes. That bound covers acceptance only: the work runs under the supervisor afterwards and is no longer ccresu's process, which is exactly why the two phases no longer share a timeout.",
        "If the CLI prints no `backgrounded` block, the handoff is an error rather than an unproven success: without the block ccresu cannot name the state it created, so it records nothing as recovered.",
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
        "A handoff is proven by acceptance, not by completion. The supervisor's block says the continuation is under way in a known conversation; it cannot say the model finished the original goal, and ccresu does not pretend otherwise.",
        "Transcript growth is recorded as a diagnostic only. Once print mode is gone, growth alone is not proof that the conversation continued.",
        "The supervisor's published state is what a non-handoff verification rests on. `blocked` means the agent is waiting for a human, so it is never recorded as RESUMED; an unknown published state is reported rather than read as success.",
        "A continuation may be a copy rather than the same conversation. That is an expected outcome and the recovery is reported against it, but the copy is a new session id: the original conversation did not advance, and if the CLI names no copy id, the new conversation cannot be addressed yet.",
        "If the acceptance block carries no short state id, `claude attach`, `claude logs` and `claude stop` cannot be pointed at the session. ccresu says so instead of leaving you to hunt for arguments that do not exist.",
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
