import { CopyButton } from "@/components/copy-button";
import { captureSource, type Capture } from "@/lib/captures";
import { cn } from "@/lib/utils";

/**
 * A figure built from real command output.
 *
 * Nothing here is a picture: the text is the binary's own stdout, captured by
 * `scripts/web-captures.sh` into `lib/captures`. That has three consequences
 * worth stating, because each one is a deliberate choice:
 *
 *  - it stays sharp and selectable at any width, including 320px, instead of
 *    being a bitmap that either shrinks to unreadable or forces the page wide;
 *  - a screen reader reads the same words a reader sees, because the `<pre>` is
 *    ordinary text with an accessible name from the caption;
 *  - it cannot quietly drift, because `tests/captures.test.tsx` compares what
 *    this component renders with the captured text and with the fixtures on
 *    disk.
 *
 * Long lines (the call log, the `sessions` table) scroll horizontally inside the
 * figure; the page itself never does.
 */
export function CaptureFigure({
  capture,
  className,
}: {
  capture: Capture;
  className?: string;
}) {
  const labelId = `capture-${capture.id}-label`;
  const descriptionId = `capture-${capture.id}-description`;

  return (
    <figure
      aria-labelledby={labelId}
      aria-describedby={descriptionId}
      data-capture={capture.id}
      className={cn(
        "overflow-hidden rounded-xl border border-border bg-card",
        className,
      )}
    >
      <figcaption
        id={labelId}
        className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-muted/40 px-3 py-2 sm:px-4"
      >
        <span className="text-xs font-medium text-muted-foreground">
          {capture.label}
        </span>
        <span className="flex items-center gap-2">
          <code className="rounded bg-background/60 px-1.5 py-0.5 font-mono text-[0.7rem] text-muted-foreground">
            {capture.command}
          </code>
          <CopyButton value={capture.output} />
        </span>
      </figcaption>

      <pre className="max-w-full overflow-x-auto p-3 text-[0.7rem] leading-relaxed sm:p-4 sm:text-xs">
        <code className="font-mono">{capture.output}</code>
      </pre>

      <div className="flex flex-col gap-1 border-t border-border px-3 py-2 text-xs leading-relaxed text-muted-foreground sm:px-4">
        <p id={descriptionId}>{capture.description}</p>
        <p className="text-[0.7rem] text-muted-foreground/80">
          Captured from the binary at{" "}
          <code className="font-mono">{captureSource.commit}</code> on{" "}
          {captureSource.generated}; scratch paths and wall-clock columns are
          scrubbed, nothing else is edited.
        </p>
      </div>
    </figure>
  );
}
