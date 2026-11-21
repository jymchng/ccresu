/**
 * The runtime half of the release resolution: everything here runs in Node, not
 * in a React render.
 *
 * It is plain JavaScript on purpose. The version is resolved by
 * `scripts/resolve-release.mjs` **before** `next build` starts, and the pages
 * only read the file that script writes — so the code that talks to GitHub has
 * to run under bare `node`, without a TypeScript loader. `lib/release.ts` is the
 * typed façade the rest of the site imports.
 *
 * Why the split at all: `fetch` called from a page render is served out of
 * Next's build data cache on a warm `.next`, so a second build silently baked
 * the *first* build's version. Three consecutive builds of the same tree with a
 * warm cache produced an export naming 0.9.0 while the API answered 0.10.0, and
 * the deploy published it. Nothing fetched during the render means nothing can
 * be served from that cache.
 */

/** The one endpoint that names the newest published release. */
export const LATEST_RELEASE_API = `https://api.github.com/repos/jymchng/ccresu/releases/latest`;

/** Long enough for a slow runner, short enough that a dead network cannot hang a build. */
const REQUEST_TIMEOUT_MS = 5000;

/**
 * The endpoint actually used.
 *
 * `CCRESU_RELEASE_API` overrides it for the two cases where the default is
 * wrong: a fork that publishes its own releases, and a build that wants to prove
 * the success path (the repository's own releases page is empty until the first
 * tag is published, so the default genuinely answers 404 today).
 */
export function latestReleaseApi() {
  const override = process.env.CCRESU_RELEASE_API?.trim();
  return override && override.length > 0 ? override : LATEST_RELEASE_API;
}

/**
 * The release tag a caller already knows, from `CCRESU_RELEASE_TAG`.
 *
 * The deploy workflow that a published release triggers passes the tag it was
 * triggered by, because that is the one fact the build cannot get wrong: the tag
 * exists (it is what fired the workflow) and the tree being built is the
 * released one. Asking `releases/latest` in that situation reintroduces the
 * race the override removes — the endpoint can keep answering with the previous
 * release for a few seconds after a release goes public, and nothing about the
 * resulting page would show that it named the wrong version.
 *
 * `null` means "not told", which is the case for a push to `main`: there the
 * API is the only source that knows what is published.
 */
export function pinnedTag() {
  const raw = process.env.CCRESU_RELEASE_TAG?.trim();
  if (!raw || raw.length === 0) {
    return null;
  }
  const tag = normalizeTag(raw);
  if (!tag) {
    console.warn(
      `[release] ignoring CCRESU_RELEASE_TAG=${raw}: not a release tag, so the published release will be read instead`,
    );
  }
  return tag;
}

/** `v1.2.3` → `1.2.3`. Returns null for anything that is not a release tag. */
export function normalizeTag(tag) {
  const stripped = tag.trim().replace(/^v/i, "");
  // Semver with an optional pre-release/build suffix: 0.1.0, 1.2.3-rc.1, 2.0.0+build.5
  if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.]+)*$/.test(stripped)) {
    return null;
  }
  return stripped;
}

/**
 * Read the tag out of a `/releases/latest` payload.
 *
 * Deliberately defensive: the payload is external input, and a release that
 * GitHub returns without a usable `tag_name` must not become `undefined` inside
 * a download URL.
 */
export function parseLatestRelease(payload) {
  if (typeof payload !== "object" || payload === null) {
    return null;
  }
  const tag = payload.tag_name;
  if (typeof tag !== "string") {
    return null;
  }
  return normalizeTag(tag);
}

/**
 * Resolve the release to advertise, exactly once per build.
 *
 * The order is deliberate: an explicitly pinned tag wins and costs no request,
 * so a release-triggered deploy cannot publish a stale version; then the API;
 * then `unavailable`, which the typed façade turns into the version pinned in
 * `lib/site.ts`.
 *
 * Every failure is reported, never thrown: a site that cannot be built is worse
 * than a site that names the last version we knew about.
 *
 * @param {{ fetchImpl?: typeof fetch, userAgent?: string }} [options]
 * @returns {Promise<{ source: "tag"|"github"|"unavailable", tag: string|null, version: string|null, endpoint: string|null, reason: string|null }>}
 */
export async function fetchLatestRelease({ fetchImpl = fetch, userAgent = "ccresu-website" } = {}) {
  const pinned = pinnedTag();
  if (pinned) {
    return {
      source: "tag",
      tag: `v${pinned}`,
      version: pinned,
      endpoint: null,
      reason: null,
    };
  }

  const endpoint = latestReleaseApi();
  try {
    const response = await fetchImpl(endpoint, {
      headers: {
        Accept: "application/vnd.github+json",
        "User-Agent": userAgent,
      },
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });

    if (!response.ok) {
      return {
        source: "unavailable",
        tag: null,
        version: null,
        endpoint,
        reason: `${endpoint} answered ${response.status} ${response.statusText}`.trim(),
      };
    }

    const version = parseLatestRelease(await response.json());
    if (!version) {
      return {
        source: "unavailable",
        tag: null,
        version: null,
        endpoint,
        reason: "the latest release carried no usable tag",
      };
    }

    return {
      source: "github",
      tag: `v${version}`,
      version,
      endpoint,
      reason: null,
    };
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error);
    return {
      source: "unavailable",
      tag: null,
      version: null,
      endpoint,
      reason: `request failed: ${reason}`,
    };
  }
}
