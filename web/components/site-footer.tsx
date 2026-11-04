import Link from "next/link";

import { repo, site, localApi } from "@/lib/site";

const productLinks = [
  { href: "/docs/installation/", label: "Installation" },
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
      <div className="mx-auto grid w-full max-w-6xl gap-8 px-4 py-10 sm:px-6 md:grid-cols-4">
        <div className="flex flex-col gap-2">
          <p className="font-semibold tracking-tight">{site.name}</p>
          <p className="max-w-xs text-sm text-muted-foreground">{site.tagline}.</p>
          <p className="text-xs text-muted-foreground">
            Local API: <code className="font-mono">{localApi.address}</code>
          </p>
        </div>
        <div className="flex flex-col gap-2">
          <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Product
          </p>
          {productLinks.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              className="text-sm text-muted-foreground transition-colors hover:text-foreground"
            >
              {link.label}
            </Link>
          ))}
        </div>
        <div className="flex flex-col gap-2">
          <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Reference
          </p>
          {referenceLinks.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              className="text-sm text-muted-foreground transition-colors hover:text-foreground"
            >
              {link.label}
            </Link>
          ))}
        </div>
        <div className="flex flex-col gap-2">
          <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Project
          </p>
          {projectLinks.map((link) => (
            <a
              key={link.href}
              href={link.href}
              className="text-sm text-muted-foreground transition-colors hover:text-foreground"
            >
              {link.label}
            </a>
          ))}
        </div>
      </div>
      <div className="border-t border-border px-4 py-4 text-center text-xs text-muted-foreground sm:px-6">
        {site.name} is a local tool: no telemetry, no hosted service, no web UI
        bundled in the binary.
      </div>
    </footer>
  );
}
