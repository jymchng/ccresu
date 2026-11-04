"use client";

import { useState } from "react";

import { CopyButton } from "@/components/copy-button";
import { cn } from "@/lib/utils";
import { assetName, exampleVersion, installTargets, releaseDownloadBase, type Platform } from "@/lib/site";

/**
 * Installation commands per platform.
 *
 * The asset names come from `lib/site.ts`, which documents the same convention
 * the release workflow must produce, so the site cannot advertise a file the
 * release does not build. The download base is the pinned-tag form
 * (`/releases/download/v<version>`), the only shape GitHub serves for a specific
 * version.
 */
export function installCommand(os: Platform, arch: "amd64" | "arm64"): string {
  const version = exampleVersion;
  const asset = assetName(version, os, arch);
  const base = releaseDownloadBase(version);

  if (os === "windows") {
    return [
      `$VERSION = "${version}"`,
      `$Asset = "${asset}"`,
      `$Base = "${base}"`,
      "",
      "Invoke-WebRequest \"$Base/$Asset\" -OutFile $Asset",
      "Invoke-WebRequest \"$Base/checksums.txt\" -OutFile checksums.txt",
      "$Expected = (Select-String -Path checksums.txt -Pattern $Asset).Line.Split()[0]",
      "if ((Get-FileHash $Asset -Algorithm SHA256).Hash -ne $Expected.ToUpper()) {",
      '  throw "checksum mismatch"',
      "}",
      'Expand-Archive $Asset -DestinationPath "$env:LOCALAPPDATA\\ccresu"',
    ].join("\n");
  }

  const digest = os === "darwin" ? "shasum -a 256 --check" : "sha256sum --check";

  return [
    `VERSION=${version}`,
    `ASSET=${asset}`,
    `BASE=${base}`,
    "",
    "mkdir -p ~/.local/bin",
    'curl -fsSLO "$BASE/$ASSET"',
    'curl -fsSLO "$BASE/checksums.txt"',
    `${digest} --ignore-missing checksums.txt`,
    'tar -xzf "$ASSET" ccresu',
    "install -m 0755 ccresu ~/.local/bin/ccresu",
    'rm -f "$ASSET" checksums.txt ccresu',
    "ccresu version",
  ].join("\n");
}

/**
 * The platform tabs come from `installTargets` in `lib/site.ts`, so the section
 * offers exactly the six targets the documentation says are published. Adding a
 * release target cannot leave the landing page behind.
 */
const options = installTargets.map((target) => ({
  id: `${target.os}-${target.arch}`,
  label: target.label,
  os: target.os,
  arch: target.arch,
}));

export function InstallCommands() {
  const [selected, setSelected] = useState(options[0]?.id ?? "linux-amd64");
  const current = options.find((option) => option.id === selected) ?? options[0];
  if (!current) {
    return null;
  }
  const command = installCommand(current.os, current.arch);

  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card">
      <div
        role="tablist"
        aria-label="Installation platform"
        className="flex flex-wrap gap-1 border-b border-border bg-muted/40 px-2 py-2"
      >
        {options.map((option) => (
          <button
            key={option.id}
            type="button"
            role="tab"
            aria-selected={option.id === selected}
            onClick={() => setSelected(option.id)}
            className={cn(
              "rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
              option.id === selected
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {option.label}
          </button>
        ))}
      </div>
      <div className="relative">
        <div className="absolute right-3 top-3 z-10">
          <CopyButton value={command} />
        </div>
        <pre className="overflow-x-auto p-4 pt-10 text-xs leading-relaxed text-muted-foreground">
          <code>{command}</code>
        </pre>
      </div>
    </div>
  );
}
