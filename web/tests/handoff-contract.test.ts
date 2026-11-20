import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

import { docs } from "@/lib/docs";
import type { Block, DocPage } from "@/lib/docs/types";

/**
 * The resume contract, asserted against the documentation.
 *
 * A recovery is one command — `claude --resume <session-id> "<continuation
 * message>"` — and its exit status is the verdict. This file checks that the
 * invocation the Go code builds and the invocation the documentation shows are
 * the same thing, and that no page still describes the background handoff it
 * replaced.
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

const repoRoot = findRepoRoot();
const webRoot = path.join(repoRoot, "web");

function read(relative: string): string {
  return readFileSync(path.join(repoRoot, relative), "utf8");
}

/** Every file a reader can reach: the manual, the site, and the landing page. */
function documentationCorpus(): string[] {
  const files = ["README.md"];

  const docsDir = path.join(repoRoot, "docs");
  for (const entry of readdirSync(docsDir)) {
    if (entry.endsWith(".md")) {
      files.push(path.join("docs", entry));
    }
  }

  const libDocs = path.join(webRoot, "lib", "docs");
  for (const entry of readdirSync(libDocs)) {
    if (entry.endsWith(".ts") && !entry.endsWith(".test.ts")) {
      files.push(path.join("web", "lib", "docs", entry));
    }
  }

  for (const extra of [
    path.join("web", "lib", "landing.ts"),
    path.join("web", "lib", "static-pages.ts"),
    path.join("web", "lib", "captures", "index.ts"),
  ]) {
    if (existsSync(path.join(repoRoot, extra))) {
      files.push(extra);
    }
  }

  const components = path.join(webRoot, "components");
  for (const entry of readdirSync(components)) {
    if (entry.endsWith(".tsx")) {
      files.push(path.join("web", "components", entry));
    }
  }

  return files;
}

/** Flatten a page so prose, code blocks and diagrams can be asserted on. */
function pageText(page: DocPage): string {
  const parts: string[] = [page.summary];

  const walk = (blocks: Block[]): void => {
    for (const block of blocks) {
      switch (block.kind) {
        case "h2":
        case "h3":
        case "p":
          parts.push(block.text);
          break;
        case "code":
          if (block.label) {
            parts.push(block.label);
          }
          parts.push(block.code);
          break;
        case "ul":
        case "ol":
          parts.push(...block.items);
          break;
        case "table":
          parts.push(...block.head);
          for (const row of block.rows) {
            parts.push(...row);
          }
          break;
        case "diagram":
          parts.push(block.title, ...block.lines);
          break;
        default:
          break;
      }
    }
  };

  walk(page.blocks);
  return parts.join("\n");
}

const FORBIDDEN = [
  "--bg",
  "--name ccresu",
  "--fallback-model",
  "--permission-mode",
  "claude stop",
  "claude attach",
  "claude logs",
  "backgrounded",
];

describe("resume contract", () => {
  it("builds one resume invocation and nothing else", () => {
    const source = read(path.join("internal", "claude", "resume.go"));
    expect(source).toContain("ResumeArgs");

    const flags = Array.from(
      new Set(
        (source.match(/"--[a-z][a-z-]*"/g) ?? []).map((match) =>
          match.slice(1, -1),
        ),
      ),
    ).sort();

    expect(flags).toEqual(["--resume"]);
  });

  it("documents the invocation the code builds", () => {
    const invocation = 'claude --resume <session-id> "<continuation message>"';

    const page = docs.find((candidate) => candidate.slug === "recovery");
    expect(page).toBeDefined();
    expect(pageText(page as DocPage)).toContain(invocation);

    expect(read(path.join("docs", "recovery.md"))).toContain(
      'claude --resume <session-id> "<continuation message>"',
    );
  });

  it("says the exit status is the verdict", () => {
    const page = docs.find((candidate) => candidate.slug === "recovery");
    const text = pageText(page as DocPage).toLowerCase();
    expect(text).toContain("exit status");
  });
});

describe("no page carries the retired handoff", () => {
  const corpus = documentationCorpus();

  it.each(FORBIDDEN)("does not mention %s", (marker) => {
    const offenders = corpus.filter((file) =>
      read(file).includes(marker),
    );

    expect(offenders).toEqual([]);
  });
});
