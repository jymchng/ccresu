import spec from "./install-commands.json";

import { fallbackReleaseVersion } from "./site";

/**
 * The installation commands the site shows, rendered from
 * `install-commands.json`.
 *
 * The commands live in a JSON file rather than in this module for one reason:
 * CI has to run them. `.github/workflows/install-commands.yml` executes exactly
 * these strings on Linux, macOS and Windows through
 * `scripts/install-one-liner.mjs`, which reads the same file with no build step
 * and no TypeScript. A documented command that stops working therefore fails a
 * workflow run, not a reader.
 *
 * Two contracts keep the two readers honest, and both are tested:
 *
 *  - every command renders to a **single line** (a one-liner you can paste),
 *  - this renderer and the Node script produce byte-identical strings.
 *
 * The variables in `vars` are duplicated facts about the repository (its URL, the
 * raw URL of the installer). `web/tests/install-commands.test.tsx` asserts they
 * agree with `lib/site.ts`, so the duplication cannot drift silently.
 */
export type InstallCommandName = keyof typeof spec.commands;

/** Every command name, in the order the JSON lists them. */
export const installCommandNames = Object.keys(spec.commands) as InstallCommandName[];

/** The raw URL of one installer script, as documented. */
export const installScripts = {
  unix: spec.vars.installScript,
  windows: spec.vars.installScriptPs1,
} as const;

/**
 * Render one installation command.
 *
 * `{version}` defaults to the release the site resolved at build time, which is
 * why the pinned commands never hard-code a number. Placeholders that do not
 * appear in the chosen command are simply ignored.
 */
export function renderInstallCommand(
  name: InstallCommandName,
  version: string = fallbackReleaseVersion,
): string {
  let command: string = spec.commands[name];
  for (const [key, value] of Object.entries(spec.vars)) {
    command = command.replaceAll(`{${key}}`, value);
  }
  return command.replaceAll("{version}", version);
}
