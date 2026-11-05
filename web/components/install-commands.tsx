"use client";

import { useState } from "react";

import { CopyButton } from "@/components/copy-button";
import { renderInstallCommand } from "@/lib/install-commands";
import { installPlatforms, type Platform } from "@/lib/site";
import { cn } from "@/lib/utils";

/**
 * The landing page's installation command.
 *
 * One line per platform, because that is what the documentation promises and
 * what the CI workflow executes: a command copied in halves is a broken install.
 * Both surfaces render the same strings from `lib/install-commands.json`, and
 * `renderInstallCommand` is the only thing that turns them into text.
 *
 * `version` is passed in, never baked in here: the page that renders this
 * component resolves the newest published release at build time
 * (`lib/release.ts`), so the pinned command on a new tag reaches the copy button
 * on the next deploy without anybody editing a constant.
 */
export function installCommand(os: Platform, version: string): string {
  return renderInstallCommand(os === "windows" ? "windowsPinned" : "unixPinned", version);
}

/**
 * The platform tabs come from `installPlatforms` in `lib/site.ts`.
 *
 * They are per operating system, not per architecture, because the installers
 * detect the architecture themselves. A "Linux ARM64" tab could only offer the
 * same line as the "Linux x86-64" one, which would be a promise the command does
 * not keep.
 */
const options = installPlatforms.map((platform) => ({
  id: platform.os,
  label: platform.label,
  note: platform.note,
  os: platform.os,
}));

export function InstallCommands({ version }: { version: string }) {
  const [selected, setSelected] = useState<Platform>(options[0]?.id ?? "linux");
  const current = options.find((option) => option.id === selected) ?? options[0];
  if (!current) {
    return null;
  }
  const command = installCommand(current.os, version);

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
            onClick={() => setSelected(option.os)}
            className={cn(
              "flex min-h-11 items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
              option.id === selected
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <span>{option.label}</span>
            <span className="text-[0.7em] text-muted-foreground">{option.note}</span>
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
