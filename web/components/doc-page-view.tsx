import { ChevronRight } from "lucide-react";
import Link from "next/link";
import { notFound } from "next/navigation";

import { DocBlocks } from "@/components/doc-blocks";
import { DocsMenu } from "@/components/docs-menu";
import { DocsNav } from "@/components/docs-nav";
import { docs, getDoc } from "@/lib/docs";

export function DocPageView({
  slug,
  version,
}: {
  slug: string;
  /** The release the install commands should name; only installation uses it. */
  version?: string;
}) {
  const doc = getDoc(slug, version);
  if (!doc) {
    notFound();
  }

  const index = docs.findIndex((candidate) => candidate.slug === slug);
  const previous = index > 0 ? docs[index - 1] : undefined;
  const next = index >= 0 ? docs[index + 1] : undefined;

  return (
    <div className="mx-auto grid w-full max-w-6xl gap-10 px-4 py-10 sm:px-6 lg:grid-cols-[220px_1fr]">
      <aside className="hidden lg:block">
        <div className="sticky top-20">
          <DocsNav activeSlug={slug} />
        </div>
      </aside>

      {/* Below `lg` the sidebar is gone, so the same list is one tap away here. */}
      <DocsMenu activeSlug={slug} />

      <article className="min-w-0">
        <nav aria-label="Breadcrumb" className="mb-4 flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
          <Link href="/docs/" className="hover:text-foreground">
            Docs
          </Link>
          <ChevronRight className="size-3" aria-hidden="true" />
          <span aria-current="page" className="text-foreground">
            {doc.title}
          </span>
        </nav>

        <h1 className="text-3xl font-bold tracking-tight">{doc.title}</h1>
        <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted-foreground">
          {doc.summary}
        </p>

        <div className="mt-8">
          <DocBlocks blocks={doc.blocks} />
        </div>

        <div className="mt-12 flex flex-wrap justify-between gap-3 border-t border-border pt-6 text-sm">
          {previous ? (
            <Link
              href={`/docs/${previous.slug}/`}
              className="text-muted-foreground hover:text-foreground"
            >
              ← {previous.title}
            </Link>
          ) : (
            <span />
          )}
          {next ? (
            <Link href={`/docs/${next.slug}/`} className="text-muted-foreground hover:text-foreground">
              {next.title} →
            </Link>
          ) : (
            <span />
          )}
        </div>
      </article>
    </div>
  );
}
