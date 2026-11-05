import { renderInstallCommand, installScripts } from "@/lib/install-commands";
import { assetName, fallbackReleaseVersion, repo } from "@/lib/site";

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
 *
 * Every command below is **one line**, and every one of them comes from
 * `install-commands.json` so that `.github/workflows/install-commands.yml` runs
 * the same string a reader copies. A multi-line block on this page would be a
 * script, and a script that is copied in halves is a broken install.
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
    "Install a released binary with a single line, or build from source with Go 1.26 or newer.",
  blocks: [
    {
      kind: "p",
      text: `Releases are published on the GitHub releases page (${repo.releases}). Each tag carries one archive per supported platform plus a \`checksums.txt\` with the SHA-256 of every asset, and the installer verifies the checksum before it extracts anything.`,
    },
    {
      kind: "h2",
      text: "Install with one line",
    },
    {
      kind: "p",
      text: "Linux and macOS, `amd64` or `arm64`: the installer detects the platform, resolves the newest release, downloads that archive and `checksums.txt`, verifies the hash, installs the binary into `~/.local/bin` and writes `~/.config/ccresu/config.yaml`.",
    },
    {
      kind: "code",
      label: "Linux and macOS",
      code: renderInstallCommand("unix"),
    },
    {
      kind: "p",
      text: "Preview it first if you prefer: the dry run prints every action and downloads nothing.",
    },
    {
      kind: "code",
      label: "Preview, changes nothing",
      code: renderInstallCommand("unixPreview"),
    },
    {
      kind: "p",
      text: `Pin a release by putting the version in the same line. The one on this page is \`${version}\`, the newest published release when the page was built.`,
    },
    {
      kind: "code",
      label: `Pinned to ${version}`,
      code: renderInstallCommand("unixPinned", version),
    },
    {
      kind: "h2",
      text: "Install on Windows",
    },
    {
      kind: "p",
      text: "The PowerShell installer does the same work in a normal, non-elevated window: it verifies the sha256 of the `.zip` before extracting it, installs `ccresu.exe` under `%LOCALAPPDATA%`, adds that directory to your user `PATH` and writes `%APPDATA%\\ccresu\\config.yaml`.",
    },
    {
      kind: "code",
      label: "Windows, PowerShell",
      code: renderInstallCommand("windows"),
    },
    {
      kind: "code",
      label: `Windows, pinned to ${version}`,
      code: renderInstallCommand("windowsPinned", version),
    },
    {
      kind: "p",
      text: `The installers are short and readable, and you should read what you pipe into a shell: [scripts/install.sh](${repo.url}/blob/main/scripts/install.sh) and [scripts/install.ps1](${repo.url}/blob/main/scripts/install.ps1). Re-running the same line is the supported upgrade path.`,
    },
    {
      kind: "note",
      tone: "info",
      title: "Asset naming",
      text: `Assets are named \`ccresu_<version>_<os>_<arch>.tar.gz\` (\`.zip\` on Windows), for example \`${linuxAsset}\`, \`${macAsset}\` and \`${winAsset}\`. Supported targets: linux and darwin on amd64 and arm64, windows on amd64 and arm64. The installers pick the matching one for the machine they run on.`,
    },
    {
      kind: "h2",
      text: "Install with Go",
    },
    {
      kind: "p",
      text: `The SQLite driver is pure Go, so the build needs no cgo and no system SQLite library. The line below installs into \`$(go env GOPATH)/bin\` and prints the version it produced.`,
    },
    {
      kind: "code",
      label: "Go 1.26 or newer",
      code: renderInstallCommand("go"),
    },
    {
      kind: "h2",
      text: "Build from source",
    },
    {
      kind: "code",
      label: "Clone, build, run",
      code: renderInstallCommand("source"),
    },
    {
      kind: "h2",
      text: "First run",
    },
    {
      kind: "ol",
      items: [
        "Create `~/.config/ccresu/config.yaml` with `ccresu config init`, then edit it. The installers write it for you already.",
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
        "No root privileges: everything lives under your home directory, and the installer refuses to run as root.",
        `The raw scripts the two lines above pipe into a shell: \`${installScripts.unix}\` and \`${installScripts.windows}\`.`,
      ],
    },
  ],
  };
}
