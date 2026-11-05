"use client";

import { ChevronDown, ListTree } from "lucide-react";

import { DocsNav } from "@/components/docs-nav";
import { MobileDisclosure } from "@/components/mobile-disclosure";

/**
 * The documentation sidebar, reachable below `lg`.
 *
 * The sidebar itself is `hidden lg:block`: on a phone it would either crowd the
 * article or push it off the screen, so it used to simply not exist there — the
 * only way to another doc page was back through the index. This renders the same
 * `DocsNav` inside a collapsible panel, so every page stays one tap away at any
 * width without duplicating the list of pages.
 */
export function DocsMenu({ activeSlug }: { activeSlug: string }) {
  return (
    <div className="lg:hidden">
      <MobileDisclosure
        label="documentation menu"
        trigger={
          <>
            <span className="flex items-center gap-2">
              <ListTree className="size-4" aria-hidden="true" />
              Documentation menu
            </span>
            <ChevronDown className="size-4" aria-hidden="true" />
          </>
        }
        buttonClassName="flex min-h-11 w-full items-center justify-between gap-2 rounded-xl border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-accent"
        panelClassName="mt-2"
        innerClassName="rounded-xl border border-border bg-card p-2"
      >
        <DocsNav activeSlug={activeSlug} />
      </MobileDisclosure>
    </div>
  );
}
