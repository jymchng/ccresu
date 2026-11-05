import Link from "next/link";

import { repo, site, localApi } from "@/lib/site";
import { staticPages } from "@/lib/static-pages";

/**
 * Footer link rows are `flex min-h-11 items-center`: a bare inline link is a
 * 20px-tall tap target, which is fine with a mouse and hostile with a thumb.
 * The column list also reflows to two columns at `sm` before it goes to four at
 * `md`, so the sections never squeeze into unreadably narrow columns.
 */
const linkClass =
  "flex min-h-11 min-w-11 items-center text-sm text-muted-foreground transition-colors hover:text-foreground";

const productLinks = [
  { href: "/docs/installation/", label: "Installation" },
  { href: "/docs/tutorial/", label: "Tutorial" },
  { href: "/docs/cli/", label: "CLI reference" },
  { href: "/docs/configuration/", label: "Configuration" },
  { href: "/docs/recovery/", label: "How recovery works" },
] as const;

const referenceLinks = [
  { href: "/docs/telegram/", label: "Telegram setup" },
  { href: "/docs/deployment/", label: "Deployment" },
  { href: "/docs/security/", label: "Security" },
  { href: "/docs/troubleshooting/", label: "Troubleshooting" },
  { href: "/docs/limitations/", label: "Known limitations" },
] as const;

const projectLinks = [
  { href: repo.url, label: "Repository" },
  { href: repo.releases, label: "Releases" },
  { href: repo.issues, label: "Issues" },
  { href: repo.license, label: "License" },
] as const;

export function SiteFooter() {
  return (
    <footer className="border-t border-border bg-muted/20">
      <div className="mx-auto grid w-full max-w-6xl gap-8 px-4 py-10 sm:grid-cols-2 sm:px-6 md:grid-cols-4">
        <div className="flex min-w-0 flex-col gap-2">
          <p className="font-semibold tracking-tight">{site.name}</p>
          <p className="max-w-xs text-sm text-muted-foreground">{site.tagline}.</p>
          <p className="text-xs text-muted-foreground">
            Local API: <code className="font-mono [overflow-wrap:anywhere]">{localApi.address}</code>
          </p>
        </div>
        <div className="flex min-w-0 flex-col sm:gap-0.5">
          <p className="pb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Product
          </p>
          {productLinks.map((link) => (
            <Link key={link.href} href={link.href} className={linkClass}>
              {link.label}
            </Link>
          ))}
        </div>
        <div className="flex min-w-0 flex-col sm:gap-0.5">
          <p className="pb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Reference
          </p>
          {referenceLinks.map((link) => (
            <Link key={link.href} href={link.href} className={linkClass}>
              {link.label}
            </Link>
          ))}
        </div>
        <div className="flex min-w-0 flex-col sm:gap-0.5">
          <p className="pb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Project
          </p>
          {projectLinks.map((link) => (
            <a key={link.href} href={link.href} className={linkClass}>
              {link.label}
            </a>
          ))}
        </div>
      </div>
      <div className="border-t border-border px-4 py-3 sm:px-6">
        {/*
          The three non-documentation pages are linked here and only here: they
          are site furniture (who runs this, what you agreed to, what happens to
          your data), not chapters of the manual, so they must not appear in
          `DocsNav`. `flex-wrap` keeps the row from running off a 320px screen,
          and each link keeps the 44px row the rest of the footer uses.
        */}
        <nav
          aria-label="Site"
          className="mx-auto flex w-full max-w-6xl flex-wrap items-center gap-x-5"
        >
          {staticPages.map((page) => (
            <Link key={page.href} href={`${page.href}/`} className={linkClass}>
              {page.title}
            </Link>
          ))}
          <a href={repo.license} className={linkClass}>
            MIT licence
          </a>
        </nav>
      </div>
      <div className="border-t border-border px-4 py-4 text-center text-xs text-muted-foreground sm:px-6">
        {site.name} is a local tool: no telemetry, no hosted service, no web UI
        bundled in the binary.
      </div>
    </footer>
  );
}
