/**
 * The three non-documentation pages: About, Terms of Service, Privacy Policy.
 *
 * They live in `lib/` rather than inline in each `app/` page module for the same
 * reason the docs do: the text is the deliverable, and a test can only assert
 * that a claim is on a page if the claim is data it can read. Each page is
 * `title` + `summary` + sections of paragraphs and bullets, rendered by one
 * shared component so the typography and tap targets cannot drift apart.
 *
 * Two rules govern every sentence here:
 *
 *  - **Nothing may contradict `LICENSE`.** The repository is MIT; the terms
 *    restate that licence's warranty and liability position rather than
 *    inventing a stricter one.
 *  - **Nothing may contradict the code.** The privacy page describes what the
 *    binary actually does: a local SQLite database, an optional Telegram
 *    notification, a loopback API, and no other network egress. The one request
 *    the *site* build makes — to `api.github.com` for the newest release tag —
 *    is described as a build-time call, not as a visitor-facing one.
 *
 * Headings carry an `id` so they are linkable and so `scroll-mt` keeps the
 * sticky header off them.
 */

export type StaticSection = {
  id: string;
  heading: string;
  paragraphs?: readonly string[];
  bullets?: readonly string[];
};

export type StaticPage = {
  /** Route with no trailing slash, matching how `next/link` normalises hrefs. */
  href: string;
  slug: string;
  title: string;
  /** One-sentence answer to "what is this page", shown under the heading. */
  summary: string;
  sections: readonly StaticSection[];
};

export const aboutPage: StaticPage = {
  href: "/about",
  slug: "about",
  title: "About",
  summary:
    "ccresu is a small local tool that waits out a Claude Code usage limit and then resumes the interrupted session, in that order.",
  sections: [
    {
      id: "what-it-is",
      heading: "What ccresu is",
      paragraphs: [
        "Claude Code stops a session when you reach a usage limit. The work is not lost \u2014 the session is on disk \u2014 but nothing resumes it until you come back, read the reset time and type into the terminal yourself. ccresu closes that gap: it reads the limit message, works out when the limit actually resets, waits for that moment, and then resumes the same session with `claude -p --resume <session-id>` with the continuation message as the last argument.",
        "Resuming is only half of it. ccresu then inspects the transcript to confirm the conversation really continued, and records the attempt. If the resume was rejected \u2014 because the window had not opened yet, or because the same session was already running in a background agent \u2014 the attempt is retried on a schedule instead of being reported as a success it never had.",
        "It runs as a CLI you invoke, or as a user-level daemon that watches for interruptions in the background. Both share one SQLite database and one configuration file.",
      ],
    },
    {
      id: "what-it-does-not-do",
      heading: "What it deliberately does not do",
      bullets: [
        "It does not lift, bypass or reset a quota. It reads the reset time, waits for it, and acts afterwards.",
        "It does not simulate keystrokes or allocate a pseudo-terminal. A resume is one `claude` process with one argv element per value.",
        "It does not fork, rewrite or replay your conversation. The same session id is resumed.",
        "It does not host anything. State is a local file, the optional API binds `127.0.0.1`, and the binary ships no web UI.",
      ],
    },
    {
      id: "who-maintains-it",
      heading: "Who maintains it",
      paragraphs: [
        "ccresu is written and maintained by jymchng as an individual open-source project. There is no company behind it, no paid tier, no support contract and no service to sign up for. It is released under the MIT licence, which is the whole of the arrangement: you get the source, and you keep the judgement about whether to run it.",
        "Development happens in the open at github.com/jymchng/ccresu. Bug reports and questions belong in the issue tracker; design arguments belong in discussions. Because it is one maintainer working on it in his own time, replies are best-effort \u2014 there is no response-time commitment, and the Terms of Service say so plainly.",
      ],
    },
    {
      id: "how-it-is-built",
      heading: "How it is built",
      paragraphs: [
        "The daemon and CLI are Go 1.26 with a pure-Go SQLite driver, so the release is a single binary that needs no runtime and no compiler toolchain on the machine that runs it. Releases cover Linux, macOS and Windows on `amd64` and `arm64`, each asset accompanied by a `checksums.txt` the installer verifies before anything is unpacked.",
        "This website is a Next.js static export served from GitHub Pages. It has no server component at request time: the pages are files, and the release version shown in the install commands is resolved when the site is built, not when it is read.",
      ],
    },
    {
      id: "maturity",
      heading: "Maturity",
      paragraphs: [
        "ccresu is pre-1.0 software. The recovery behaviour, the configuration schema and the HTTP API are settled enough to rely on, but they are not frozen: a minor release may change a flag or a default, and the release notes say when it does. Read the notes for the version you are installing before you put it in a systemd unit you will forget about.",
      ],
    },
  ],
};

