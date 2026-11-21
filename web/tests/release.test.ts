import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { afterAll, beforeEach, describe, expect, it, vi } from "vitest";

import {
  LATEST_RELEASE_API,
  latestReleaseApi,
  normalizeTag,
  parseLatestRelease,
  pinnedTag,
  readGeneratedRelease,
  releaseFromFile,
  resolvedRelease,
  type ReleaseInfo,
} from "@/lib/release";
import { fallbackReleaseVersion, repo } from "@/lib/site";

/**
 * The version the install commands name.
 *
 * Two things are being tested, and only one of them is the happy path:
 *
 *  1. the record the prebuild step wrote is read correctly — the `v` is stripped
 *     because the asset names use `x.y.z` while the tag is `vx.y.z`, and a
 *     handed tag is believed without a request;
 *  2. **every** way the resolution can fail falls back instead of throwing. The
 *     site is a static export built in CI: a 403 from the unauthenticated rate
 *     limit, a dead network, an HTML error page, a release without a tag, or no
 *     generated file at all must all still produce a page. A build that cannot
 *     run is a worse outcome than a page naming the last version we knew about.
 *
 * What is deliberately *not* here any more is a `fetch` at render time: the
 * resolution moved to `scripts/resolve-release.mjs`, because a `fetch` inside a
 * page render is served from Next's build data cache and a warm `.next` made the
 * second build reuse the first build's version. `tests/release-generation.test.ts`
 * drives that script; `tests/build-cache.test.ts` proves two consecutive builds
 * disagree when the release changes.
 */

const scratch = mkdtempSync(path.join(tmpdir(), "ccresu-release-"));

afterAll(() => {
  rmSync(scratch, { recursive: true, force: true });
});

beforeEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

/** Write a generated record and point the loader at it. */
function generated(name: string, record: unknown): string {
  const file = path.join(scratch, `${name}.json`);
  writeFileSync(file, JSON.stringify(record));
  vi.stubEnv("CCRESU_RELEASE_FILE", file);
  return file;
}

