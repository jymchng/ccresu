import Link from "next/link";

import type { StaticPage } from "@/lib/static-pages";
import { staticPages } from "@/lib/static-pages";

/**
 * One template for About, Terms and Privacy.
 *
 * These pages are prose, not documentation: there is no sidebar and no previous
 * or next pager, because a reader arrives from the footer looking for one
 * specific answer and should not be walked through ten chapters. What they do
 * share with the docs is the measure, the heading offsets and the tap-target
 * minimum, which is why the layout lives here rather than being copied three
 * times.
 *
 * The cross-links at the bottom are 44px rows (`min-h-11`), like every other
 * navigational control on the site: a footer link at 20px is unusable with a
 * thumb, and a chain of one-way pages is where readers get stranded.
 */
export function StaticPageView({ page }: { page: StaticPage }) {
  const siblings = staticPages.filter((candidate) => candidate.slug !== page.slug);

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-10 sm:px-6">
      <article className="min-w-0">
        <h1 className="text-3xl font-bold tracking-tight">{page.title}</h1>
        <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted-foreground">
          {page.summary}
        </p>

        <div className="mt-10 flex flex-col gap-10">
          {page.sections.map((section) => (
            <section
              key={section.id}
              aria-labelledby={`${page.slug}-${section.id}`}
              className="flex flex-col gap-3"
            >
              <h2
                id={`${page.slug}-${section.id}`}
                className="scroll-mt-24 text-xl font-semibold tracking-tight"
              >
                {section.heading}
              </h2>
              {section.paragraphs?.map((paragraph) => (
                <p
                  key={paragraph.slice(0, 48)}
                  className="text-sm leading-relaxed text-muted-foreground [overflow-wrap:anywhere]"
                >
                  {paragraph}
                </p>
              ))}
              {section.bullets ? (
                <ul className="flex list-disc flex-col gap-2 pl-5 text-sm leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">
                  {section.bullets.map((bullet) => (
                    <li key={bullet.slice(0, 48)}>{bullet}</li>
                  ))}
                </ul>
              ) : null}
            </section>
          ))}
        </div>

        <nav
          aria-label="Related pages"
          className="mt-12 flex flex-wrap gap-x-6 border-t border-border pt-4"
        >
          <Link
            href="/docs/"
            className="flex min-h-11 min-w-11 items-center text-sm text-muted-foreground hover:text-foreground"
          >
            Documentation
          </Link>
          {siblings.map((sibling) => (
            <Link
              key={sibling.href}
              href={`${sibling.href}/`}
              className="flex min-h-11 min-w-11 items-center text-sm text-muted-foreground hover:text-foreground"
            >
              {sibling.title}
            </Link>
          ))}
        </nav>
      </article>
    </div>
  );
}
