import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

import { docs } from "@/lib/docs";
import type { Block } from "@/lib/docs/types";
import { localApi } from "@/lib/site";

/**
 * The handoff contract, pinned so the drift cannot come back.
 *
 * The site spent a release describing `claude -p --resume` and a
 * stop-then-resume retry after the binary had stopped running either. Nothing
 * failed, because the copy was only ever checked for *presence* of something
 * handoff-shaped: a page could keep its `claude --bg --resume` mention, add
 * `--output-format json` next to it, and every gate stayed green.
 *
 * So this file asserts the three things that actually drifted, from the
 * artefacts rather than from memory:
 *
 *  1. **The vector.** The documented invocation is
 *     `claude --bg --resume <id> --name <label> <message>` — that order, with
 *     the message last. The literal here is anchored to the Go function that
 *     builds it (`claude.HandoffArgs`), so neither side can move alone.
 *  2. **The retired claims.** No doc or landing module carries `-p --resume`,
 *     `--output-format`, a stop-then-resume retry, or a *positive* print-mode
 *     claim. Negations are allowed and expected: "`--bg` cannot be combined
 *     with `-p`/`--print`" is the true statement, and deleting it would make
 *     the docs worse, not safer.
 *  3. **The scope boundary.** The site is documentation and a landing page: no
 *     dashboard, no auth, no analytics, and no client-side request to the local
 *     API. That last one is the one a "helpful" feature would break first, so
 *     the network surface is pinned to the single build-time GitHub call.
 */

const webRoot = path.resolve(import.meta.dirname, "..");
const repoRoot = path.resolve(webRoot, "..");

function read(file: string): string {
  return readFileSync(file, "utf8");
}

function relative(file: string): string {
  return path.relative(webRoot, file);
}

/**
 * Every module that publishes prose about how a recovery runs.
 *
 * `lib/captures/index.ts` is deliberately absent: it is machine output, and the
 * harness inside it says "no print-mode flag" because that is what it asserted.
 * `tests/captures.test.tsx` owns that file.
 */
const COPY_MODULES: readonly string[] = [
  ...readdirSync(path.join(webRoot, "lib", "docs"))
    .filter((name) => name.endsWith(".ts"))
    .sort()
    .map((name) => path.join(webRoot, "lib", "docs", name)),
  path.join(webRoot, "lib", "landing.ts"),
  path.join(webRoot, "lib", "static-pages.ts"),
  path.join(webRoot, "lib", "site.ts"),
  path.join(webRoot, "components", "hero.tsx"),
  path.join(webRoot, "components", "landing-page.tsx"),
  path.join(webRoot, "components", "doc-diagrams.tsx"),
];

/* ------------------------------------------------------- 1. the vector, in order */

/**
 * The role each token of the invocation plays.
 *
 * The copy spells the placeholders several ways — `<session-id>`,
 * `<first 8 hex of the id>`, `<N-byte continuation message>` — because each
 * sentence is explaining a different part of it. The *roles* are what must not
 * move, so placeholders are collapsed to one canonical token before the order
 * is compared.
 */
