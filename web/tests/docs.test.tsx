import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import DocsIndexPage from "@/app/docs/page";
import { DocPageView } from "@/components/doc-page-view";
import { docs, getDoc } from "@/lib/docs";
import { normalizeHref } from "./helpers";

describe("documentation", () => {
  it("registers the nine required pages with unique slugs", () => {
    expect(docs).toHaveLength(9);
    const slugs = docs.map((doc) => doc.slug);
    expect(new Set(slugs).size).toBe(slugs.length);
    expect(slugs).toEqual([
      "installation",
      "configuration",
      "cli",
      "recovery",
      "telegram",
      "deployment",
      "security",
      "troubleshooting",
      "limitations",
    ]);
  });

  it("renders every page with real content and a heading", () => {
    for (const doc of docs) {
      const { unmount } = render(<DocPageView slug={doc.slug} />);
      expect(
        screen.getByRole("heading", { level: 1, name: doc.title }),
      ).toBeInTheDocument();
      expect(screen.getByText(doc.summary)).toBeInTheDocument();
      unmount();
    }
  });

  it("gives every block a renderable shape", () => {
    for (const doc of docs) {
      expect(doc.blocks.length).toBeGreaterThan(6);
      for (const block of doc.blocks) {
        switch (block.kind) {
          case "p":
          case "h2":
            expect(block.text.length).toBeGreaterThan(0);
            break;
          case "ul":
          case "ol":
            expect(block.items.length).toBeGreaterThan(0);
            break;
          case "code":
            expect(block.code.length).toBeGreaterThan(0);
            break;
          case "note":
            expect(block.title.length).toBeGreaterThan(0);
            expect(block.text.length).toBeGreaterThan(0);
            break;
          case "table":
            expect(block.head.length).toBeGreaterThan(0);
            for (const row of block.rows) {
              expect(row).toHaveLength(block.head.length);
            }
            break;
        }
      }
    }
  });

  it("returns undefined for an unknown slug instead of inventing a page", () => {
    expect(getDoc("does-not-exist")).toBeUndefined();
    expect(() => DocPageView({ slug: "does-not-exist" })).toThrow();
  });

  it("indexes every page and links to each one once", () => {
    const { container } = render(<DocsIndexPage />);
    const list = container.querySelector("ul");
    expect(list).not.toBeNull();

    for (const doc of docs) {
      const links = within(list as HTMLElement)
        .getAllByRole("link")
        .filter(
          (link) =>
            normalizeHref(link.getAttribute("href") ?? "") === `/docs/${doc.slug}`,
        );
      expect(links).toHaveLength(1);
      expect(links[0]).toHaveTextContent(doc.title);
    }
  });

  it("links to the neighbouring pages from a doc page", () => {
    render(<DocPageView slug="cli" />);

    const hrefs = screen
      .getAllByRole("link")
      .map((link) => normalizeHref(link.getAttribute("href") ?? ""));

    expect(hrefs).toContain("/docs/configuration");
    expect(hrefs).toContain("/docs/recovery");
  });
});
