#!/usr/bin/env node
/**
 * Resolve the release the site advertises, and write it to a file the build
 * reads.
 *
 * This runs as `npm run build`'s `prebuild` step, i.e. **before** Next starts.
 * That ordering is the whole point: `fetch` from inside a page render is served
 * out of Next's build data cache, so a build that inherited a warm `.next`
 * served the *first* build's answer and published a page naming 0.9.0 while the
 * API said 0.10.0. Two consecutive warm builds made zero requests and both baked
 * the stale version. Resolving here means the number the pages print is a
 * consequence of this run, not of a cache.
 *
 * It also makes a fallback visible: the file records `source` and, when the
 * release could not be read, `reason`. The page then says what it is naming and
 * why, instead of quietly printing a remembered version.
 *
 * Usage:
 *   node scripts/resolve-release.mjs [--out PATH]
 *
 * Environment, all read by `lib/release-core.mjs`:
 *   CCRESU_RELEASE_TAG  the tag to name, set by a release-triggered deploy
 *   CCRESU_RELEASE_API  an alternate `releases/latest` endpoint
 *
 * Exit status is 0 whether or not the release could be read: a site that names
 * the previous release is a better outcome than a build that cannot run, and the
 * deploy's version guard is what refuses to publish a page the tree contradicts.
 * Only a usage error exits 2.
 */
import { mkdirSync, renameSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { fetchLatestRelease } from "../lib/release-core.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const webRoot = resolve(here, "..");

/** Where the resolved release is written, relative to `web/`. */
export const DEFAULT_OUT = join("lib", "generated", "release.json");

function usage(message) {
  process.stderr.write(`resolve-release: ${message}\n`);
  process.exit(2);
}

function parseArgs(argv) {
  let out = null;
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--out" && argv[i + 1] !== undefined) {
      out = resolve(argv[i + 1]);
      i += 1;
      continue;
    }
    if (arg === "--help" || arg === "-h") {
      process.stdout.write("usage: resolve-release.mjs [--out PATH]\n");
      process.exit(0);
    }
    usage(`unexpected argument ${JSON.stringify(arg)}`);
  }
  return { out: out ?? join(webRoot, DEFAULT_OUT) };
}

async function main() {
  const { out } = parseArgs(process.argv.slice(2));
  const resolved = await fetchLatestRelease();

  const record = {
    ...resolved,
    resolvedAt: new Date().toISOString(),
    generator: "scripts/resolve-release.mjs",
  };

  mkdirSync(dirname(out), { recursive: true });
  const tmp = `${out}.${process.pid}.tmp`;
  writeFileSync(tmp, `${JSON.stringify(record, null, 2)}\n`, "utf8");
  // Rename rather than write-in-place: a build killed mid-write must not leave a
  // half-shaped file that the loader then has to guess at.
  renameSync(tmp, out);

  const shown = record.version ?? `no release (${record.reason ?? "unknown reason"})`;
  process.stdout.write(
    `resolve-release: ${shown} — source ${record.source} — ${out}\n`,
  );
}

await main();