function canonicalise(text: string): string {
  return text
    .replace(/\\"/g, '"')
    .replace(/<([^<>\n]*)>/g, (placeholder: string, inner: string) => {
      const words = inner.toLowerCase();
      if (/message|prompt/.test(words)) return "<message>";
      if (/label/.test(words)) return "<label>";
      if (/id|uuid|session/.test(words)) return "<id>";
      if (/hex|short|name/.test(words)) return "<label>";
      return placeholder;
    });
}

const EXPECTED_ARGV = [
  "command",
  "--bg",
  "--resume",
  "id",
  "--name",
  "label",
  "message",
] as const;

/** Tokens a documented invocation may be made of, once canonicalised. */
const INVOCATION_ROLES = new Set([
  "command",
  "--bg",
  "--resume",
  "--name",
  "id",
  "label",
  "message",
  "ellipsis",
]);

function roleOf(token: string): string {
  // A message placeholder is quoted in the copy ("<continuation message>"), so
  // the quotes come off before the role is read.
  const bare = token.replace(/^"+|"+$/g, "");
  if (bare === "claude") return "command";
  if (bare === "--bg" || bare === "--resume" || bare === "--name") return bare;
  if (bare.startsWith("ccresu-")) return "label";
  if (bare === "<id>") return "id";
  if (bare === "<label>") return "label";
  if (bare === "<message>") return "message";
  if (bare === "…" || bare === "...") return "ellipsis";
  return "unexpected";
}

function rolesOf(command: string): string[] {
  return canonicalise(command)
    .trim()
    .split(/\s+/)
    .map(roleOf);
}

/**
 * Every `claude --bg …` mention in one blob of source, cut at the first token
 * that cannot belong to an invocation.
 *
 * Cutting on an unrecognised token is what keeps this honest across three very
 * different shapes of text: prose ("… with `claude --bg --resume <id>` and the
 * message"), JSX (`claude --bg --resume</code>`), and an array split for
 * layout (`["claude --bg --resume <id>", '--name …']`).
 */
function mentionsIn(text: string): string[] {
  const canonical = canonicalise(text);
  const found: string[] = [];
  let index = canonical.indexOf("claude --bg");
  while (index !== -1) {
    const tokens = ["claude"];
    let cursor = index + "claude".length;
    for (;;) {
      const match = /^\s+(\S+)/.exec(canonical.slice(cursor));
      // A mention in prose ends at the closing backtick of its code span, which
      // the whitespace split still has attached to the last token; the same is
      // true of the quote and comma that close an array element in a figure.
      const token = match?.[1].replace(/[`"',;)]+$/, "") ?? "";
      if (!match || !INVOCATION_ROLES.has(roleOf(token))) break;
      tokens.push(token);
      cursor += match[0].length;
    }
    found.push(tokens.join(" "));
    index = canonical.indexOf("claude --bg", cursor);
  }
  return found;
}

/** `observed` keeps the order of `expected`; it may stop early, never reorder. */
function isOrderedSubsequence(observed: readonly string[], expected: readonly string[]): boolean {
  let cursor = 0;
  for (const role of observed) {
    while (cursor < expected.length && expected[cursor] !== role) cursor += 1;
    if (cursor === expected.length) return false;
    cursor += 1;
  }
  return true;
}

/**
 * The command array of one diagram step, joined.
 *
 * The handoff figure breaks its command across two `<text>` runs so it fits on
 * a phone; joined back together it is the same invocation, and that is what the
 * order check needs to see.
 */
function diagramCommands(): { file: string; command: string }[] {
  const file = path.join(webRoot, "components", "doc-diagrams.tsx");
  const source = read(file);
  return [...source.matchAll(/command:\s*\[([\s\S]*?)\]/g)]
    .map((match) => ({
      file: relative(file),
      command: [...match[1].matchAll(/"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'/g)]
        .map((literal) => JSON.parse(`"${(literal[1] ?? literal[2]).replace(/"/g, '\\"')}"`) as string)
        .join(" "),
    }))
    .filter((step) => step.command.includes("claude --bg"));
}

const mentions: { file: string; command: string }[] = [
  ...COPY_MODULES.flatMap((file) =>
    mentionsIn(read(file)).map((command) => ({ file: relative(file), command })),
  ),
  ...diagramCommands(),
];

describe("the documented handoff invocation", () => {
  it("documents the exact vector `claude.HandoffArgs` builds, in that order", () => {
    // The page states the contract in one code block, so this is the one place
    // that must be the whole thing: --bg, --resume <id>, --name <label>,
    // message last. Everything else is allowed to abbreviate it, never to
    // reorder it.
    const block = docs
      .flatMap((doc) => doc.blocks)
      .find(
        (candidate): candidate is Extract<Block, { kind: "code" }> =>
          candidate.kind === "code" && candidate.label === "The handoff command",
      );

    expect(block, "the recovery page no longer documents the handoff command").toBeDefined();
    expect(rolesOf(block?.code ?? "")).toEqual([...EXPECTED_ARGV]);
  });

  it("agrees with the Go function that builds the vector", () => {
    // Without this the literal above is just a second opinion. With it, a
    // reordering in `internal/claude/handoff.go` fails a web test and a
    // reordering in the copy fails against the binary's own source.
    const source = read(path.join(repoRoot, "internal", "claude", "handoff.go"));
    const start = source.indexOf("func HandoffArgs(");
    expect(start, "claude.HandoffArgs is gone").toBeGreaterThan(-1);

    const body = source.slice(start, source.indexOf("\n}\n", start));
    const ordered = [
      '"--bg",',
      '"--resume", sessionID,',
      '"--name", HandoffLabel(sessionID),',
      "return append(args, message)",
    ];

    let previous = -1;
    for (const fragment of ordered) {
      const at = body.indexOf(fragment);
      expect(at, `HandoffArgs no longer contains ${fragment}`).toBeGreaterThan(-1);
      expect(at, `HandoffArgs reordered ${fragment}`).toBeGreaterThan(previous);
      previous = at;
    }

    // The retired vector, asserted at the source of the vector itself.
    for (const retired of ['"-p"', '"--print"', '"--output-format"']) {
      expect(body, `HandoffArgs carries ${retired} again`).not.toContain(retired);
    }
  });

  it("never reorders a mention, wherever the copy abbreviates it", () => {
    expect(mentions.length, "no documented invocation was found at all").toBeGreaterThanOrEqual(8);

    const complaints: string[] = [];
    let exact = 0;

    for (const { file, command } of mentions) {
      const roles = rolesOf(command);
      while (roles[roles.length - 1] === "ellipsis") roles.pop();

      if (roles.join(" ") === EXPECTED_ARGV.join(" ")) {
        exact += 1;
        continue;
      }

      if (roles.slice(0, 3).join(" ") !== "command --bg --resume") {
        complaints.push(`${file}: does not start "claude --bg --resume": ${command}`);
        continue;
      }
      if (!isOrderedSubsequence(roles, [...EXPECTED_ARGV])) {
        complaints.push(`${file}: reorders the invocation: ${command}`);
        continue;
      }
      // An abbreviation may drop the trailing parts. It may not carry a message
      // and still be out of order: the message is the last argument, and the
      // drift this test exists for put flags after it.
      if (roles.includes("message")) {
        complaints.push(`${file}: a mention with a message must be the full vector: ${command}`);
      }
    }

    expect(complaints).toEqual([]);
    // Abbreviations are fine; losing the full form everywhere is not.
    expect(exact, "no page states the full invocation").toBeGreaterThanOrEqual(4);
  });
});

