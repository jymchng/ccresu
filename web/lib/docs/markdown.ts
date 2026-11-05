import { captures } from "@/lib/captures";
import { absoluteUrl } from "@/lib/seo";
import type { Block, DocPage } from "./types";

/**
 * Documentation pages as Markdown.
 *
 * Every page is already data (see `./types.ts`), so the same content that the
 * renderer turns into HTML can be turned into Markdown instead. That matters for
 * the reader who wants the page somewhere else: in a note, in a prompt, in a
 * pull request, in an editor with no network. "Copy the page" is the request;
 * scraping rendered HTML produces something nobody wants to paste.
 *
 * The rules are deliberately literal:
 *
 *  - the page becomes `# title`, the summary as a blockquote, then the blocks in
 *    the order the page declares them, so the Markdown and the site cannot
 *    disagree about structure;
 *  - a `capture` is a real command and its real output, so it becomes a fenced
 *    block with the command above it — a reader can run it again;
 *  - a `diagram` is hand-drawn SVG, which has no Markdown equivalent. It is
 *    written as a captioned pointer to the page instead of being dropped, so no
 *    information silently disappears from the copy.
 *
 * Prose is passed through unchanged: the content already uses inline Markdown
 * (`code spans`, links) and re-escaping it would corrupt it.
 */

/** Characters that end a fenced block inside this content, if any. */
const FENCE = "```";

/**
 * Pick a fence long enough for the body.
 *
 * A capture of `ccresu --help` cannot contain a fence, but a documentation page
 * about Markdown could, and an unescaped fence would truncate the copy. Growing
 * the fence is the fix that keeps the output valid Markdown.
 */
function fenceFor(body: string): string {
  let fence = FENCE;
  while (body.includes(fence)) {
    fence += "`";
  }
  return fence;
}

/** A table cell may not contain a raw pipe or a newline. */
function cell(text: string): string {
  return text.replace(/\|/g, "\\|").replace(/\r?\n/g, " ");
}

function blockToMarkdown(block: Block): string {
  switch (block.kind) {
    case "p":
      return block.text;

    case "h2":
      return `## ${block.text}`;

    case "ul":
      return block.items.map((item) => `- ${item}`).join("\n");

    case "ol":
      return block.items.map((item, index) => `${index + 1}. ${item}`).join("\n");

    case "code": {
      const fence = fenceFor(block.code);
      const label = block.label ? `${block.label}:\n\n` : "";
      return `${label}${fence}sh\n${block.code}\n${fence}`;
    }

    case "note": {
      // A blockquote keeps the warning attached to the paragraph it qualifies,
      // and every Markdown renderer agrees on what it means.
      const tone = block.tone === "warning" ? "Warning" : "Note";
      const quoted = block.text
        .split(/\r?\n/)
        .map((line) => (line.length > 0 ? `> ${line}` : ">"))
        .join("\n");
      return `> **${tone}: ${block.title}**\n>\n${quoted}`;
    }

    case "table": {
      const head = `| ${block.head.map(cell).join(" | ")} |`;
      const divider = `| ${block.head.map(() => "---").join(" | ")} |`;
      const rows = block.rows.map((row) => `| ${row.map(cell).join(" | ")} |`);
      return [head, divider, ...rows].join("\n");
    }

    case "capture": {
      const capture = captures[block.id];
      const fence = fenceFor(capture.output);
      return [
        `**${capture.label}** (from \`${capture.command}\`)`,
        "",
        `${fence}console`,
        capture.output.replace(/\n+$/, ""),
        fence,
        "",
        capture.description,
      ].join("\n");
    }

    case "diagram": {
      const pointer = absoluteUrl(`/docs/`);
      return [
        `**Figure: ${block.label}**`,
        "",
        block.description,
        "",
        `(An SVG diagram on the page; it is not reproduced in text. See ${pointer}.)`,
      ].join("\n");
    }
  }
}

/**
 * One documentation page as a Markdown document.
 *
 * `url` is the canonical address of the page; it goes into the trailing "source"
 * line so a pasted page still says where it came from.
 */
export function pageToMarkdown(doc: DocPage, url: string = absoluteUrl(`/docs/${doc.slug}/`)): string {
  const parts = [`# ${doc.title}`, `> ${doc.summary}`];

  for (const block of doc.blocks) {
    parts.push(blockToMarkdown(block));
  }

  parts.push(`Source: ${url}`);

  return `${parts.join("\n\n")}\n`;
}

/**
 * The documentation index as a Markdown document.
 *
 * Kept separate from `pageToMarkdown` because the index is a list of links
 * rather than a page with blocks, and pretending otherwise would put the list
 * through a serialiser that knows nothing about it.
 */
export function indexToMarkdown(
  pages: readonly DocPage[],
  url: string = absoluteUrl("/docs/"),
): string {
  const links = pages.map(
    (doc) => `- [${doc.title}](${absoluteUrl(`/docs/${doc.slug}/`)}) — ${doc.summary}`,
  );
  return [
    "# Documentation",
    "> Everything ccresu does, in the order you need it.",
    links.join("\n"),
    `Source: ${url}`,
  ].join("\n\n").concat("\n");
}
