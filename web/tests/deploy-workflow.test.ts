import { existsSync, readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

/**
 * The deploy is wired, not just written.
 *
 * Every part of the release→site fix existed once and none of it worked: the
 * `release: published` trigger was added, while the build job still said
 * `if: github.ref == 'refs/heads/main'` — which a release event (ref
 * `refs/tags/<tag>`) fails — and neither the pin nor the version guard was
 * called. The Vitest suite was green the whole time, because it covered
 * `lib/release.ts` and `scripts/check-site-version.mjs` and nothing asserted
 * that the workflow uses them. A release-triggered deploy would have been a
 * green run that built and published nothing.
 *
 * So this file reads the workflow and asserts the wiring: the trigger, the job
 * condition that lets the trigger through, the pin the build receives, and the
 * guard that runs between the build and the publish.
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
const deploy = readFileSync(
  path.join(repoRoot, ".github", "workflows", "deploy-website.yml"),
  "utf8",
);
const release = readFileSync(
  path.join(repoRoot, ".github", "workflows", "release.yml"),
  "utf8",
);

/** The lines of a top-level block: `key:` up to the next column-zero key. */
function topLevelBlock(yaml: string, key: string): string[] {
  const lines = yaml.split("\n");
  const start = lines.findIndex((line) => line.startsWith(`${key}:`));
  if (start < 0) {
    throw new Error(`no top-level \`${key}:\` block`);
  }
  const rest = lines.slice(start + 1);
  const end = rest.findIndex((line) => /^[A-Za-z_]/.test(line));
  return [lines[start], ...(end < 0 ? rest : rest.slice(0, end))];
}

/**
 * One step of a job, from its `- name:` line to the next step.
 *
 * Steps are found by name because that is what a reader of the workflow sees
 * and what this test is about; a rename should fail here rather than silently
 * making an assertion vacuous.
 */
function step(yaml: string, name: string): { index: number; block: string } {
  const lines = yaml.split("\n");
  const start = lines.findIndex((line) => line.trim() === `- name: ${name}`);
  if (start < 0) {
    throw new Error(`no step named ${JSON.stringify(name)}`);
  }
  const rest = lines.slice(start + 1);
  const end = rest.findIndex(
    (line) => /^\s*- name: /.test(line) || /^[A-Za-z_]/.test(line),
  );
  return {
    index: start,
    block: (end < 0 ? rest : rest.slice(0, end)).join("\n"),
  };
}

describe("the deploy workflow is wired to the release", () => {
  it("triggers on a published release, not only on a push to main", () => {
    const triggers = topLevelBlock(deploy, "on").join("\n");

    expect(triggers).toContain("push:");
    expect(triggers).toContain("branches: [main]");
    expect(triggers).toContain("release:");
    expect(triggers).toContain("types: [published]");
  });

  it("lets the release event through the build job's condition", () => {
    // This is the half that was missing: a release event's ref is
    // `refs/tags/<tag>`, so a main-only condition skips the only job that
    // builds and publishes anything — a green run that does nothing.
    expect(deploy).toContain(
      "if: github.ref == 'refs/heads/main' || github.event_name == 'release'",
    );
  });

  it("hands the build the tag the release fired on", () => {
    const build = step(deploy, "Build static export").block;

    expect(build).toContain("CCRESU_RELEASE_TAG:");
    expect(build).toContain("${{ github.event.release.tag_name }}");
    expect(build).toContain("run: npm run build");
  });

  it("runs the version guard after the build and before publishing", () => {
    const build = step(deploy, "Build static export");
    const guard = step(deploy, "Check the export names the version this commit declares");
    // The publish job, not a step: it is the job that pushes gh-pages.
    const publish = deploy.indexOf("name: Publish to gh-pages");

    expect(publish).toBeGreaterThan(0);
    expect(guard.block).toContain("node scripts/check-site-version.mjs");
    // The job's default working directory is web/; the script resolves web/out
    // from the repository root, so the override is load-bearing.
    expect(guard.block).toContain("working-directory: .");
    expect(guard.index).toBeGreaterThan(build.index);
    expect(publish).toBeGreaterThan(guard.index);
  });

  it("still publishes from the build it just checked", () => {
    expect(deploy).toContain("needs: build");
    // A publish must not cancel one in flight, or the branch keeps whichever
    // force-push landed last.
    expect(deploy).toContain("cancel-in-progress: false");
  });

  it("leaves the Release workflow's semantics alone", () => {
    const triggers = topLevelBlock(release, "on").join("\n");
    expect(triggers).toContain('tags: ["v*"]');

    // The release is still created from the tag with generated notes: the
    // deploy gained triggers, the release did not change meaning.
    expect(release).toContain("--verify-tag");
    expect(release).toContain("--generate-notes");
  });
});
