import { fallbackReleaseVersion, site } from "./site";

/**
 * Where the install commands get their version from.
 *
 * The site is a static export, so there is no server at request time: the
 * version is resolved **when the site is built** and baked into the HTML. That
 * is the right trade for a documentation site — a reader gets a complete page
 * with no extra request and no layout shift — but it has consequences worth
 * stating:
 *
 *  - the version is as fresh as the last deploy, so the deploy workflow is what
 *    keeps it current;
 *  - the build must not fail because GitHub is unreachable, rate-limiting an
 *    unauthenticated request, or serving something unexpected. A site that
 *    cannot be built is worse than a site that names the last version we knew
 *    about, so every failure path falls back to the version pinned in
 *    `lib/site.ts` and says so in the build log.
 *
 * Only the tag is read; nothing from the API is trusted beyond that, and the
 * tag is re-shaped into the `x.y.z` form the asset names use.
 */

export type ReleaseSource = "github" | "fallback";

export type ReleaseInfo = {
  /** `x.y.z`, without the leading `v`: exactly what the asset names carry. */
  version: string;
  /** The tag as GitHub reports it, or null when the fallback was used. */
  tag: string | null;
  /** Where the version came from; "fallback" means GitHub was not consulted. */
  source: ReleaseSource;
};

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
export function latestReleaseApi(): string {
  const override = process.env.CCRESU_RELEASE_API?.trim();
  return override && override.length > 0 ? override : LATEST_RELEASE_API;
}

/** `v1.2.3` → `1.2.3`. Returns null for anything that is not a release tag. */
export function normalizeTag(tag: string): string | null {
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
export function parseLatestRelease(payload: unknown): string | null {
  if (typeof payload !== "object" || payload === null) {
    return null;
  }
  const tag = (payload as { tag_name?: unknown }).tag_name;
  if (typeof tag !== "string") {
    return null;
  }
  return normalizeTag(tag);
}

function fallback(reason: string): ReleaseInfo {
  console.warn(
    `[release] falling back to the pinned version ${fallbackReleaseVersion}: ${reason}`,
  );
  return { version: fallbackReleaseVersion, tag: null, source: "fallback" };
}

/**
 * Ask GitHub for the newest release tag.
 *
 * `fetchImpl` exists so the failure paths are testable without a network: the
 * tests below cover a network error, a 403 from the unauthenticated rate limit,
 * a non-JSON body and a payload without a usable tag.
 */
export async function resolveLatestRelease(
  fetchImpl: typeof fetch = fetch,
): Promise<ReleaseInfo> {
  const endpoint = latestReleaseApi();
  try {
    const response = await fetchImpl(endpoint, {
      headers: {
        Accept: "application/vnd.github+json",
        "User-Agent": `${site.name}-website`,
      },
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });

    if (!response.ok) {
      return fallback(
        `${endpoint} answered ${response.status} ${response.statusText}`.trim(),
      );
    }

    const version = parseLatestRelease(await response.json());
    if (!version) {
      return fallback("the latest release carried no usable tag");
    }

    return { version, tag: `v${version}`, source: "github" };
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error);
    return fallback(`request failed: ${reason}`);
  }
}
