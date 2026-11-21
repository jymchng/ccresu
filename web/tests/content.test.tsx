import { existsSync, readFileSync, readdirSync } from "node:fs";
import path from "node:path";

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import NotFound from "@/app/not-found";
import { docs, getDoc } from "@/lib/docs";
import { features, workflow } from "@/lib/landing";
import { fallbackReleaseVersion, repo, site } from "@/lib/site";
import { themeBootstrapScript, themeStorageKey } from "@/lib/theme";
import { normalizeHref } from "./helpers";

const landingText = [
  ...features.map((feature) => `${feature.title} ${feature.description} ${feature.detail}`),
  ...workflow.map((step) => `${step.title} ${step.command} ${step.description}`),
].join("\n");

function allText(): string {
  const pages = docs.map((doc) => JSON.stringify(doc.blocks)).join("\n");
  return `${pages}\n${landingText}`;
}

/**
 * Where the Go sources live, so a page that cites a test can be held to it.
 *
 * The recovery page names the guards a reader is expected to run. Asserting only
 * that the page contains some particular name pins the copy to whatever string
 * was typed when it was written: an invented test, or one that was since renamed,
 * passes that check happily. Resolving the citations against the tree cannot rot
 * — it is the difference between documenting a guard and documenting a wish.
 */
function findRepoRoot(): string {
  let dir = process.cwd();
  for (let i = 0; i < 6; i += 1) {
    if (
      existsSync(path.join(dir, "docs")) &&
      existsSync(path.join(dir, "web")) &&
      existsSync(path.join(dir, "internal"))
    ) {
      return dir;
    }
    dir = path.dirname(dir);
  }
  throw new Error(`could not locate the repository root from ${process.cwd()}`);
}

function goTestSources(directory: string): string[] {
  const sources: string[] = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      sources.push(...goTestSources(full));
    } else if (entry.name.endsWith("_test.go")) {
      sources.push(readFileSync(full, "utf8"));
    }
  }
  return sources;
}

const repoRoot = findRepoRoot();
const goTests = [
  ...goTestSources(path.join(repoRoot, "internal")),
  ...goTestSources(path.join(repoRoot, "tests")),
];

/** The body of one Go test function, or null when no test has that name. */
function goTestBody(name: string): string | null {
  for (const source of goTests) {
    const start = source.indexOf(`func ${name}(`);
    if (start < 0) continue;
    const rest = source.slice(start + 1);
    const end = rest.indexOf("\nfunc ");
    return end < 0 ? rest : rest.slice(0, end);
  }
  return null;
}

const text = allText();

describe("content integrity", () => {
  it("contains no placeholder copy", () => {
    for (const placeholder of ["lorem ipsum", "TODO", "TBD", "example.com", "coming soon"]) {
      expect(text.toLowerCase()).not.toContain(placeholder.toLowerCase());
    }
  });

  it("documents the resume that is executed, and no retired flag", () => {
    // `claude.ResumeArgs` builds `claude --resume <id> <message>`. Print mode
    // and the background handoff are both gone, so any of those strings left
    // in the copy is drift rather than documentation.
    expect(text).toContain("claude --resume <session-id>");
    expect(text).toContain("claude agents --json");
    expect(text).not.toContain("claude -p --resume");
    expect(text).not.toContain("--output-format");
    expect(text).not.toContain("--bg");
    expect(text).not.toContain("backgrounded");
    expect(text).not.toContain("claude --resume-fork");
  });

  it("describes the resume on the landing copy, not only in the docs", () => {
    // The landing surfaces are the ones a reader sees before the docs, so they
    // have to carry the same contract: the invocation, the exit status as the
    // verdict, and no claim that a longer transcript is the proof.
    expect(landingText).toContain("claude --resume");
    expect(landingText).toContain("exit status");
    expect(landingText).not.toContain("--bg");
    expect(landingText).not.toContain("backgrounded");
    expect(landingText).not.toContain("supervisor");
    expect(landingText).not.toContain("transcript grew before");
    expect(landingText).not.toMatch(/longer transcript/);
    expect(landingText).not.toContain("-p --resume");
  });

  it("says the binary ships no web UI", () => {
    const joined = `${text} ${site.description}`.toLowerCase();
    expect(joined).toMatch(/no (embedded )?web ui/);
  });

  it("documents why the continuation message is the last argument", () => {
    const doc = getDoc("recovery");
    expect(doc).toBeDefined();
    const code = (doc?.blocks ?? [])
      .flatMap((block) => (block.kind === "code" ? [block.code] : []))
      .join("\n");

    expect(JSON.stringify(doc?.blocks)).toContain(
      "Why the message is the last argument",
    );
    // The vector that is executed, in that order, message last.
    expect(code).toContain(
      'claude --resume <session-id> "<continuation message>"',
    );

    // Every guard the page names has to be a test that exists, and has to do
    // what the page says it does. Pinning one name would accept any string that
    // was typed; resolving the citations cannot.
    const cited = Array.from(
      new Set(
        JSON.stringify(doc?.blocks ?? []).match(/Test[A-Z][A-Za-z0-9]*/g) ?? [],
      ),
    );
    expect(cited.length).toBeGreaterThan(0);
    const bodies = cited.map((name) => {
      const body = goTestBody(name);
      expect(body, `${name} is cited but no Go test defines it`).not.toBeNull();
      return body ?? "";
    });
    // One compares the argument vector, and one forbids print mode beside it.
    expect(bodies.some((body) => body.includes('"--resume"'))).toBe(true);
    expect(bodies.some((body) => body.includes('"--print"'))).toBe(true);
  });

  it("narrates the outcome and what an operator can still do", () => {
    const body = JSON.stringify(getDoc("recovery")?.blocks ?? []);

    // The verdict is the exit status, the bound is the turn timeout, and the
    // next pass re-reads a session that is still stopped rather than holding a
    // separate state for it.
    expect(body).toContain("The exit status is the verdict");
    expect(body).toContain("recovery.turn_timeout");
    expect(body).toContain("still stopped when the next pass runs");
    expect(body).not.toContain("withheld");
  });

  it("keeps the site on the GitHub Pages base path", () => {
    expect(site.basePath).toBe("/ccresu");
    expect(site.url).toBe(`https://${repo.owner}.github.io/${repo.name}`);
    // The pinned fallback must itself be a version the asset naming accepts;
    // the version actually rendered comes from lib/release.ts at build time.
    expect(fallbackReleaseVersion).toMatch(/^\d+\.\d+\.\d+$/);
  });

  it("bootstraps the theme before paint and honours the stored value", () => {
    expect(themeBootstrapScript).toContain(themeStorageKey);
    expect(themeBootstrapScript).toContain("classList");
    expect(themeBootstrapScript).not.toContain("<script");
  });

  it("renders a usable 404 state", () => {
    render(<NotFound />);

    expect(
      screen.getByRole("heading", { level: 1, name: "That page does not exist" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Back to the landing page" }),
    ).toHaveAttribute("href", "/");
    expect(
      normalizeHref(
        screen.getByRole("link", { name: "Browse the documentation" }).getAttribute("href") ?? "",
      ),
    ).toBe("/docs");
  });
});