/* ------------------------------------------------------ 2. the retired claims */

/** Where a sentence about print mode stops being a claim and starts retiring one. */
const NEGATION = /\bno\b|\bnot\b|\bnever\b|\bcannot\b|\bcan't\b|\bwithout\b|\bgone\b|\bretired\b|plays no part|\bnothing\b|fails if/i;

describe("the retired print-mode resume", () => {
  it("is not described anywhere in the copy", () => {
    const signatures: readonly { name: string; pattern: RegExp }[] = [
      { name: "`-p --resume` (the retired invocation)", pattern: /-p\s+--resume/ },
      { name: "`--output-format` (print-mode output)", pattern: /--output-format/ },
      { name: "a stop-then-resume retry", pattern: /stop-then-resume|stop then resume/i },
      { name: "a forked resume", pattern: /--resume-fork|resume --fork/ },
    ];

    const complaints: string[] = [];
    for (const file of COPY_MODULES) {
      const source = read(file);
      for (const { name, pattern } of signatures) {
        if (pattern.test(source)) complaints.push(`${relative(file)}: mentions ${name}`);
      }
    }

    expect(complaints).toEqual([]);
  });

  it("is only ever mentioned to be ruled out", () => {
    // "`--bg` cannot be combined with `-p`/`--print`, so print mode is not a
    // fallback" is the true sentence, and it has to stay. What must not come
    // back is a sentence that *offers* print mode as the mechanism.
    //
    // The check is per clause, not per sentence, because the true sentence is
    // built out of both halves: a negation in one clause ("it cannot be
    // combined with `--print`") would otherwise license a claim in the next
    // ("so recovery runs in print mode").
    const complaints: string[] = [];

    for (const file of COPY_MODULES) {
      const source = read(file);
      for (const sentence of source.split(/(?<=[.!?])\s+/)) {
        for (const clause of sentence.split(/;\s+|:\s+|,\s+|\s+\u2014\s+/)) {
          if (!/print[-\s]?mode/i.test(clause)) continue;
          if (NEGATION.test(clause)) continue;
          complaints.push(`${relative(file)}: ${clause.trim().slice(0, 160)}`);
        }
      }
    }

    expect(complaints).toEqual([]);
  });
});

/* -------------------------------------------------------- 3. the scope boundary */

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...sourceFiles(full));
    else if (/\.(ts|tsx)$/.test(entry.name)) out.push(full);
  }
  return out;
}

/** Files that could ship to a browser, and therefore could make a request. */
const SHIPPED_SOURCES: readonly string[] = [
  ...sourceFiles(path.join(webRoot, "lib")),
  ...sourceFiles(path.join(webRoot, "components")),
  ...sourceFiles(path.join(webRoot, "app")),
];

const NETWORK_PRIMITIVE = /\bfetch\b|EventSource|WebSocket|sendBeacon|XMLHttpRequest/;

