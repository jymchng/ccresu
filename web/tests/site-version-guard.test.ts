import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { afterAll, describe, expect, it } from "vitest";

/**
 * The deploy guard has to fail on the mistake it was written for.
 *
 * Deploy run 44883298831 built the 0.10.0 tree while `releases/latest` still
 * answered 0.9.2, and published a site advertising 0.9.2. `scripts/
 * check-site-version.mjs` is what makes that a red deploy instead of a wrong
 * page, and a guard nobody has watched fail is indistinguishable from no guard
 * at all — this repository has already been bitten once by a command that named
 * its own test and matched nothing. So every case below drives the real script
 * over a real directory tree and asserts the exit status and the sentence a
 * person would read.
 */

const script = path.resolve(
  import.meta.dirname,
  "..",
  "..",
  "scripts",
  "check-site-version.mjs",
);

const scratch = mkdtempSync(path.join(tmpdir(), "ccresu-site-version-"));

afterAll(() => {
  rmSync(scratch, { recursive: true, force: true });
});

/** Build `web/package.json` plus whatever export pages a case needs. */
function tree(name: string, version: string, pages: Record<string, string>): string {
  const root = path.join(scratch, name);
  mkdirSync(path.join(root, "web", "out"), { recursive: true });
  writeFileSync(
    path.join(root, "web", "package.json"),
    JSON.stringify({ name: "ccresu-website", version }),
  );
  for (const [page, html] of Object.entries(pages)) {
    const file = path.join(root, "web", "out", page);
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, html);
  }
  return root;
}

/** A page that advertises `release`, the way the landing page does. */
function pageAdverting(release: string, prose = ""): string {
  return [
    "<html><body>",
    prose,
    `<a href="https://github.com/jymchng/ccresu/releases/download/v${release}/`,
    `ccresu_${release}_linux_amd64.tar.gz">Linux</a>`,
    "</body></html>",
  ].join("");
}

const ADVERTISING_PAGES = [
  "index.html",
  path.join("docs", "installation", "index.html"),
];

function run(args: string[]): { status: number; stdout: string; stderr: string } {
  const result = spawnSync(process.execPath, [script, ...args], { encoding: "utf8" });
  return {
    status: result.status ?? -1,
    stdout: result.stdout,
    stderr: result.stderr,
  };
}

describe("the deploy version guard", () => {
  it("passes when every advertising page names the version this commit declares", () => {
    const root = tree("match", "0.10.0", {
      [ADVERTISING_PAGES[0]]: pageAdverting("0.10.0"),
      [ADVERTISING_PAGES[1]]: pageAdverting("0.10.0"),
    });

    const result = run(["--root", root]);

    expect(result.stderr).toBe("");
    expect(result.status).toBe(0);
    expect(result.stdout).toContain("0.10.0");
  });

  it("fails on the shape of run 44883298831: export behind the tree", () => {
    // The 0.10.0 tree built while releases/latest still answered 0.9.2.
    const root = tree("race", "0.10.0", {
      [ADVERTISING_PAGES[0]]: pageAdverting("0.9.2"),
      [ADVERTISING_PAGES[1]]: pageAdverting("0.9.2"),
    });

    const result = run(["--root", root]);

    expect(result.status).toBe(1);
    // Both versions, so a reader can tell which way round the mistake is.
    expect(result.stderr).toContain("0.9.2");
    expect(result.stderr).toContain("0.10.0");
    // And the one step that fixes it, rather than "something went wrong".
    expect(result.stderr).toContain("Push the tag");
    expect(result.stderr).toContain("CCRESU_RELEASE_TAG");
  });

  it("fails when a page advertises the declared release and a stale one", () => {
    const root = tree("mixed", "0.10.0", {
      [ADVERTISING_PAGES[0]]: `${pageAdverting("0.10.0")}${pageAdverting("0.9.2")}`,
      [ADVERTISING_PAGES[1]]: pageAdverting("0.10.0"),
    });

    const result = run(["--root", root]);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain("0.9.2");
  });

  it("counts an archive name, not a version mentioned in prose", () => {
    // The changelog on a page can name any version it likes; only a download a
    // reader is told to make counts as advertising a release.
    const root = tree("prose", "0.10.0", {
      [ADVERTISING_PAGES[0]]: pageAdverting("0.10.0", "<p>0.9.2 fixed the wait.</p>"),
      [ADVERTISING_PAGES[1]]: pageAdverting("0.10.0", "<p>since 0.9.2</p>"),
    });

    expect(run(["--root", root]).status).toBe(0);
  });

  it("fails when the export was never built, and says which page is missing", () => {
    const root = tree("unbuilt", "0.10.0", {
      [ADVERTISING_PAGES[0]]: pageAdverting("0.10.0"),
    });

    const result = run(["--root", root]);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain("is missing from the export");
    expect(result.stderr).toContain("npm run build");
  });

  it("fails when a page has no install command at all", () => {
    // A page in the list that advertises nothing means the list and the export
    // have diverged, which is worth stopping for.
    const root = tree("empty", "0.10.0", {
      [ADVERTISING_PAGES[0]]: "<html><body><p>no downloads here</p></body></html>",
      [ADVERTISING_PAGES[1]]: pageAdverting("0.10.0"),
    });

    const result = run(["--root", root]);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain("advertises no release");
  });

  it("takes the declared version from the tree when no override is given", () => {
    // The override is for a preview; the checked-in manifest is the authority.
    const root = tree("manifest", "1.2.3", {
      [ADVERTISING_PAGES[0]]: pageAdverting("1.2.3"),
      [ADVERTISING_PAGES[1]]: pageAdverting("1.2.3"),
    });

    expect(run(["--root", root]).status).toBe(0);
    expect(run(["--root", root, "--version", "9.9.9"]).status).toBe(1);
  });

  it("refuses an argument it does not understand", () => {
    const result = run(["--nope"]);

    expect(result.status).toBe(2);
    expect(result.stderr).toContain("unexpected argument");
  });
});