export const termsPage: StaticPage = {
  href: "/terms",
  slug: "terms",
  title: "Terms of Service",
  summary:
    "ccresu is free software under the MIT licence, provided as-is, with no warranty and no liability. Using it is how you accept these terms.",
  sections: [
    {
      id: "the-short-version",
      heading: "The short version",
      paragraphs: [
        "These terms are short because the licence is the contract. ccresu is open-source software you run on your own machine; nobody operates it for you, nobody charges you for it, and nobody can guarantee what it will do to your sessions. The rest of this page restates \u2014 in ordinary language and without narrowing anything the MIT licence grants you \u2014 what that means in practice.",
        "If you do not accept these terms, do not install or run ccresu. There is no account to close and nothing to cancel.",
      ],
    },
    {
      id: "licence",
      heading: "Licence",
      paragraphs: [
        "ccresu is released under the MIT License, copyright (c) 2026 jymchng. The licence text in the repository's LICENSE file is the authoritative grant: it permits use, copy, modification, merge, publication, distribution, sublicensing and sale, on the single condition that the copyright notice and permission notice travel with copies or substantial portions of the software.",
        "Nothing on this page takes away a right the MIT licence gives you. Where this page and LICENSE disagree, LICENSE wins.",
      ],
    },
    {
      id: "no-warranty",
      heading: "No warranty",
      paragraphs: [
        "The software is provided \"as is\", without warranty of any kind, express or implied, including but not limited to the warranties of merchantability, fitness for a particular purpose and noninfringement \u2014 the words are the licence's, and they apply to every part of ccresu.",
        "Concretely: no promise that a resume will be attempted at the right moment, that it will be accepted, that the resumed conversation will continue rather than come back empty, that the reset time parsed from a usage-limit message is accurate, or that a future change to Claude Code will not break detection entirely. ccresu is a convenience wrapped around the `claude` CLI; it has no privileged access to the service and cannot promise anything about it.",
      ],
    },
    {
      id: "no-liability",
      heading: "No liability",
      paragraphs: [
        "In one sentence: if running ccresu costs you something \u2014 time, quota, work or data \u2014 that cost is yours, not the maintainer's. The software is provided as-is, and it is run at your own risk.",
        "To the fullest extent permitted by law, the author and copyright holder of ccresu is not liable for any damages of any kind arising from the use of, or the inability to use, this software \u2014 whether in contract, tort or otherwise, and whether the damage was foreseeable or not. This mirrors the licence's clause that in no event shall the authors or copyright holders be liable for any claim, damages or other liability arising from, out of or in connection with the software or its use.",
        "The exclusion covers damages arising from the use of ccresu or from the inability to use it \u2014 a machine that cannot run the binary, a service that rejects the resume, a build that does not compile \u2014 and it specifically includes, without limiting it:",
      ],
      bullets: [
        "lost work, truncated responses or a conversation that does not continue as you expected;",
        "a session resumption that is missed, delayed, attempted more than once, or attempted before the limit actually reset;",
        "usage-limit overruns, unexpected consumption of your quota, or costs billed by a third party;",
        "loss of, or corruption of, data in the local SQLite database, the configuration file or anything else in your home directory;",
        "anything an unattended or autonomous resume does on your behalf, including a continuation message sent to a session you were no longer watching;",
        "damage to your machine, your repository or your working tree caused by running the binary, the installer or the generated systemd unit.",
      ],
    },
    {
      id: "your-responsibility",
      heading: "Your responsibility",
      paragraphs: [
        "You are responsible for supervising resumption. Running ccresu as a daemon means an automated process will send a continuation message to your session after a limit resets; if that is not what you want for a particular session, do not leave it enabled for that session, and read the attempt history before you assume a resume did what you asked.",
        "You are also responsible for the third-party terms you are subject to. ccresu drives Claude Code, and your use of Claude Code \u2014 including anything ccresu does on your behalf \u2014 remains governed by Anthropic's own terms and usage policy. ccresu is not affiliated with, endorsed by or supported by Anthropic, and it does not attempt to evade any limit or policy those terms impose.",
      ],
    },
    {
      id: "acceptance",
      heading: "Acceptance",
      paragraphs: [
        "There is no signature, no checkbox and no account. Using the software is the acceptance: by installing, running or distributing ccresu, you agree to these terms and to the MIT licence it is released under. You cannot withdraw the disclaimer after the fact by continuing to use the software \u2014 the exclusion of warranty and liability applies to every run, including the run in which you decided you no longer agreed with it.",
      ],
    },
    {
      id: "changes",
      heading: "Changes to these terms",
      paragraphs: [
        "The terms may be revised as the software changes; the version published in the repository at the time you obtain a release is the one that applies to that release. Because ccresu is distributed under the MIT licence, a revision here can never retroactively narrow the permissions that licence already granted you for a copy you hold.",
      ],
    },
  ],
};

