import { assetName, fallbackReleaseVersion, releaseDownloadBase, repo } from "@/lib/site";

import type { DocPage } from "./types";

/**
 * The installation page depends on a value the content cannot know: which
 * release is newest. Everything else in the documentation is static, so the
 * page is built by a function rather than exported as a constant — the caller
 * supplies the version that `lib/release.ts` resolved at build time.
 *
 * The default exists so navigation, the docs index and the tests can hold a
 * page without a network round trip; the page itself always passes the real
 * version.
 */
export function createInstallationDoc(
  version: string = fallbackReleaseVersion,
): DocPage {
  const linuxAsset = assetName(version, "linux", "amd64");
  const macAsset = assetName(version, "darwin", "arm64");
  const winAsset = assetName(version, "windows", "amd64");

  return {
  slug: "installation",
  title: "Installation",
  summary:
    "Install a signed-off release binary, or build from source with Go 1.26 or newer.",
  blocks: [
    {
      kind: "p",
      text: `Releases are published on the GitHub releases page (${repo.releases}). Each tag produces one archive per supported platform plus a \`checksums.txt\` file containing the SHA-256 of every asset. Verify the checksum before you run the binary.`,
    },
    {
      kind: "h2",
      text: "Download a release",
    },
    {
      kind: "p",
      text: `Replace the version in the file name with the tag you want. The commands below use \`${version}\`, the latest published release when this page was built.`,
    },
    {
      kind: "code",
      label: "Linux, x86-64",
      code: `VERSION=${version}\nASSET=${linuxAsset}\nBASE=${releaseDownloadBase(version)}\n\nmkdir -p ~/.local/bin\ncurl -fsSLO "$BASE/$ASSET"\ncurl -fsSLO "$BASE/checksums.txt"\nsha256sum --check --ignore-missing checksums.txt\ntar -xzf "$ASSET" ccresu\ninstall -m 0755 ccresu ~/.local/bin/ccresu\nrm -f "$ASSET" checksums.txt ccresu\nccresu version`,
    },
    {
      kind: "code",
      label: "macOS, Apple silicon",
      code: `VERSION=${version}\nASSET=${macAsset}\nBASE=${releaseDownloadBase(version)}\n\nmkdir -p ~/.local/bin\ncurl -fsSLO "$BASE/$ASSET"\ncurl -fsSLO "$BASE/checksums.txt"\nshasum -a 256 --check --ignore-missing checksums.txt\ntar -xzf "$ASSET" ccresu\ninstall -m 0755 ccresu ~/.local/bin/ccresu\nxattr -d com.apple.quarantine ~/.local/bin/ccresu 2>/dev/null || true`,
    },
    {
      kind: "code",
      label: "Windows, x86-64 (PowerShell)",
      code: `$VERSION = "${version}"\n$Asset = "${winAsset}"\n$Base = "${releaseDownloadBase(version)}"\n\nInvoke-WebRequest "$Base/$Asset" -OutFile $Asset\nInvoke-WebRequest "$Base/checksums.txt" -OutFile checksums.txt\n$Expected = (Select-String -Path checksums.txt -Pattern $Asset).Line.Split()[0]\nif ((Get-FileHash $Asset -Algorithm SHA256).Hash -ne $Expected.ToUpper()) { throw "checksum mismatch" }\nExpand-Archive $Asset -DestinationPath "$env:LOCALAPPDATA\\ccresu"`,
    },
    {
      kind: "note",
      tone: "info",
      title: "Asset naming",
      text: "Assets are named `ccresu_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows), for example `${linuxAsset}`. Supported targets: linux and darwin on amd64 and arm64, windows on amd64 and arm64.",
    },
    {
      kind: "h2",
      text: "Install with Go",
    },
    {
      kind: "p",
      text: "The SQLite driver is pure Go, so the build needs no cgo and no system SQLite library.",
    },
    {
      kind: "code",
      label: "Go 1.26 or newer",
      code: `CGO_ENABLED=0 go install ${repo.url.replace("https://", "")}/cmd/ccresu@latest\nccresu version`,
    },
    {
      kind: "h2",
      text: "Build from source",
    },
    {
      kind: "code",
      label: "Clone and build",
      code: `git clone ${repo.clone}\ncd ccresu\nmake build          # writes ./ccresu with version, commit and build date\n./ccresu version`,
    },
    {
      kind: "h2",
      text: "First run",
    },
    {
      kind: "ol",
      items: [
        "Create `~/.config/ccresu/config.yaml` with `ccresu config init`, then edit it.",
        "Run `ccresu config validate` to check the file, the environment overrides and every default.",
        "Run `ccresu doctor` to check the Claude Code CLI, transcript access, the database and the data directory.",
        "Start the watchdog with `ccresu daemon`, or install it as a service (see Deployment).",
      ],
    },
    {
      kind: "p",
      text: "ccresu creates its SQLite database at `~/.local/share/ccresu/ccresu.db` and, if the API is enabled, a bearer token with mode 0600 beside the configuration file in use (`~/.config/ccresu/api-token` for the default configuration). Neither is created until it is needed.",
    },
    {
      kind: "h2",
      text: "Requirements",
    },
    {
      kind: "ul",
      items: [
        "Claude Code installed and logged in: discovery runs `claude agents --json`, so `claude` must be on PATH.",
        "Read access to `~/.claude/projects` and `~/.claude/session-env` for transcript-based detection and verification.",
        "No root privileges: everything lives under your home directory.",
      ],
    },
  ],
  };
}
