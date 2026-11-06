import type { DocPage } from "./types";

export const limitationsDoc: DocPage = {
  slug: "limitations",
  title: "Known limitations",
  summary:
    "What ccresu deliberately does not do, and where it can still be defeated.",
  blocks: [
    {
      kind: "p",
      text: "This project is honest about its boundaries: it resumes work after a documented usage limit, and it reports what it cannot do rather than guessing.",
    },
    {
      kind: "h2",
      text: "Scope",
    },
    {
      kind: "ul",
      items: [
        "It does not lift, reset or bypass usage limits. It waits for the reset time the message reports, and nothing more.",
        "It cannot resume a session type the CLI cannot resume. Such a session is marked UNRECOVERABLE and left alone.",
        "It dispatches the resume into a background job (`claude --resume <session-id> --bg`), so the work continues in that job instead of in your terminal. If you want to keep typing in that conversation, attach to it yourself afterwards.",
        "It is a local tool: one machine, one user, one home directory. Session discovery reads that machine's Claude Code state.",
        "It ships no web dashboard. The interfaces are the CLI and the REST/SSE API, and this site is documentation only.",
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
        "Verification depends on transcript growth. If Claude Code changes where or how it writes transcripts, verification fails safe: the session is not marked RESUMED.",
        "Transcript growth proves the conversation continued; it cannot prove the model finished the original goal.",
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
