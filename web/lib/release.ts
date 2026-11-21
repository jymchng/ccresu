import { readFileSync } from "node:fs";
import { join } from "node:path";

import { parseLatestRelease } from "./release-core.mjs";
import { fallbackReleaseVersion } from "./site";

/**
 * Where the install commands get their version from.
 *
 * The site is a static export, so there is no server at request time: the
 * version is decided **when the site is built** and baked into the HTML. That is
 * the right trade for a documentation site — a reader gets a complete page with
 * no extra request and no layout shift — but it has consequences worth stating:
 *
 *  - the resolution does not happen inside the build. `scripts/resolve-release.mjs`
 *    runs as `npm run build`'s `prebuild` step, asks GitHub once (or takes the
 *    tag a release-triggered deploy hands it), and writes
 *    `lib/generated/release.json`. This module only reads that file, so there is
 *    no `fetch` in a render for Next's build data cache to serve: before the
 *    split, a warm `.next` made the second build reuse the *first* build's
 *    answer, and a page naming 0.9.0 was published from a tree that declared
 *    0.10.0;
 *  - "as fresh as the last build" is not the same as "as fresh as the newest
 *    release". A deploy triggered by a merge builds the tree that *declares* the
 *    new version, while `releases/latest` still answers with the previous one
 *    until the tag is published. `CCRESU_RELEASE_TAG` is how the deploy that a
 *    published release triggers avoids the question entirely: it names the tag it
 *    was triggered by and never asks the API;
 *  - the build must not fail because GitHub is unreachable, rate-limiting an
 *    unauthenticated request, or serving something unexpected. Every failure path
 *    falls back to the version pinned in `lib/site.ts` and says so in the build
 *    log — and the generated file keeps the reason, so a fallback is
 *    distinguishable from a real answer instead of invisible.
 *
 * Only the tag is read; nothing from the API is trusted beyond that, and the tag
 * is re-shaped into the `x.y.z` form the asset names use.
 */

export type ReleaseSource = "tag" | "github" | "fallback";

export type ReleaseInfo = {
  /** `x.y.z`, without the leading `v`: exactly what the asset names carry. */
  version: string;
  /** The tag this version came from, or null when the fallback was used. */
  tag: string | null;
  /**
   * Where the version came from. `"tag"` means the build was handed the tag,
   * `"github"` means the API answered, `"fallback"` means neither was available
   * and the version pinned in `lib/site.ts` was used.
   */
  source: ReleaseSource;
};

/**
 * What `scripts/resolve-release.mjs` writes, and nothing more.
 *
 * `source: "unavailable"` is the interesting one: it carries the `reason` the
 * release could not be read, which is what makes the fallback legible.
 */
export type ResolvedReleaseFile = {
  source: "tag" | "github" | "unavailable";
  version: string | null;
  tag: string | null;
  endpoint: string | null;
  reason: string | null;
  resolvedAt?: string;
  generator?: string;
};

export {
  LATEST_RELEASE_API,
  latestReleaseApi,
  normalizeTag,
  parseLatestRelease,
  pinnedTag,
} from "./release-core.mjs";

function fallback(reason: string): ReleaseInfo {
  console.warn(
    `[release] falling back to the pinned version ${fallbackReleaseVersion}: ${reason}`,
  );
  return { version: fallbackReleaseVersion, tag: null, source: "fallback" };
}

/**
 * Turn a generated record into the version the pages print.
 *
 * Separated from the file read so every shape can be tested without touching the
 * filesystem: a handed tag, a good API answer, an API answer without a usable
 * tag, a recorded failure, and a record that is not one of ours at all.
 *
 * A record is only believed when it is self-consistent — `version` must be the
 * tag with the `v` stripped and nothing else — so a hand-edited or truncated
 * file degrades to the fallback instead of putting an arbitrary string into a
 * download URL.
 */
export function releaseFromFile(record: unknown): ReleaseInfo {
  if (typeof record !== "object" || record === null) {
    return fallback("no generated release file (run `npm run resolve-release`)");
  }

  const { source, version, tag, reason } = record as Partial<ResolvedReleaseFile>;

  if (source === "unavailable") {
    return fallback(reason ?? "the newest release could not be read");
  }

  if (source !== "tag" && source !== "github") {
    return fallback(`the generated release file has an unknown source ${JSON.stringify(source)}`);
  }

  if (typeof tag !== "string" || parseLatestRelease({ tag_name: tag }) !== version) {
    return fallback(
      `the generated release file is inconsistent: tag ${JSON.stringify(tag)} does not match version ${JSON.stringify(version)}`,
    );
  }

  return { version: version as string, tag, source };
}

/** Where the generated record lives; `CCRESU_RELEASE_FILE` overrides it for tests. */
export function generatedReleasePath(): string {
  const override = process.env.CCRESU_RELEASE_FILE?.trim();
  if (override && override.length > 0) {
    return override;
  }
  return join(process.cwd(), "lib", "generated", "release.json");
}

/** The generated record, or null when the prebuild step did not run. */
export function readGeneratedRelease(path: string = generatedReleasePath()): unknown {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error);
    // A missing file is the documented case, not a catastrophe: `npm run dev`
    // and the test suite run without the prebuild step.
    return { source: "unavailable", reason: `could not read ${path}: ${reason}` };
  }
}

let cached: { path: string; info: ReleaseInfo } | null = null;

/**
 * The version this build advertises: read once, from the file the prebuild step
 * wrote.
 *
 * Memoised because every page asks and the answer cannot change during a build —
 * and because the warning should appear once in the log, not once per route. The
 * memo is keyed on the path so a caller that points at a different file (the
 * tests do) gets that file's answer rather than the previous one.
 */
export function resolvedRelease(): ReleaseInfo {
  const path = generatedReleasePath();
  if (cached === null || cached.path !== path) {
    cached = { path, info: releaseFromFile(readGeneratedRelease(path)) };
  }
  return cached.info;
}
