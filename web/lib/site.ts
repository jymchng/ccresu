/**
 * Single source of truth for every external link and release asset name the
 * site shows. Nothing in the UI hard-codes a URL, so a link can only be wrong
 * in one place.
 *
 * The release asset convention below is the contract the release workflow
 * (`release.yml`) must produce:
 *
 *   ccresu_<version>_<os>_<arch>.tar.gz    linux, darwin
 *   ccresu_<version>_<os>_<arch>.zip       windows
 *   checksums.txt                          sha256 of every asset
 *
 * with <version> taken from the git tag without its leading "v".
 */

export const site = {
  name: "ccresu",
  tagline: "Resume Claude Code sessions after a usage limit resets",
  description:
    "ccresu detects Claude Code usage-limit interruptions, waits for the real reset time, resumes the same session and verifies that the conversation actually continued.",
  url: "https://jymchng.github.io/ccresu",
  basePath: "/ccresu",
} as const;

export const repo = {
  owner: "jymchng",
  name: "ccresu",
  url: "https://github.com/jymchng/ccresu",
  releases: "https://github.com/jymchng/ccresu/releases",
  latestRelease: "https://github.com/jymchng/ccresu/releases/latest",
  issues: "https://github.com/jymchng/ccresu/issues",
  discussions: "https://github.com/jymchng/ccresu/discussions",
  license: "https://github.com/jymchng/ccresu/blob/main/LICENSE",
  clone: "https://github.com/jymchng/ccresu.git",
} as const;

export type Platform = "linux" | "darwin" | "windows";
export type Arch = "amd64" | "arm64";

/** Asset file name for one release target. */
export function assetName(version: string, os: Platform, arch: Arch): string {
  const extension = os === "windows" ? "zip" : "tar.gz";
  return `ccresu_${version}_${os}_${arch}.${extension}`;
}

/**
 * Base URL of one tag's downloadable assets.
 *
 * GitHub serves assets from exactly two shapes — `/releases/download/<tag>/<asset>`
 * for a pinned tag and `/releases/latest/download/<asset>` for whatever is newest.
 * These are not interchangeable: appending a tag to the `latest` form
 * (`/releases/latest/download/<tag>/<asset>`) is a 404, which is why the pinned
 * tag is expressed the way GitHub documents it.
 */
export function releaseDownloadBase(version: string): string {
  return `${repo.url}/releases/download/v${version}`;
}

/** Full download URL for one release target. */
export function assetUrl(version: string, os: Platform, arch: Arch): string {
  return `${releaseDownloadBase(version)}/${assetName(version, os, arch)}`;
}

/** Download URL of the checksum file that covers every asset in a release. */
export function checksumsUrl(version: string): string {
  return `${releaseDownloadBase(version)}/checksums.txt`;
}

/**
 * The version every surface falls back to when the newest release cannot be
 * read from GitHub.
 *
 * It is **not** what the site normally shows: `lib/release.ts` resolves the
 * latest published tag at build time and the install commands use that. This
 * value only exists so an offline, rate-limited or misconfigured build still
 * renders a coherent page — and so the fallback is a single obvious constant to
 * bump rather than a number scattered through the content.
 */
export const fallbackReleaseVersion = "0.1.0";

export const installTargets: ReadonlyArray<{
  os: Platform;
  arch: Arch;
  label: string;
}> = [
  { os: "linux", arch: "amd64", label: "Linux x86-64" },
  { os: "linux", arch: "arm64", label: "Linux ARM64" },
  { os: "darwin", arch: "arm64", label: "macOS Apple silicon" },
  { os: "darwin", arch: "amd64", label: "macOS Intel" },
  { os: "windows", arch: "amd64", label: "Windows x86-64" },
  { os: "windows", arch: "arm64", label: "Windows ARM64" },
];

export const localApi = {
  address: "127.0.0.1:8787",
  prefix: "/api/v1",
  tokenFile: "~/.config/ccresu/api-token",
} as const;

export const defaultPaths = {
  config: "~/.config/ccresu/config.yaml",
  database: "~/.local/share/ccresu/ccresu.db",
} as const;
