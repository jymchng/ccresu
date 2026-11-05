import { CaptureFigure } from "@/components/capture-figure";
import { CopyButton } from "@/components/copy-button";
import { DocDiagram } from "@/components/doc-diagrams";
import { getCapture } from "@/lib/captures";
import type { Block } from "@/lib/docs/types";
import { cn } from "@/lib/utils";

/**
 * Renders `backticked` spans as inline code. Deliberately tiny: the docs are
 * data, and this is the only place that interprets them.
 *
 * Inline code gets `overflow-wrap: anywhere`, because the docs are full of URLs
 * and file paths with no spaces in them: one unbreakable token is enough to make
 * the whole page wider than a 320px phone.
 */
export function InlineText({ text }: { text: string }) {
  const parts = text.split("`");
  return (
    <>
      {parts.map((part, index) =>
        index % 2 === 1 ? (
          <code
            key={index}
            className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em] text-foreground [overflow-wrap:anywhere]"
          >
            {part}
          </code>
        ) : (
          <span key={index} className="[overflow-wrap:anywhere]">
            {part}
          </span>
        ),
      )}
    </>
  );
}

export function CodeBlock({ code, label }: { code: string; label?: string }) {
  return (
    <figure className="overflow-hidden rounded-xl border border-border bg-card">
      {label ? (
        <figcaption className="flex items-center justify-between gap-3 border-b border-border bg-muted/40 px-4 py-2 text-xs font-medium text-muted-foreground">
          <span>{label}</span>
          <CopyButton value={code} />
        </figcaption>
      ) : null}
      <pre className="overflow-x-auto p-4 text-xs leading-relaxed">
        <code className={cn("font-mono", label ? "text-muted-foreground" : "text-foreground")}>
          {code}
        </code>
      </pre>
    </figure>
  );
}

/** Renders one documentation page body from its typed block list. */
export function DocBlocks({ blocks }: { blocks: readonly Block[] }) {
  return (
    <div className="flex flex-col gap-5">
      {blocks.map((block, index) => {
        switch (block.kind) {
          case "h2":
            return (
              <h2
                key={index}
                className="mt-4 scroll-mt-24 text-xl font-semibold tracking-tight"
              >
                {block.text}
              </h2>
            );
          case "p":
            return (
              <p key={index} className="text-sm leading-relaxed text-muted-foreground">
                <InlineText text={block.text} />
              </p>
            );
          case "ul":
            return (
              <ul key={index} className="flex flex-col gap-2 pl-5">
                {block.items.map((item, itemIndex) => (
                  <li
                    key={itemIndex}
                    className="list-disc text-sm leading-relaxed text-muted-foreground marker:text-border"
                  >
                    <InlineText text={item} />
                  </li>
                ))}
              </ul>
            );
          case "ol":
            return (
              <ol key={index} className="flex flex-col gap-2 pl-5">
                {block.items.map((item, itemIndex) => (
                  <li
                    key={itemIndex}
                    className="list-decimal text-sm leading-relaxed text-muted-foreground marker:text-border"
                  >
                    <InlineText text={item} />
                  </li>
                ))}
              </ol>
            );
          case "code":
            return (
              <CodeBlock key={index} code={block.code} label={block.label} />
            );
          case "capture": {
            // Real output only: the id is typed, and an id that somehow does not
            // resolve renders nothing rather than an empty, misleading box.
            const capture = getCapture(block.id);
            if (!capture) {
              return null;
            }
            return <CaptureFigure key={index} capture={capture} />;
          }
          case "diagram":
            return (
              <DocDiagram
                key={index}
                id={block.id}
                label={block.label}
                description={block.description}
              />
            );
          case "note":
            return (
              <aside
                key={index}
                className={cn(
                  "rounded-xl border p-4 text-sm",
                  block.tone === "warning"
                    ? "border-amber-500/30 bg-amber-500/5"
                    : "border-primary/30 bg-primary/5",
                )}
              >
                <p className="mb-1 font-semibold">{block.title}</p>
                <p className="leading-relaxed text-muted-foreground">
                  <InlineText text={block.text} />
                </p>
              </aside>
            );
          case "table":
            return (
              <div key={index} className="flex flex-col gap-1">
                {/*
                  A table wider than a phone has to scroll inside its own box —
                  never widen the page. It is a labelled, focusable scroll
                  region so a keyboard user can reach it and a screen reader
                  says what it is, and the hint below is only shown where the
                  table actually has to scroll (`sm:` upwards the columns fit).
                */}
                <div
                  role="region"
                  aria-label={block.head.join(", ")}
                  tabIndex={0}
                  className="overflow-x-auto rounded-xl border border-border [scrollbar-width:thin] focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <table className="w-full border-collapse text-left text-sm">
                    <thead className="bg-muted/40">
                      <tr>
                        {block.head.map((cell) => (
                          <th
                            key={cell}
                            scope="col"
                            className="px-3 py-2.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground sm:whitespace-nowrap sm:px-4"
                          >
                            {cell}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {block.rows.map((row, rowIndex) => (
                        <tr key={rowIndex} className="border-t border-border align-top">
                          {row.map((cell, cellIndex) => (
                            <td
                              key={cellIndex}
                              className={cn(
                                "px-3 py-2.5 sm:px-4",
                                cellIndex === 0
                                  ? "font-medium text-foreground"
                                  : "text-muted-foreground",
                              )}
                            >
                              <InlineText text={cell} />
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                <p className="text-xs text-muted-foreground sm:hidden">
                  Swipe the table sideways to see every column.
                </p>
              </div>
            );
          default:
            return null;
        }
      })}
    </div>
  );
}