export const privacyPage: StaticPage = {
  href: "/privacy",
  slug: "privacy",
  title: "Privacy Policy",
  summary:
    "ccresu keeps your data on your machine. There is no telemetry, no server-side analytics and no account; the only outbound request the tool can make is the Telegram notification you configure yourself.",
  sections: [
    {
      id: "what-ccresu-stores",
      heading: "What ccresu does with your data",
      paragraphs: [
        "ccresu runs as you, on your computer. To detect an interruption it reads the Claude Code state it needs: the usage-limit message and the session list that `claude agents --json` reports, plus the transcript of a session it is about to resume or has just resumed. It writes two things of its own: a configuration file at `~/.config/ccresu/config.yaml` and a SQLite database at `~/.local/share/ccresu/ccresu.db`.",
        "The database holds the session ids ccresu is tracking, the reset time it parsed, the attempt history and a short excerpt of the transcript used to verify that a resume continued the conversation. Both the file and its directory are created owner-only \u2014 `0600` inside `0700` \u2014 and a directory that already existed with a looser mode is tightened on start.",
        "None of this is sent anywhere by ccresu. It is not uploaded, not synced, and not backed up by the project; deleting the files is the deletion.",
      ],
    },
    {
      id: "what-leaves-your-machine",
      heading: "What leaves your machine",
      paragraphs: [
        "Telegram notifications are optional and off unless you configure them. When they are on, ccresu posts to `https://api.telegram.org` with the bot token and chat id from your configuration, and the message body contains what the notification is about: the project, the session id, the usage-limit reset time and the attempt status. That request goes to Telegram's servers and lands in your own chat, which means Telegram's privacy terms apply to it.",
        "That is the only request the binary makes. There is no telemetry, no crash reporting, no usage ping, no update check and no analytics endpoint compiled into ccresu. With notifications disabled, it makes no outbound connections at all.",
      ],
    },
    {
      id: "local-api",
      heading: "The local API",
      paragraphs: [
        "The optional HTTP API binds `127.0.0.1:8787` \u2014 loopback only, unreachable from your network unless you deliberately change `server.host`. State-changing routes require a bearer token generated from `crypto/rand` and stored owner-only at `~/.config/ccresu/api-token`. The binary ships no web UI, so there is no page in it that could be indexed or crawled.",
      ],
    },
    {
      id: "this-website",
      heading: "This website",
      paragraphs: [
        "This site is a set of static files served by GitHub Pages. There is no analytics script, no tracking pixel, no advertising identifier, no server-side session and no account. No cookie is set. Your only browser-local storage is the theme preference (light or dark) that the toggle writes to `localStorage`; it never leaves your browser and is not readable by the site's authors.",
        "GitHub, as the hosting provider, necessarily processes requests to serve those files \u2014 including IP addresses in its own logs \u2014 under GitHub's privacy statement. The project does not receive, aggregate or analyse that data, and there is nothing on the site that would give it the ability to.",
      ],
    },
    {
      id: "build-time-request",
      heading: "The build-time GitHub API request",
      paragraphs: [
        "The install commands and the installation page name a version, and the site does not hard-code it. When the site is built, the build asks `https://api.github.com/repos/jymchng/ccresu/releases/latest` for the newest release tag, strips the leading `v`, and writes the result into the generated HTML. If that request fails \u2014 offline runner, rate limit, no published release \u2014 the build falls back to a pinned version and says so in its log rather than failing.",
        "This is a build-time call made by a CI runner, not a request made by your browser. Loading a page here does not call GitHub's API, does not carry anything about you, and would produce exactly the same HTML for every visitor.",
      ],
    },
    {
      id: "your-choices",
      heading: "Your choices",
      paragraphs: [
        "There is no account and no server-side profile, so there is nothing to opt out of and nothing for the project to delete on your behalf. To remove everything the tool stored, stop the daemon and delete `~/.local/share/ccresu/ccresu.db` and `~/.config/ccresu/`; to stop notifications, remove the Telegram settings from the configuration file, which leaves the binary with no outbound requests at all.",
        "Questions about any of this belong in the repository's issue tracker, where the answer is public and can be corrected by anyone who reads it.",
      ],
    },
  ],
};

/** Every non-documentation static page, in the order the footer lists them. */
export const staticPages: readonly StaticPage[] = [aboutPage, termsPage, privacyPage];
