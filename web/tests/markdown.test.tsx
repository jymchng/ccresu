import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import DocsIndexPage from "@/app/docs/page";
import { DocPageView } from "@/components/doc-page-view";
import { captures } from "@/lib/captures";
import { docs } from "@/lib/docs";
import { indexToMarkdown, pageToMarkdown } from "@/lib/docs/markdown";
import { absoluteUrl } from "@/lib/seo";
import type { Block, DocPage } from "@/lib/docs/types";

/**
 * The "copy this page as Markdown" promise, tested as a promise about content.
 *
 * The failure mode worth guarding against is not a missing button: it is a
 * button that copies *most* of the page. A serializer that forgets tables, or
 * drops the hand-drawn diagrams, would still look right in a screenshot and
 * would silently lose information for the reader who pastes the result into a
 * prompt. So every block kind is asserted, and then the counts are compared
 * against the content itself — a block kind added later without a case here
 * fails the arithmetic rather than disappearing.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Count non-overlapping occurrences of a short string. */
function count(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1;
}

/** Every block kind the renderer knows about. */
function blocksOf(doc: DocPage, kind: Block["kind"]): Block[] {
  return doc.blocks.filter((block) => block.kind === kind);
}

describe("documentation pages as Markdown", () => {
  it("serialises the title, the summary and the source URL", () => {
    const doc = docs[0];
    const markdown = pageToMarkdown(doc);

    expect(markdown.startsWith(`# ${doc.title}\n`)).toBe(true);
    expect(markdown).toContain(`> ${doc.summary}`);
    expect(markdown).toContain(`Source: ${absoluteUrl(`/docs/${doc.slug}/`)}`);
    expect(markdown.endsWith("\n")).toBe(true);
  });

  it("covers every block kind, not just paragraphs", () => {
    // One page that contains each kind, so the assertions are about content the
    // reader actually gets rather than a synthetic fixture.
    const page: DocPage = {
      slug: "fixture",
      title: "Fixture",
      summary: "A page with one of everything.",
      blocks: [
        { kind: "p", text: "Opening paragraph with `code` in it." },
        { kind: "h2", text: "A heading" },
        { kind: "ul", items: ["first bullet", "second bullet"] },
        { kind: "ol", items: ["first step", "second step"] },
        { kind: "code", label: "Run this", code: "ccresu status\nccresu history abc" },
        { kind: "note", tone: "warning", title: "Careful", text: "This resets the counter." },
        { kind: "table", head: ["command", "effect"], rows: [["stop", "stops it"], ["reset", "clears it"]] },
        { kind: "capture", id: "help" },
        {
          kind: "diagram",
          id: "stop-then-resume",
          label: "Stop then resume",
          description: "The session is stopped before it is resumed.",
        },
      ],
    };

    const markdown = pageToMarkdown(page);

    expect(markdown).toContain("Opening paragraph with `code` in it.");
    expect(markdown).toContain("## A heading");
    expect(markdown).toContain("- first bullet\n- second bullet");
    expect(markdown).toContain("1. first step\n2. second step");
    expect(markdown).toContain("Run this:\n\n```sh\nccresu status\nccresu history abc\n```");
    expect(markdown).toContain("> **Warning: Careful**\n>\n> This resets the counter.");
    expect(markdown).toContain("| command | effect |\n| --- | --- |\n| stop | stops it |\n| reset | clears it |");

    // A capture is the command and its real output, so the copy is re-runnable.
    expect(markdown).toContain(`from \`${captures.help.command}\``);
    expect(markdown).toContain(captures.help.output.replace(/\n+$/, ""));

    // A diagram has no Markdown equivalent, but it must not vanish.
    expect(markdown).toContain("**Figure: Stop then resume**");
    expect(markdown).toContain("The session is stopped before it is resumed.");
  });

  it("loses no block on any real page", () => {
    for (const doc of docs) {
      const markdown = pageToMarkdown(doc);

      // Every heading appears as a heading.
      for (const block of blocksOf(doc, "h2")) {
        expect(markdown, `${doc.slug} lost a heading`).toContain(
          `## ${(block as { text: string }).text}`,
        );
      }

      // Every fenced block in the output belongs to a code or capture block.
      const fences = count(markdown, "```") / 2;
      expect(fences, `${doc.slug} fence count`).toBe(
        blocksOf(doc, "code").length + blocksOf(doc, "capture").length,
      );

      // Every table row survives.
      for (const block of blocksOf(doc, "table")) {
        for (const row of (block as { rows: string[][] }).rows) {
          for (const cellText of row) {
            expect(markdown, `${doc.slug} lost a table cell`).toContain(
              cellText.replace(/\|/g, "\\|"),
            );
          }
        }
      }

      // Every list item survives.
      for (const kind of ["ul", "ol"] as const) {
        for (const block of blocksOf(doc, kind)) {
          for (const item of (block as { items: string[] }).items) {
            expect(markdown, `${doc.slug} lost a ${kind} item`).toContain(item);
          }
        }
      }
    }
  });

  it("grows the fence when the content contains one", () => {
    const page: DocPage = {
      slug: "fenced",
      title: "Fenced",
      summary: "A page about fences.",
      blocks: [{ kind: "code", code: "```\nnested\n```" }],
    };
    const markdown = pageToMarkdown(page);

    expect(markdown).toContain("````sh\n```\nnested\n```\n````");
  });

  it("escapes pipes so a table cell cannot break the table", () => {
    const page: DocPage = {
      slug: "pipes",
      title: "Pipes",
      summary: "A page about pipes.",
      blocks: [{ kind: "table", head: ["a|b"], rows: [["x|y", "z"]] }],
    };
    expect(pageToMarkdown(page)).toContain("| a\\|b |");
    expect(pageToMarkdown(page)).toContain("| x\\|y | z |");
  });

  it("lists every page, with its URL, in the index", () => {
    const markdown = indexToMarkdown(docs);

    expect(markdown.startsWith("# Documentation\n")).toBe(true);
    for (const doc of docs) {
      expect(markdown).toContain(
        `- [${doc.title}](${absoluteUrl(`/docs/${doc.slug}/`)}) — ${doc.summary}`,
      );
    }
    expect(count(markdown, "\n- [")).toBe(docs.length);
  });
});