describe("release resolution", () => {
  it("asks the endpoint that names the newest release of this repository", () => {
    expect(LATEST_RELEASE_API).toBe(
      `https://api.github.com/repos/${repo.owner}/${repo.name}/releases/latest`,
    );
  });

  it("strips the leading v, because the asset names do not carry it", () => {
    expect(normalizeTag("v1.4.2")).toBe("1.4.2");
    expect(normalizeTag("1.4.2")).toBe("1.4.2");
    expect(normalizeTag("  v0.10.0  ")).toBe("0.10.0");
    // Pre-releases are still usable tags: v2.0.0-rc.1 ships as ccresu_2.0.0-rc.1_...
    expect(normalizeTag("v2.0.0-rc.1")).toBe("2.0.0-rc.1");
  });

  it("refuses a tag it cannot turn into an asset name", () => {
    for (const tag of ["", "latest", "v1", "v1.2", "nightly-2026-01-01", "v1.2.3.4"]) {
      expect(normalizeTag(tag)).toBeNull();
    }
  });

  it("reads the tag out of a release payload, and nothing else", () => {
    expect(parseLatestRelease({ tag_name: "v0.3.1", name: "0.3.1" })).toBe("0.3.1");
    expect(parseLatestRelease({ tag_name: 42 })).toBeNull();
    expect(parseLatestRelease({ name: "no tag here" })).toBeNull();
    expect(parseLatestRelease(null)).toBeNull();
    expect(parseLatestRelease("v1.2.3")).toBeNull();
  });

  it("believes the tag a release-triggered build was handed", () => {
    generated("tag", {
      source: "tag",
      version: "0.10.0",
      tag: "v0.10.0",
      endpoint: null,
      reason: null,
    });

    expect(resolvedRelease()).toEqual<ReleaseInfo>({
      version: "0.10.0",
      tag: "v0.10.0",
      source: "tag",
    });
  });

  it("returns the published version when GitHub answered", () => {
    generated("github", {
      source: "github",
      version: "0.9.0",
      tag: "v0.9.0",
      endpoint: LATEST_RELEASE_API,
      reason: null,
    });

    expect(resolvedRelease()).toEqual<ReleaseInfo>({
      version: "0.9.0",
      tag: "v0.9.0",
      source: "github",
    });
  });

  it("falls back, and repeats the recorded reason, when the release could not be read", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const file = generated("unavailable", {
      source: "unavailable",
      version: null,
      tag: null,
      endpoint: LATEST_RELEASE_API,
      reason: `${LATEST_RELEASE_API} answered 403 Forbidden`,
    });

    const release = releaseFromFile(readGeneratedRelease(file));

    expect(release).toEqual<ReleaseInfo>({
      version: fallbackReleaseVersion,
      tag: null,
      source: "fallback",
    });
    // The reason travels with the record, so a fallback is legible instead of
    // indistinguishable from a real answer.
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("answered 403 Forbidden"),
    );
  });

  it("falls back when the prebuild step never ran", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const missing = path.join(scratch, "not-written.json");
    vi.stubEnv("CCRESU_RELEASE_FILE", missing);

    expect(resolvedRelease().source).toBe("fallback");
    expect(resolvedRelease().version).toBe(fallbackReleaseVersion);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("could not read"),
    );
  });

  it("falls back rather than trusting a record it cannot make sense of", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});

    // A version that is not the tag minus its `v` would put an arbitrary string
    // into a download URL.
    expect(releaseFromFile({ source: "github", version: "1.0.0", tag: "v2.0.0" }).source).toBe(
      "fallback",
    );
    expect(releaseFromFile({ source: "unknown", version: "1.0.0", tag: "v1.0.0" }).source).toBe(
      "fallback",
    );
    expect(releaseFromFile({ source: "github", version: null, tag: null }).source).toBe("fallback");
    expect(releaseFromFile(null).source).toBe("fallback");
    expect(releaseFromFile("v0.10.0").source).toBe("fallback");
  });

  it("reads the version exactly once per process", () => {
    const file = generated("memo", {
      source: "github",
      version: "0.8.0",
      tag: "v0.8.0",
      endpoint: LATEST_RELEASE_API,
      reason: null,
    });

    expect(resolvedRelease().version).toBe("0.8.0");

    // Rewriting the file cannot change an answer this build already took; the
    // memo is what keeps a single warning in the log rather than one per route.
    writeFileSync(
      file,
      JSON.stringify({ source: "github", version: "9.9.9", tag: "v9.9.9", reason: null }),
    );
    expect(resolvedRelease().version).toBe("0.8.0");
  });

  it("the fallback is a version the asset naming accepts", () => {
    expect(normalizeTag(fallbackReleaseVersion)).toBe(fallbackReleaseVersion);
  });

  it("lets a fork or a smoke test point the resolver elsewhere", () => {
    vi.stubEnv("CCRESU_RELEASE_API", "https://example.test/releases/latest");
    expect(latestReleaseApi()).toBe("https://example.test/releases/latest");

    vi.unstubAllEnvs();
    // Unset, the canonical endpoint comes back.
    expect(latestReleaseApi()).toBe(LATEST_RELEASE_API);
  });

  it("ignores a pinned value that is not a release tag, and says so", () => {
    vi.stubEnv("CCRESU_RELEASE_TAG", "nightly-2026-11-21");
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    expect(pinnedTag()).toBeNull();
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("CCRESU_RELEASE_TAG=nightly-2026-11-21"),
    );
  });

  it("normalises a pinned tag the deploy was triggered by", () => {
    vi.stubEnv("CCRESU_RELEASE_TAG", "v0.10.0");
    expect(pinnedTag()).toBe("0.10.0");

    vi.unstubAllEnvs();
    expect(pinnedTag()).toBeNull();
  });
});
