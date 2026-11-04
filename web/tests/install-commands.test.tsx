import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { InstallCommands, installCommand } from "@/components/install-commands";
import { assetName, exampleVersion } from "@/lib/site";

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
    const command = installCommand("linux", "amd64");

    expect(command).toContain(
      `curl -fsSLO "$BASE/checksums.txt"`,
    );
    expect(command).toContain("sha256sum --check");
    expect(command).toContain("install -m 0755 ccresu ~/.local/bin/ccresu");
    expect(command).toContain(`ccresu_${exampleVersion}_linux_amd64.tar.gz`);
  });

  it("uses shasum and no root on macOS", () => {
    const command = installCommand("darwin", "arm64");
    expect(command).toContain("shasum -a 256 --check");
    expect(command).not.toContain("sudo");
  });

  it("verifies the hash in PowerShell on Windows", () => {
    const command = installCommand("windows", "amd64");
    expect(command).toContain("Get-FileHash");
    expect(command).toContain("checksum mismatch");
    expect(command).toContain(`ccresu_${exampleVersion}_windows_amd64.zip`);
  });

  it("switches platform when a tab is chosen", async () => {
    const user = userEvent.setup();
    render(<InstallCommands />);

    expect(screen.getByRole("tab", { name: "Linux x86-64" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText(/linux_amd64\.tar\.gz/)).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Windows" }));

    expect(screen.getByRole("tab", { name: "Windows" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText(/windows_amd64\.zip/)).toBeInTheDocument();
  });
});
