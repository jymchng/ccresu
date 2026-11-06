import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import NotFound from "@/app/not-found";
import { docs, getDoc } from "@/lib/docs";
import { features, workflow } from "@/lib/landing";
import { fallbackReleaseVersion, repo, site } from "@/lib/site";
import { themeBootstrapScript, themeStorageKey } from "@/lib/theme";
import { normalizeHref } from "./helpers";

function allText(): string {
  const pages = docs.map((doc) => JSON.stringify(doc.blocks)).join("\n");
  const landing = [
    ...features.map((feature) => `${feature.title} ${feature.description} ${feature.detail}`),
    ...workflow.map((step) => `${step.title} ${step.command} ${step.description}`),
  ].join("\n");
  return `${pages}\n${landing}`;
}

const text = allText();

describe("content integrity", () => {
  it("contains no placeholder copy", () => {
    for (const placeholder of ["lorem ipsum", "TODO", "TBD", "example.com", "coming soon"]) {
      expect(text.toLowerCase()).not.toContain(placeholder.toLowerCase());
    }
  });

  it("documents the verified resume command, not a guessed one", () => {
    expect(text).toContain("claude -p --resume");
    expect(text).toContain("claude agents --json");
    expect(text).not.toContain("claude --resume-fork");
  });

  it("says the binary ships no web UI", () => {
    const joined = `${text} ${site.description}`.toLowerCase();
    expect(joined).toMatch(/no (embedded )?web ui/);
  });

  it("documents why the continuation message is the last argument", () => {
    const doc = getDoc("recovery");
    expect(doc).toBeDefined();
    const code = (doc?.blocks ?? [])
      .flatMap((block) => (block.kind === "code" ? [block.code] : []))
      .join("\n");

    expect(JSON.stringify(doc?.blocks)).toContain(
      "Why the message is the last argument",
    );
    // The vector that is executed, in that order, message last.
    expect(code).toContain(
      '"-p", "--resume", sessionID, "--output-format", "json", message',
    );
    // The audit preview must put the message where the real one goes: last.
    expect(code + JSON.stringify(doc?.blocks)).toContain(
      "--output-format json <N-byte continuation message>",
    );
    expect(code + JSON.stringify(doc?.blocks)).toContain("TestResumeArgsExact");
  });

  it("documents that a resume is never dispatched with --bg", () => {
    const doc = getDoc("recovery");
    const body = JSON.stringify(doc?.blocks ?? []);

    expect(body).toContain("Why the resume never asks for `--bg`");
    expect(body).toContain("--bg and --print conflict");
    expect(body).toContain("TestResumeArgsNeverPassBackground");
  });

  it("narrates the background-agent recovery and the operator reset", () => {
    const body = JSON.stringify(getDoc("recovery")?.blocks ?? []);

    expect(body).toContain("claude stop <job-id>");
    expect(body).toContain("DISCOVERED");
    expect(body).toContain("ccresu retry");
    expect(body).toContain("keeps the attempt row");
    // The reproduce-then-fix contrast is documented, not just the happy path.
    expect(body).toContain("--pre-fix");
    expect(body).toContain("00692e3");
  });

  it("keeps the site on the GitHub Pages base path", () => {
    expect(site.basePath).toBe("/ccresu");
    expect(site.url).toBe(`https://${repo.owner}.github.io/${repo.name}`);
    // The pinned fallback must itself be a version the asset naming accepts;
    // the version actually rendered comes from lib/release.ts at build time.
    expect(fallbackReleaseVersion).toMatch(/^\d+\.\d+\.\d+$/);
  });

  it("bootstraps the theme before paint and honours the stored value", () => {
    expect(themeBootstrapScript).toContain(themeStorageKey);
    expect(themeBootstrapScript).toContain("classList");
    expect(themeBootstrapScript).not.toContain("<script");
  });

  it("renders a usable 404 state", () => {
    render(<NotFound />);

    expect(
      screen.getByRole("heading", { level: 1, name: "That page does not exist" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Back to the landing page" }),
    ).toHaveAttribute("href", "/");
    expect(
      normalizeHref(
        screen.getByRole("link", { name: "Browse the documentation" }).getAttribute("href") ?? "",
      ),
    ).toBe("/docs");
  });
});
