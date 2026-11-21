import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

/**
 * The commands the manual gives a contributor have to run something.
 *
 * `go test ./internal/recovery/ -run TestVerifier -v` printed "no tests to run"
 * and exited 0, so a contributor following `docs/development.md` was told the
 * recovery diagnostic was green while nothing had been executed. Exit status is
 * a poor witness for a command that names its own tests: it is 0 whether the
 * pattern matched one test, ten tests, or none.
 *
 * So the citations are resolved against the tree instead of trusted. A pattern
 * has to match at least one `func Test…` that some `*_test.go` file defines,
 * which is what `go test` itself matches against. Resolving cannot rot: it is
 * the difference between documenting a guard and documenting a wish.
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

/** Every top-level Go test function the repository defines. */
function goTestNames(): string[] {
  const names: string[] = [];

  const walk = (directory: string): void => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const full = path.join(directory, entry.name);
      if (entry.isDirectory()) {
        walk(full);
      } else if (entry.name.endsWith("_test.go")) {
        for (const match of readFileSync(full, "utf8").matchAll(
          /^func (Test[A-Za-z0-9_]*)\s*\(/gm,
        )) {
          names.push(match[1]);
        }
      }
    }
  };

  walk(path.join(repoRoot, "internal"));
  walk(path.join(repoRoot, "tests"));

  return names;
}

/** The Markdown a reader can reach outside the rendered site. */
function markdownCorpus(): string[] {
  const files = ["README.md"];
  for (const entry of readdirSync(path.join(repoRoot, "docs"))) {
    if (entry.endsWith(".md")) {
      files.push(path.join("docs", entry));
    }
  }
  return files;
}

/** Every `go test … -run <pattern>` the manual shows, with the file it is in. */
function runCitations(): { file: string; pattern: string }[] {
  const citations: { file: string; pattern: string }[] = [];

  for (const file of markdownCorpus()) {
    const text = readFileSync(path.join(repoRoot, file), "utf8");
    for (const match of text.matchAll(/\bgo test\b[^\n]*?\s-run\s+(\S+)/g)) {
      citations.push({ file, pattern: match[1].replace(/^["']|["']$/g, "") });
    }
  }

  return citations;
}

describe("manual test citations", () => {
  const names = goTestNames();
  const citations = runCitations();

  it("finds the go test -run commands the manual shows", () => {
    // The failure this file guards against is a command that runs nothing and
    // still exits 0, so an extractor that quietly matched nothing would be the
    // same bug wearing a different hat.
    expect(citations.length).toBeGreaterThan(0);
  });

  it.each(citations.map(({ file, pattern }) => [file, pattern]))(
    "docs cite %s with a pattern that matches a real test (%s)",
    (file, pattern) => {
      const matched = names.filter((name) => new RegExp(pattern).test(name));
      expect(
        matched,
        `${file} tells a contributor to run "go test … -run ${pattern}", which matches no test`,
      ).not.toEqual([]);
    },
  );
});
