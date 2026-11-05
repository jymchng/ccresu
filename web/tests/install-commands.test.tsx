import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { DocPageView } from "@/components/doc-page-view";
import { InstallCommands, installCommand } from "@/components/install-commands";
import { createInstallationDoc } from "@/lib/docs/installation";
import type { Block } from "@/lib/docs/types";
import {
  assetName,
  assetUrl,
  fallbackReleaseVersion,
  installTargets,
  releaseDownloadBase,
  repo,
} from "@/lib/site";

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

  it("verifies the checksum before installing on unix", () => {
    const command = installCommand("linux", "amd64", fallbackReleaseVersion);

    expect(command).toContain(
      `curl -fsSLO "$BASE/checksums.txt"`,
    );
    expect(command).toContain("sha256sum --check");
    expect(command).toContain("install -m 0755 ccresu ~/.local/bin/ccresu");
    expect(command).toContain(`ccresu_${fallbackReleaseVersion}_linux_amd64.tar.gz`);
  });

  it("uses shasum and no root on macOS", () => {
    const command = installCommand("darwin", "arm64", fallbackReleaseVersion);
    expect(command).toContain("shasum -a 256 --check");
    expect(command).not.toContain("sudo");
  });

  it("verifies the hash in PowerShell on Windows", () => {
    const command = installCommand("windows", "amd64", fallbackReleaseVersion);
    expect(command).toContain("Get-FileHash");
    expect(command).toContain("checksum mismatch");
    expect(command).toContain(`ccresu_${fallbackReleaseVersion}_windows_amd64.zip`);
  });

  it("switches platform when a tab is chosen", async () => {
    const user = userEvent.setup();
    render(<InstallCommands version={fallbackReleaseVersion} />);

    expect(screen.getByRole("tab", { name: "Linux x86-64" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText(/linux_amd64\.tar\.gz/)).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Windows x86-64" }));

    expect(screen.getByRole("tab", { name: "Windows x86-64" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText(/windows_amd64\.zip/)).toBeInTheDocument();
  });

  it("offers every published target as a tab", () => {
    render(<InstallCommands version={fallbackReleaseVersion} />);

    const tabs = screen.getAllByRole("tab");

    expect(tabs).toHaveLength(installTargets.length);
    expect(tabs.map((tab) => tab.textContent)).toEqual(
      installTargets.map((target) => target.label),
    );
  });

  it("downloads from the pinned-tag URL GitHub actually serves", () => {
    // The two halves the installer scripts join with a slash.
    expect(assetUrl(fallbackReleaseVersion, "linux", "amd64")).toBe(
      `${releaseDownloadBase(fallbackReleaseVersion)}/${assetName(fallbackReleaseVersion, "linux", "amd64")}`,
    );

    for (const target of installTargets) {
      const command = installCommand(target.os, target.arch, fallbackReleaseVersion);

      expect(command).toContain(releaseDownloadBase(fallbackReleaseVersion));
      expect(command).toContain(assetName(fallbackReleaseVersion, target.os, target.arch));
      // `/releases/latest/download/<tag>/<asset>` is a 404; only the two
      // documented shapes resolve.
      expect(command).not.toContain("/latest/download/");
      expect(command).toContain(
        `https://github.com/${repo.owner}/${repo.name}/releases/download/v${fallbackReleaseVersion}`,
      );
    }
  });

  it("installation doc uses the same download base as the landing page", () => {
    const installationDoc = createInstallationDoc(fallbackReleaseVersion);
    const codeBlocks = installationDoc.blocks.filter(
      (block): block is Extract<Block, { kind: "code" }> => block.kind === "code",
    );
    const downloadBlocks = codeBlocks.filter((block) =>
      block.code.includes("/$ASSET"),
    );

    expect(downloadBlocks.length).toBeGreaterThan(0);

    for (const block of downloadBlocks) {
      expect(block.code).toContain(releaseDownloadBase(fallbackReleaseVersion));
      expect(block.code).not.toContain("/latest/download/");
      expect(block.code).toContain("checksums.txt");
    }
  });

  it("prints the version the resolver reported, not the pinned fallback", () => {
    const resolved = "9.9.9";
    expect(resolved).not.toBe(fallbackReleaseVersion);

    // The landing-page command block.
    const home = render(<InstallCommands version={resolved} />);
    expect(home.container.textContent).toContain(
      `ccresu_${resolved}_linux_amd64.tar.gz`,
    );
    expect(home.container.textContent).toContain(
      `/releases/download/v${resolved}`,
    );
    expect(home.container.textContent).not.toContain(`_${fallbackReleaseVersion}_`);
    home.unmount();

    // The installation page, whose three download snippets also name a release.
    const doc = render(<DocPageView slug="installation" version={resolved} />);
    expect(doc.container.textContent).toContain(`VERSION=${resolved}`);
    expect(doc.container.textContent).toContain(
      `/releases/download/v${resolved}`,
    );
    expect(doc.container.textContent).not.toContain(fallbackReleaseVersion);
  });
});
