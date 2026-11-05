import { describe, expect, it, vi } from "vitest";

import {
  LATEST_RELEASE_API,
  latestReleaseApi,
  normalizeTag,
  parseLatestRelease,
  resolveLatestRelease,
  type ReleaseInfo,
} from "@/lib/release";
import { fallbackReleaseVersion, repo } from "@/lib/site";

/**
 * The version the install commands name.
 *
 * Two things are being tested, and only one of them is the happy path:
 *
 *  1. a real GitHub answer is read correctly and the `v` is stripped, because
 *     the asset names use `x.y.z` while the tag is `vx.y.z`;
 *  2. **every** way the request can fail falls back instead of throwing. The
 *     site is a static export built in CI: a 403 from the unauthenticated rate
 *     limit, a dead network, an HTML error page or a release without a tag must
 *     all still produce a page. A build that cannot run is a worse outcome than
 *     a page naming the last version we knew about.
 */

function jsonResponse(payload: unknown, init: { ok?: boolean; status?: number } = {}) {
  return {
    ok: init.ok ?? true,
    status: init.status ?? 200,
    statusText: init.status === 403 ? "Forbidden" : "OK",
    json: async () => payload,
  } as unknown as Response;
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

  it("returns the published version when GitHub answers", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse({ tag_name: "v0.9.0" }));
    const release = await resolveLatestRelease(fetchImpl as unknown as typeof fetch);

    expect(release).toEqual<ReleaseInfo>({
      version: "0.9.0",
      tag: "v0.9.0",
      source: "github",
    });

    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(LATEST_RELEASE_API);
    expect((init.headers as Record<string, string>).Accept).toBe(
      "application/vnd.github+json",
    );
  });

  it("falls back instead of throwing when the network fails", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const fetchImpl = vi.fn(async () => {
      throw new Error("getaddrinfo ENOTFOUND api.github.com");
    });

    const release = await resolveLatestRelease(fetchImpl as unknown as typeof fetch);

    expect(release.source).toBe("fallback");
    expect(release.version).toBe(fallbackReleaseVersion);
    expect(release.tag).toBeNull();
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("getaddrinfo ENOTFOUND api.github.com"),
    );
  });

  it("falls back on a rate-limited or rejected request", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const fetchImpl = vi.fn(async () => jsonResponse({ message: "rate limited" }, { ok: false, status: 403 }));

    const release = await resolveLatestRelease(fetchImpl as unknown as typeof fetch);

    expect(release).toEqual<ReleaseInfo>({
      version: fallbackReleaseVersion,
      tag: null,
      source: "fallback",
    });
  });

  it("falls back when the payload has no usable tag", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const fetchImpl = vi.fn(async () => jsonResponse({ message: "Not Found" }));

    const release = await resolveLatestRelease(fetchImpl as unknown as typeof fetch);

    expect(release.source).toBe("fallback");
    expect(release.version).toBe(fallbackReleaseVersion);
  });

  it("falls back when the body is not JSON at all", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const fetchImpl = vi.fn(async () => ({
      ok: true,
      status: 200,
      statusText: "OK",
      json: async () => {
        throw new SyntaxError("Unexpected token < in JSON");
      },
    })) as unknown as typeof fetch;

    const release = await resolveLatestRelease(fetchImpl);

    expect(release.source).toBe("fallback");
  });

  it("the fallback is a version the asset naming accepts", () => {
    expect(normalizeTag(fallbackReleaseVersion)).toBe(fallbackReleaseVersion);
  });

  it("lets a fork or a smoke test point the resolver elsewhere", async () => {
    vi.stubEnv("CCRESU_RELEASE_API", "https://example.test/releases/latest");
    try {
      expect(latestReleaseApi()).toBe("https://example.test/releases/latest");

      const fetchImpl = vi.fn(async () => jsonResponse({ tag_name: "v3.1.4" }));
      const release = await resolveLatestRelease(fetchImpl as unknown as typeof fetch);

      expect(release.version).toBe("3.1.4");
      expect((fetchImpl.mock.calls[0] as unknown as [string])[0]).toBe(
        "https://example.test/releases/latest",
      );
    } finally {
      vi.unstubAllEnvs();
    }

    // Unset, the canonical endpoint comes back.
    expect(latestReleaseApi()).toBe(LATEST_RELEASE_API);
  });
});