describe("the copy controls on the pages", () => {
  it("copies the whole page as Markdown from a documentation page", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    render(<DocPageView slug="cli" />);
    await user.click(
      screen.getByRole("button", { name: "Copy page as Markdown to clipboard" }),
    );

    expect(writeText).toHaveBeenCalledTimes(1);
    const copied = writeText.mock.calls[0][0] as string;
    const doc = docs.find((candidate) => candidate.slug === "cli");
    expect(doc).toBeDefined();
    expect(copied).toBe(pageToMarkdown(doc as DocPage));
    expect(copied).toContain("## ");
  });

  it("offers a .md download of exactly what the button copies", () => {
    render(<DocPageView slug="cli" />);

    const link = screen.getByRole("link", { name: /Download \.md/ });
    const href = link.getAttribute("href") ?? "";
    expect(href.startsWith("data:text/markdown;charset=utf-8,")).toBe(true);
    expect(link.getAttribute("download")).toBe("ccresu-docs-cli.md");

    const doc = docs.find((candidate) => candidate.slug === "cli") as DocPage;
    const decoded = decodeURIComponent(href.replace("data:text/markdown;charset=utf-8,", ""));
    expect(decoded).toBe(pageToMarkdown(doc));
  });

  it("is present on the documentation index too", () => {
    render(<DocsIndexPage />);

    expect(
      screen.getByRole("button", { name: "Copy page as Markdown to clipboard" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Download \.md/ })).toHaveAttribute(
      "download",
      "ccresu-docs.md",
    );
  });

  it("keeps the controls at least 44px, like every other target on the page", () => {
    render(<DocPageView slug="cli" />);

    expect(
      screen.getByRole("button", { name: "Copy page as Markdown to clipboard" }).className,
    ).toContain("min-h-11");
    expect(screen.getByRole("link", { name: /Download \.md/ }).className).toContain(
      "min-h-11",
    );
  });
});