function routeList(dir: string, prefix = ""): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      out.push(...routeList(path.join(dir, entry.name), `${prefix}${entry.name}/`));
    } else if (entry.name === "page.tsx") {
      out.push(prefix === "" ? "/" : `/${prefix.replace(/\/$/, "")}`);
    }
  }
  return out.sort();
}

describe("the site's scope boundary", () => {
  it("serves the landing page, the ten documentation pages and three static pages — nothing else", () => {
    // A dashboard, a login or an admin page is a route. Pinning the whole
    // inventory means adding one fails here rather than shipping quietly.
    expect(routeList(path.join(webRoot, "app"))).toEqual([
      "/",
      "/about",
      "/docs",
      "/docs/cli",
      "/docs/configuration",
      "/docs/deployment",
      "/docs/installation",
      "/docs/limitations",
      "/docs/recovery",
      "/docs/security",
      "/docs/telegram",
      "/docs/troubleshooting",
      "/docs/tutorial",
      "/privacy",
      "/terms",
    ]);

    // The three shapes a dashboard or a login would need and this site has
    // never had: a server route, middleware that could gate a page, and an API
    // handler.
    expect(existsSync(path.join(webRoot, "app", "api"))).toBe(false);
    expect(existsSync(path.join(webRoot, "middleware.ts"))).toBe(false);
  });

  it("depends on nothing that could authenticate a user or measure one", () => {
    const pkg = JSON.parse(read(REPO("web", "package.json"))) as {
      dependencies?: Record<string, string>;
      devDependencies?: Record<string, string>;
    };
    const names = [
      ...Object.keys(pkg.dependencies ?? {}),
      ...Object.keys(pkg.devDependencies ?? {}),
    ];

    const forbidden = new Set([
      "auth",
      "oauth",
      "analytics",
      "gtag",
      "gtm",
      "segment",
      "sentry",
      "posthog",
      "plausible",
      "mixpanel",
      "amplitude",
      "firebase",
      "supabase",
      "clerk",
      "okta",
      "datadog",
      "hotjar",
      "matomo",
    ]);

    const offenders = names.filter((name) =>
      name
        .toLowerCase()
        .split(/[^a-z]+/)
        .some((word) => forbidden.has(word)),
    );

    expect(offenders).toEqual([]);
  });

  it("makes no client-side request to the local API", () => {
    // The whole network surface of the site: one build-time GitHub call. A
    // component that started polling `127.0.0.1:8787` would be a dashboard, and
    // it would also be a browser request to a loopback address the reader does
    // not have — the page would show an error, not a session.
    const callers = SHIPPED_SOURCES.filter((file) => NETWORK_PRIMITIVE.test(read(file))).map(
      relative,
    );
    expect(callers).toEqual([path.join("lib", "release.ts")]);

    const release = read(path.join(webRoot, "lib", "release.ts"));
    expect(release).toContain("https://api.github.com/repos/jymchng/ccresu/releases/latest");
    for (const loopback of ["127.0.0.1", "localhost", "8787"]) {
      expect(release).not.toContain(loopback);
    }

    // Belt and braces on the address itself: whatever else changes, no shipped
    // module may name the loopback API and a network primitive in one file.
    const dual = SHIPPED_SOURCES.filter((file) => {
      const source = read(file);
      return source.includes(localApi.address) && NETWORK_PRIMITIVE.test(source);
    }).map(relative);
    expect(dual).toEqual([]);

    // The address is documentation: it is the value the copy prints.
    expect(localApi.address).toBe("127.0.0.1:8787");
  });

  it("loads no third-party script and no analytics host", () => {
    const layout = read(path.join(webRoot, "app", "layout.tsx"));
    // The one inline script is the theme bootstrap, which reads localStorage
    // before paint. An external `src` is what would be a tracker.
    expect(layout).not.toMatch(/<script[^>]*\ssrc=/);
    expect(layout).not.toContain("next/script");

    const hosts = [
      "google-analytics.com",
      "googletagmanager.com",
      "plausible.io",
      "posthog.com",
      "mixpanel.com",
      "segment.io",
      "amplitude.com",
      "hotjar.com",
      "sentry.io",
      "@vercel/analytics",
    ];

    const complaints: string[] = [];
    for (const file of SHIPPED_SOURCES) {
      const source = read(file);
      for (const host of hosts) {
        if (source.includes(host)) complaints.push(`${relative(file)}: references ${host}`);
      }
    }

    expect(complaints).toEqual([]);
  });
});

/** Resolve a path from the repository root, for the artefacts outside `web/`. */
function REPO(...segments: string[]): string {
  return path.join(repoRoot, ...segments);
}
