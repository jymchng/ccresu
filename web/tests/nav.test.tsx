import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { SiteFooter } from "@/components/site-footer";
import { SiteHeader } from "@/components/site-header";
import { docs } from "@/lib/docs";
import { repo } from "@/lib/site";
import { normalizeHref } from "./helpers";

/** Every internal route the export actually builds. */
const knownRoutes = new Set([
  "/",
  "/docs",
  ...docs.map((doc) => `/docs/${doc.slug}`),
]);

function internalHrefs(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll("a"))
    .map((anchor) => anchor.getAttribute("href") ?? "")
    .filter((href) => href.startsWith("/"))
    .map(normalizeHref);
}

describe("navigation", () => {
  it("only links to routes the export builds", () => {
    const { container } = render(<SiteHeader />);
    const hrefs = internalHrefs(container);

    expect(hrefs.length).toBeGreaterThan(0);
    for (const href of hrefs) {
      expect(knownRoutes.has(href)).toBe(true);
    }
  });

  it("links every documentation page in the footer", () => {
    const { container } = render(<SiteFooter />);
    const hrefs = internalHrefs(container);

    for (const href of hrefs) {
      expect(knownRoutes.has(href)).toBe(true);
    }

    for (const doc of docs) {
      expect(hrefs).toContain(`/docs/${doc.slug}`);
    }
  });

  it("points external links at the real repository", () => {
    const { container } = render(<SiteFooter />);
    const external = Array.from(container.querySelectorAll("a"))
      .map((anchor) => anchor.getAttribute("href") ?? "")
      .filter((href) => href.startsWith("http"));

    expect(external.length).toBeGreaterThan(0);
    for (const href of external) {
      expect(href.startsWith(repo.url)).toBe(true);
    }
  });

  it("exposes a labelled main navigation and a theme switch", () => {
    render(<SiteHeader />);

    expect(
      screen.getByRole("navigation", { name: "Main" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Switch to (light|dark) theme/ }),
    ).toBeInTheDocument();
  });
});
