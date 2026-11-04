import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import NotFound from "@/app/not-found";
import { docs } from "@/lib/docs";
import { features, workflow } from "@/lib/landing";
import { exampleVersion, repo, site } from "@/lib/site";
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

  it("keeps the site on the GitHub Pages base path", () => {
    expect(site.basePath).toBe("/ccresu");
    expect(site.url).toBe(`https://${repo.owner}.github.io/${repo.name}`);
    expect(exampleVersion).toMatch(/^\d+\.\d+\.\d+$/);
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
