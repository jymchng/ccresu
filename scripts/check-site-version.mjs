#!/usr/bin/env node
//
// Fails a deploy whose built site advertises a release the commit does not
// declare.
//
// The site is a static export, so the version the install commands name is
// decided once, when the workflow builds it, from
// `https://api.github.com/repos/jymchng/ccresu/releases/latest`. That source is
// right almost always and wrong in exactly one window: the release order is
// "merge the version bump, then tag it", so the deploy the merge triggers asks
// the API before the tag exists and is told about the *previous* release.
//
// That is not hypothetical. Deploy run 44883298831 built the tree whose
// `web/lib/site.ts` and `web/package.json` both said 0.10.0 while the API still
// answered 0.9.2, and published a gh-pages index.html that named 0.9.2 — a site
// telling every reader to install a version the code had already left behind.
// Nothing failed; it was noticed only by looking at the live page.
//
// So this step exists: the version a page advertises has to be the version the
// tree declares, or the deploy stops. Publish the tag and the deploy that the
// release triggers rebuilds with `CCRESU_RELEASE_TAG` pinned to that tag, which
// cannot race the API at all.
//
// Usage:
//   node scripts/check-site-version.mjs [--root DIR] [--version X.Y.Z]
//
// Defaults: the repository this script lives in, and the version in
// `web/package.json`. Exit status is 0 when every checked page advertises the
// declared release, 1 when one does not, and 2 for a usage error.
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const defaultRoot = resolve(here, "..");

/**
 * The exported pages that advertise a release, relative to `web/out`.
 *
 * Deliberately a short explicit list rather than a walk of the export: a page
 * earns its place here by showing the install commands, and a page that stops
 * showing them should be removed from this list on purpose.
 */
const PAGES = [join("index.html"), join("docs", "installation", "index.html")];

function fail(message) {
  process.stderr.write(`check-site-version: ${message}\n`);
  process.exit(2);
}

/**
 * Every release a page advertises, read from the archive names it shows.
 *
 * The archive name is the honest witness: `ccresu_0.9.2_linux_amd64.tar.gz` in
 * a download URL is a version a reader is told to fetch, so a page that
 * advertises the wrong one is wrong in the way that costs somebody a download.
 * A version that merely appears in prose does not count.
 */
export function advertisedVersions(html) {
  const found = new Set();
  for (const match of html.matchAll(/ccresu_([0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*)_/g)) {
    found.add(match[1]);
  }
  return [...found].sort();
}

/** The version this tree declares: what the site should be advertising. */
export function declaredVersion(root) {
  const manifest = JSON.parse(readFileSync(join(root, "web", "package.json"), "utf8"));
  const version = manifest.version;
  if (typeof version !== "string" || version.length === 0) {
    fail(`web/package.json has no version, so there is nothing to check against`);
  }
  return version;
}

/**
 * Check every advertising page against the declared version.
 *
 * Returns one report per page instead of exiting, so the caller decides what a
 * problem means. Every report says what the page advertised, because "0.9.2 vs
 * 0.10.0" tells a reader which way round the mistake is and a bare "mismatch"
 * does not.
 */
export function checkExport({ root, version }) {
  const outDir = join(root, "web", "out");
  return PAGES.map((page) => {
    const path = join(outDir, page);
    if (!existsSync(path)) {
      return { page, path, ok: false, reason: "missing", advertised: [] };
    }
    const advertised = advertisedVersions(readFileSync(path, "utf8"));
    if (advertised.length === 0) {
      return { page, path, ok: false, reason: "advertises nothing", advertised };
    }
    const ok = advertised.length === 1 && advertised[0] === version;
    return { page, path, ok, reason: ok ? "matches" : "mismatch", advertised };
  });
}

function report(problems, version, root) {
  const lines = [];
  for (const problem of problems) {
    const what =
      problem.reason === "missing"
        ? "is missing from the export"
        : problem.reason === "advertises nothing"
          ? "advertises no release (no archive name on the page)"
          : `advertises ${problem.advertised.join(", ")}`;
    lines.push(`  web/out/${problem.page} ${what}`);
  }

  const mismatch = problems.some((problem) => problem.reason === "mismatch");
  const header = mismatch
    ? `the export does not advertise the version this commit declares (${version}):`
    : `the export cannot be checked against the version this commit declares (${version}):`;

  const advice = mismatch
    ? [
        "",
        "This is the merge-to-tag window, not a broken build: the commit that bumps",
        "the version has landed, and releases/latest still answers with the previous",
        "release, so the build has nothing newer to name. Publishing that page would",
        "tell every reader to install a version the code has left behind.",
        "",
        "Push the tag. The deploy that a published release triggers rebuilds with",
        "CCRESU_RELEASE_TAG pinned to that tag, which names the release directly and",
        "never asks the API.",
      ]
    : [
        "",
        "Run `npm run build` in web/ before this check, and keep the page list in",
        "scripts/check-site-version.mjs in step with the pages that show install",
        "commands.",
      ];

  process.stderr.write(
    `check-site-version: ${header}\n${lines.join("\n")}\n${advice.join("\n")}\n`,
  );
}

function main(argv) {
  let root = defaultRoot;
  let version = null;

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--root" && argv[i + 1] !== undefined) {
      root = resolve(argv[i + 1]);
      i += 1;
      continue;
    }
    if (arg === "--version" && argv[i + 1] !== undefined) {
      version = argv[i + 1];
      i += 1;
      continue;
    }
    if (arg === "--help" || arg === "-h") {
      process.stdout.write(
        "usage: check-site-version.mjs [--root DIR] [--version X.Y.Z]\n",
      );
      process.exit(0);
    }
    fail(`unexpected argument ${JSON.stringify(arg)}`);
  }

  const declared = version ?? declaredVersion(root);
  const reports = checkExport({ root, version: declared });
  const problems = reports.filter((entry) => !entry.ok);

  if (problems.length > 0) {
    report(problems, declared, root);
    process.exit(1);
  }

  process.stdout.write(
    `check-site-version: every advertising page names ${declared} ` +
      `(${reports.map((entry) => entry.page).join(", ")})\n`,
  );
}

const invokedDirectly =
  process.argv[1] !== undefined &&
  pathToFileURL(resolve(process.argv[1])).href === import.meta.url;

if (invokedDirectly) {
  main(process.argv.slice(2));
}
