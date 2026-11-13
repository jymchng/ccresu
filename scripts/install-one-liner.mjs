#!/usr/bin/env node
//
// Prints one documented installation command, exactly as the site renders it.
//
// The workflow `.github/workflows/install-commands.yml` runs the output on Linux,
// macOS and Windows so the one-liners on the installation page are proven, not
// merely published. Nothing is re-implemented here: the strings come from
// web/lib/install-commands.json, which the site also renders, and
// web/tests/install-commands.test.tsx asserts the two renderers agree byte for
// byte.
//
// Usage:
//   node scripts/install-one-liner.mjs <name> [--version X]
//   node scripts/install-one-liner.mjs --list
//
// Names: unix, unixPreview, unixPinned, windows, windowsPinned, go, source.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const specPath = join(here, "..", "web", "lib", "install-commands.json");
const spec = JSON.parse(readFileSync(specPath, "utf8"));

/** The fallback release; the site passes the version it resolved instead. */
const FALLBACK_VERSION = "0.6.0";

function fail(message) {
  process.stderr.write(`install-one-liner: ${message}\n`);
  process.exit(2);
}

/**
 * Render one command.
 *
 * Kept deliberately trivial and aligned with `renderInstallCommand` in
 * `web/lib/install-commands.ts`: substitute every `{var}` from `spec.vars`, then
 * `{version}`.
 */
function render(name, version) {
  let command = spec.commands[name];
  if (command === undefined) {
    fail(`unknown command ${JSON.stringify(name)} (try --list)`);
  }
  for (const [key, value] of Object.entries(spec.vars)) {
    command = command.replaceAll(`{${key}}`, value);
  }
  return command.replaceAll("{version}", version);
}

const args = process.argv.slice(2);
if (args.length === 0 || args[0] === "--help" || args[0] === "-h") {
  process.stderr.write(
    "usage: install-one-liner.mjs <name> [--version X]\n" +
      `names: ${Object.keys(spec.commands).join(", ")}\n`,
  );
  process.exit(args.length === 0 ? 2 : 0);
}

if (args[0] === "--list") {
  process.stdout.write(`${Object.keys(spec.commands).join("\n")}\n`);
  process.exit(0);
}

let version = FALLBACK_VERSION;
const name = args[0];
for (let i = 1; i < args.length; i += 1) {
  if (args[i] === "--version" && args[i + 1] !== undefined) {
    version = args[i + 1];
    i += 1;
    continue;
  }
  fail(`unexpected argument ${JSON.stringify(args[i])}`);
}

const command = render(name, version);

// A one-liner is the whole contract: a newline here means somebody pasted half a
// script into the page, which is exactly what CI is meant to catch.
if (command.includes("\n")) {
  fail(`${name} renders to more than one line`);
}

process.stdout.write(command);
