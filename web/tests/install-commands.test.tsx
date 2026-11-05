import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { DocPageView } from "@/components/doc-page-view";
import { InstallCommands, installCommand } from "@/components/install-commands";
import { createInstallationDoc } from "@/lib/docs/installation";
import type { Block } from "@/lib/docs/types";
import {
  installCommandNames,
  installScripts,
  renderInstallCommand,
} from "@/lib/install-commands";
import {
  assetName,
  assetUrl,
  fallbackReleaseVersion,
  installPlatforms,
  releaseDownloadBase,
  repo,
} from "@/lib/site";

/** The code blocks of the installation page, parsed once. */
function installationCodeBlocks(version = fallbackReleaseVersion) {
  return createInstallationDoc(version).blocks.filter(
    (block): block is Extract<Block, { kind: "code" }> => block.kind === "code",
  );
}

describe("install commands", () => {
  it("names the assets the release workflow publishes", () => {
    expect(assetName("0.1.0", "linux", "amd64")).toBe(
      "ccresu_0.1.0_linux_amd64.tar.gz",
    );
    expect(assetName("0.1.0", "darwin", "arm64")).toBe(
      "ccresu_0.1.0_darwin_arm64.tar.gz",
    );
    expect(assetName("0.1.0", "windows", "arm64")).toBe(
      "ccresu_0.1.0_windows_arm64.zip",
    );
  });

  it("renders every command as a single line", () => {
    for (const name of installCommandNames) {
      const command = renderInstallCommand(name, fallbackReleaseVersion);
      expect(command, name).not.toContain("\n");
      expect(command.trim(), name).toBe(command);
      expect(command.length, name).toBeGreaterThan(0);
    }
  });

  it("leaves no placeholder behind", () => {
    for (const name of installCommandNames) {
      const command = renderInstallCommand(name, "9.9.9");
      expect(command, name).not.toMatch(/\{[a-zA-Z]+\}/);
      expect(command, name).not.toContain("<version>");
    }
  });

  it("pipes the documented installer scripts, one line each", () => {
    // The Unix line runs the shell installer and nothing else.
    expect(renderInstallCommand("unix")).toBe(
      `curl -fsSL ${installScripts.unix} | bash`,
    );
    // Pinning is an environment variable in the same line, so the command stays
    // a one-liner instead of turning into a script.
    expect(renderInstallCommand("unixPinned", "1.2.3")).toBe(
      `curl -fsSL ${installScripts.unix} | CCRESU_VERSION=1.2.3 bash`,
    );
    // The dry run changes nothing and is still one line.
    expect(renderInstallCommand("unixPreview")).toContain("--dry-run");

    expect(renderInstallCommand("windows")).toBe(
      `irm ${installScripts.windows} | iex`,
    );
    expect(renderInstallCommand("windowsPinned", "1.2.3")).toBe(
      `$env:CCRESU_VERSION = '1.2.3'; irm ${installScripts.windows} | iex`,
    );
  });

  it("keeps the documented raw URLs consistent with the repository", () => {
    // The commands carry raw.githubusercontent.com URLs, which are a fact about
    // the repository that `lib/site.ts` also states. Both must agree.
    const rawPrefix = `https://raw.githubusercontent.com/${repo.owner}/${repo.name}/main/`;
    expect(installScripts.unix).toBe(`${rawPrefix}scripts/install.sh`);
    expect(installScripts.windows).toBe(`${rawPrefix}scripts/install.ps1`);

    expect(renderInstallCommand("source")).toContain(repo.clone);
    expect(renderInstallCommand("go")).toContain(
      `${repo.url.replace("https://", "")}/cmd/ccresu@latest`,
    );
  });

  it("pins the version the resolver reported, not the fallback", () => {
    const resolved = "9.9.9";
    expect(resolved).not.toBe(fallbackReleaseVersion);

    const unix = renderInstallCommand("unixPinned", resolved);
    expect(unix).toContain(`CCRESU_VERSION=${resolved}`);
    expect(unix).not.toContain(fallbackReleaseVersion);

    const windows = renderInstallCommand("windowsPinned", resolved);
    expect(windows).toContain(`'${resolved}'`);
    expect(windows).not.toContain(fallbackReleaseVersion);
  });

  it("installs with a one-liner on every platform tab", async () => {
    const user = userEvent.setup();
    render(<InstallCommands version={fallbackReleaseVersion} />);

    expect(screen.getByRole("tab", { name: /Linux/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(
      screen.getByText(new RegExp(`CCRESU_VERSION=${fallbackReleaseVersion}`)),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: /Windows/ }));

    expect(screen.getByRole("tab", { name: /Windows/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText(/CCRESU_VERSION/)).toHaveTextContent("irm");
  });

  it("offers one tab per operating system, which is what the installers detect", () => {
    render(<InstallCommands version={fallbackReleaseVersion} />);

    const tabs = screen.getAllByRole("tab");

    expect(tabs).toHaveLength(installPlatforms.length);
    for (const platform of installPlatforms) {
      expect(
        screen.getByRole("tab", { name: new RegExp(platform.label) }),
      ).toBeInTheDocument();
    }
  });

  it("downloads from the pinned-tag URL GitHub actually serves", () => {
    // Two halves the installers join with a slash; `/releases/latest/download/
    // <tag>/<asset>` is a 404, so only the two documented shapes resolve.
    expect(assetUrl(fallbackReleaseVersion, "linux", "amd64")).toBe(
      `${releaseDownloadBase(fallbackReleaseVersion)}/${assetName(fallbackReleaseVersion, "linux", "amd64")}`,
    );
    expect(assetUrl(fallbackReleaseVersion, "linux", "amd64")).not.toContain(
      "/latest/download/",
    );
  });

  it("gives the installation page only one-line commands", () => {
    const blocks = installationCodeBlocks();

    expect(blocks.length).toBeGreaterThanOrEqual(4);
    for (const block of blocks) {
      expect(block.code, block.label).not.toContain("\n");
      expect(block.code, block.label).not.toMatch(/^\s*#/);
    }
  });

  it("documentation page and landing page render the same strings", () => {
    const version = fallbackReleaseVersion;
    const blocks = installationCodeBlocks(version).map((block) => block.code);

    // The documented commands are the rendered commands, not parallel copies.
    expect(blocks).toContain(renderInstallCommand("unix", version));
    expect(blocks).toContain(renderInstallCommand("unixPreview", version));
    expect(blocks).toContain(renderInstallCommand("unixPinned", version));
    expect(blocks).toContain(renderInstallCommand("windows", version));
    expect(blocks).toContain(renderInstallCommand("windowsPinned", version));
    expect(blocks).toContain(renderInstallCommand("go", version));
    expect(blocks).toContain(renderInstallCommand("source", version));

    const home = render(<InstallCommands version={version} />);
    expect(home.container.textContent).toContain(
      installCommand("linux", version),
    );
    home.unmount();
  });

  it("shows the resolved version on the installation page", () => {
    const resolved = "9.9.9";
    const doc = render(<DocPageView slug="installation" version={resolved} />);

    expect(doc.container.textContent).toContain(`CCRESU_VERSION=${resolved}`);
    expect(doc.container.textContent).not.toContain(fallbackReleaseVersion);
  });
});
