"use client";

import { Check, Copy } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";

/**
 * Copy-to-clipboard control with three honest states: idle, copied and failed.
 *
 * `navigator.clipboard` is unavailable in insecure contexts and can be blocked
 * by permissions, so a failure is surfaced instead of pretending the copy
 * worked.
 */
export function CopyButton({
  value,
  label = "Copy",
  className,
}: {
  value: string;
  label?: string;
  className?: string;
}) {
  const [status, setStatus] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (timer.current) {
        clearTimeout(timer.current);
      }
    };
  }, []);

  async function copy() {
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error("clipboard unavailable");
      }
      await navigator.clipboard.writeText(value);
      setStatus("copied");
    } catch {
      setStatus("failed");
    }
    if (timer.current) {
      clearTimeout(timer.current);
    }
    timer.current = setTimeout(() => setStatus("idle"), 2000);
  }

  return (
    <button
      type="button"
      onClick={copy}
      className={cn(
        "inline-flex min-h-11 min-w-11 items-center justify-center gap-1.5 rounded-md border border-border bg-background/80 px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
        className,
      )}
      aria-label={`${label} to clipboard`}
    >
      {status === "copied" ? (
        <Check className="size-3.5 text-emerald-500" aria-hidden="true" />
      ) : (
        <Copy className="size-3.5" aria-hidden="true" />
      )}
      <span aria-live="polite">
        {status === "copied" ? "Copied" : status === "failed" ? "Copy failed" : label}
      </span>
    </button>
  );
}
