import { execFileSync } from "node:child_process";
import path from "node:path";

import { describe, expect, it } from "vitest";

import { installCommandNames, renderInstallCommand } from "@/lib/install-commands";
import { fallbackReleaseVersion } from "@/lib/site";

/**
 * The site and CI must agree about what a documented command is.
 *
 * CI cannot import TypeScript, so it renders the commands with
 * `scripts/install-one-liner.mjs`, which reads the same
 * `lib/install-commands.json` with its own (tiny) substitution. Two renderers is
 * one more than ideal, so this test makes them one in practice: if they ever
 * produce a different string, the command that CI proves working is not the
 * command the page shows, and the build fails here rather than shipping a page
 * nobody tested.
 */
const script = path.resolve(import.meta.dirname, "..", "..", "scripts", "install-one-liner.mjs");

function printWithNode(args: string[]): string {
  return execFileSync(process.execPath, [script, ...args], {
    encoding: "utf8",
  });
}

describe("install one-liner script", () => {
  it("prints exactly what the site renders, for every command", () => {
    for (const name of installCommandNames) {
      const fromNode = printWithNode([name, "--version", "9.9.9"]);
      const fromSite = renderInstallCommand(name, "9.9.9");
      expect(fromNode, name).toBe(fromSite);
    }
  });

  it("defaults to the same fallback version as the site", () => {
    expect(printWithNode(["unixPinned"])).toBe(
      renderInstallCommand("unixPinned", fallbackReleaseVersion),
    );
  });

  it("prints a single line with no trailing newline", () => {
    for (const name of installCommandNames) {
      const output = printWithNode([name]);
      expect(output, name).not.toContain("\n");
    }
  });

  it("lists the command names the site knows", () => {
    expect(printWithNode(["--list"]).trim().split("\n")).toEqual([
      ...installCommandNames,
    ]);
  });

  it("refuses an unknown command instead of printing nothing", () => {
    expect(() => printWithNode(["nope"])).toThrow();
  });
});
