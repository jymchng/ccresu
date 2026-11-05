import { existsSync, readFileSync } from "node:fs";
import path from "node:path";

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import AboutPage from "@/app/about/page";
import PrivacyPage from "@/app/privacy/page";
import TermsPage from "@/app/terms/page";
import { DocsNav } from "@/components/docs-nav";
import { SiteFooter } from "@/components/site-footer";
import { docs } from "@/lib/docs";
import { aboutPage, privacyPage, staticPages, termsPage } from "@/lib/static-pages";
import { normalizeHref } from "./helpers";

const webRoot = path.resolve(import.meta.dirname, "..");
const repoRoot = path.resolve(webRoot, "..");

const pages = [
  { page: aboutPage, Component: AboutPage },
  { page: termsPage, Component: TermsPage },
  { page: privacyPage, Component: PrivacyPage },
] as const;

/** All the prose on one page, for claims that must appear somewhere in it. */
function pageText(page: (typeof pages)[number]["page"]): string {
  return [
    page.title,
    page.summary,
    ...page.sections.flatMap((section) => [
      section.heading,
      ...(section.paragraphs ?? []),
      ...(section.bullets ?? []),
    ]),
  ].join("\n");
}

describe("static pages", () => {
  it("registers About, Terms and Privacy, and nothing else", () => {
    expect(staticPages.map((page) => page.href)).toEqual([
      "/about",
      "/terms",
      "/privacy",
    ]);
    expect(staticPages.map((page) => page.slug)).toEqual([
      "about",
      "terms",
      "privacy",
    ]);
  });

  it("keeps them out of the documentation navigation", () => {
    const docSlugs = docs.map((doc) => doc.slug);
    for (const page of staticPages) {
      expect(docSlugs).not.toContain(page.slug);

      const { container, unmount } = render(<DocsNav activeSlug="" />);
      const hrefs = Array.from(container.querySelectorAll("a")).map(
        (anchor) => normalizeHref(anchor.getAttribute("href") ?? ""),
      );
      expect(hrefs).not.toContain(page.href);
      unmount();
    }
  });

  it("has a page component for every route the export builds", () => {
    for (const { page } of pages) {
      // The route only exists in the export if Next finds a page module for it.
      const source = path.join(webRoot, "app", page.slug, "page.tsx");
      expect(existsSync(source), `${page.slug} page module`).toBe(true);

      // When the site has been built, the route must also be a real file:
      // `trailingSlash` turns `/about` into `out/about/index.html`.
      const built = path.join(webRoot, "out", page.slug, "index.html");
      if (existsSync(path.join(webRoot, "out"))) {
        expect(existsSync(built), `${page.slug}/index.html`).toBe(true);
      }
    }
  });

  it("renders a top-level heading and substantive content on each page", () => {
    for (const { page, Component } of pages) {
      const { unmount } = render(<Component />);

      expect(
        screen.getByRole("heading", { level: 1, name: page.title }),
      ).toBeInTheDocument();
      expect(screen.getByText(page.summary)).toBeInTheDocument();

      // "Substantive" as a number a test can hold: several sections, each with
      // a heading and real prose, and a page a reader has to scroll.
      expect(page.sections.length).toBeGreaterThanOrEqual(4);
      const words = pageText(page).split(/\s+/).filter(Boolean).length;
      expect(words).toBeGreaterThan(250);

      for (const section of page.sections) {
        expect(
          screen.getByRole("heading", { level: 2, name: section.heading }),
        ).toBeInTheDocument();
        const body = [...(section.paragraphs ?? []), ...(section.bullets ?? [])];
        expect(body.length).toBeGreaterThan(0);
      }

      unmount();
    }
  });

  it("introduces the project and names its maintainer", () => {
    const text = pageText(aboutPage);

    expect(text).toContain("jymchng");
    expect(text).toContain("github.com/jymchng/ccresu");
    expect(text).toContain("MIT");
    expect(text).toMatch(/does not lift|does not fork|does not simulate/i);
  });

  it("states the licence and the no-warranty position the MIT text requires", () => {
    const text = pageText(termsPage);
    // The licence is hard-wrapped across lines; compare on collapsed whitespace
    // so the assertion is about the clause, not about where LICENSE wraps.
    const licence = readFileSync(path.join(repoRoot, "LICENSE"), "utf8").replace(
      /\s+/g,
      " ",
    );

    // The repository really is MIT with this copyright line; the page says so.
    expect(licence).toContain("MIT License");
    expect(licence).toContain("Copyright (c) 2026 jymchng");
    expect(text).toContain("MIT License");
    expect(text).toContain("copyright (c) 2026 jymchng");

    // The phrases the licence uses, restated rather than replaced.
    expect(licence).toContain('PROVIDED "AS IS"');
    expect(text.toLowerCase()).toContain("provided \"as is\"");
    expect(text).toContain("merchantability");
    expect(text).toContain("fitness for a particular purpose");
    expect(licence).toContain("IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE");
    expect(text.toLowerCase()).toMatch(
      /in no event shall the authors or copyright holders be liable/,
    );

    // and nothing that would grant a warranty the licence disclaims.
    expect(text.toLowerCase()).not.toContain("warrants that");
    expect(text.toLowerCase()).not.toContain("guarantees that the software will");
  });

  it("denies liability for damages, in plain language, on the page itself", () => {
    render(<TermsPage />);

    const section = screen.getByRole("heading", { level: 2, name: "No liability" })
      .closest("section");
    expect(section).not.toBeNull();

    const liability = within(section as HTMLElement).getByText(
      /not liable for any damages of any kind/i,
    );
    expect(liability).toBeInTheDocument();

    for (const damage of [
      /lost work/i,
      /missed, delayed/i,
      /usage-limit overruns/i,
      /local SQLite database/i,
      /autonomous resume does on your behalf/i,
    ]) {
      expect(within(section as HTMLElement).getByText(damage)).toBeInTheDocument();
    }

    // Use is the acceptance, and it cannot be withdrawn afterwards.
    expect(
      screen.getByRole("heading", { level: 2, name: "Acceptance" }),
    ).toBeInTheDocument();
    expect(pageText(termsPage)).toMatch(/cannot withdraw the disclaimer/i);
  });

  it("describes the real data flow, not a generic privacy template", () => {
    const text = pageText(privacyPage);

    expect(text).toContain("~/.local/share/ccresu/ccresu.db");
    expect(text).toContain("~/.config/ccresu/config.yaml");
    expect(text).toContain("0600");
    expect(text).toContain("api.telegram.org");
    expect(text).toMatch(/no telemetry/i);
    expect(text).toContain("no crash reporting");
    expect(text).toMatch(/127\.0\.0\.1/);

    // The site: static files, no analytics, and the one build-time API call.
    expect(text).toContain("GitHub Pages");
    expect(text).toMatch(/no analytics/i);
    expect(text).toContain("localStorage");
    expect(text).toContain(
      "https://api.github.com/repos/jymchng/ccresu/releases/latest",
    );
    expect(text).toMatch(/build-time/i);

    // Claims that would be false: the site sets no cookie, ships no analytics
    // vendor, and makes no client-side request to GitHub.
    expect(text).not.toMatch(/uses cookies|analytics provider|google analytics/i);
    expect(text).not.toMatch(/we collect your (email|name|ip)/i);

    // Telegram really is the only outbound host in the binary.
    const telegramClient = readFileSync(
      path.join(repoRoot, "internal", "telegram", "client.go"),
      "utf8",
    );
    expect(telegramClient).toContain("https://api.telegram.org");
  });

  it("contains no placeholder copy", () => {
    const text = staticPages.map(pageText).join("\n").toLowerCase();

    for (const placeholder of ["lorem ipsum", "todo", "tbd", "example.com", "coming soon"]) {
      expect(text).not.toContain(placeholder);
    }
  });

  it("links all three pages from the footer and nowhere else", () => {
    const { container } = render(<SiteFooter />);
    const hrefs = Array.from(container.querySelectorAll("a")).map((anchor) =>
      normalizeHref(anchor.getAttribute("href") ?? ""),
    );

    for (const page of staticPages) {
      expect(hrefs).toContain(page.href);
    }

    // Every internal footer href must be a route the export actually builds:
    // either a docs page, a static page, or the index of one of the two.
    const known = new Set<string>([
      "/",
      "/docs",
      ...docs.map((doc) => `/docs/${doc.slug}`),
      ...staticPages.map((page) => page.href),
    ]);
    for (const href of hrefs.filter((value) => value.startsWith("/"))) {
      expect(known.has(href), href).toBe(true);
    }
  });

  it("gives every footer and related link a 44px target in both directions", () => {
    // The browser gate caught this once: `min-h-11` alone leaves a 41px-wide
    // "About" link, which is a thumb miss. Both dimensions are asserted here so
    // the regression fails in jsdom as well as in Chromium.
    const { container: footer } = render(<SiteFooter />);
    for (const anchor of Array.from(footer.querySelectorAll("a"))) {
      expect(anchor.className, anchor.textContent ?? "").toMatch(/\bmin-h-11\b/);
      expect(anchor.className, anchor.textContent ?? "").toMatch(/\bmin-w-11\b/);
    }

    const { container: page } = render(<PrivacyPage />);
    for (const anchor of Array.from(page.querySelectorAll("a"))) {
      expect(anchor.className, anchor.textContent ?? "").toMatch(/\bmin-h-11\b/);
      expect(anchor.className, anchor.textContent ?? "").toMatch(/\bmin-w-11\b/);
    }
  });
});
