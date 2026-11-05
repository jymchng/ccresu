"use client";

import { Download } from "lucide-react";

import { CopyButton } from "@/components/copy-button";

/**
 * "Take this page with you": copy it as Markdown, or download it as a file.
 *
 * Both controls come from the same string, so what lands in the clipboard and
 * what lands on disk cannot differ. The download is a `data:` URL rather than a
 * route: the site is a static export with no server to answer
 * `/docs/<slug>.md`, and generating one file per page at build time would be a
 * second copy of the content to keep honest. The Markdown is already in the
 * HTML at this point; the anchor just hands it over.
 *
 * `download` is advisory — a browser may open the file instead of saving it —
 * which is why the copy button exists beside it.
 */
export function CopyMarkdown({
  markdown,
  filename,
}: {
  /** The whole page as Markdown, exactly what the copy button writes. */
  markdown: string;
  /** Suggested file name, e.g. `ccresu-docs-cli.md`. */
  filename: string;
}) {
  const href = `data:text/markdown;charset=utf-8,${encodeURIComponent(markdown)}`;

  return (
    <div className="flex flex-wrap items-center gap-2">
      <CopyButton value={markdown} label="Copy page as Markdown" />
      <a
        href={href}
        download={filename}
        className="inline-flex min-h-11 min-w-11 items-center justify-center gap-1.5 rounded-md border border-border bg-background/80 px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        <Download className="size-3.5" aria-hidden="true" />
        Download .md
      </a>
    </div>
  );
}
