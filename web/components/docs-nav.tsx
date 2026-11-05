import Link from "next/link";

import { docs } from "@/lib/docs";
import { cn } from "@/lib/utils";

/**
 * Sidebar navigation for the documentation section.
 *
 * The active page is passed in rather than read from the router, so the
 * component stays a plain function that a test can render directly — and so the
 * same list can be shown twice: in the desktop sidebar and inside the mobile
 * docs menu.
 *
 * Rows are 44px tall (`min-h-11`) with the target as wide as the sidebar, which
 * is what makes them usable with a thumb on a phone as well as readable in a
 * 220px column.
 */
export function DocsNav({ activeSlug }: { activeSlug: string }) {
  return (
    <nav aria-label="Documentation" className="flex flex-col gap-1">
      <p className="px-2 pb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Documentation
      </p>
      {docs.map((doc) => {
        const active = doc.slug === activeSlug;
        return (
          <Link
            key={doc.slug}
            href={`/docs/${doc.slug}/`}
            aria-current={active ? "page" : undefined}
            className={cn(
              "flex min-h-11 items-center rounded-md px-2 text-sm transition-colors",
              active
                ? "bg-accent font-medium text-accent-foreground"
                : "text-muted-foreground hover:bg-accent/60 hover:text-accent-foreground",
            )}
          >
            {doc.title}
          </Link>
        );
      })}
    </nav>
  );
}
