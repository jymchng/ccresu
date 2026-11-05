import Link from "next/link";
import { GitBranch, Menu } from "lucide-react";

import { MobileDisclosure } from "@/components/mobile-disclosure";
import { ThemeToggle } from "@/components/theme-toggle";
import { Button } from "@/components/ui/button";
import { repo, site } from "@/lib/site";

const navigation = [
  { href: "/", label: "Home" },
  { href: "/docs/", label: "Documentation" },
  { href: "/docs/installation/", label: "Install" },
  { href: "/docs/recovery/", label: "How it works" },
] as const;

export function SiteHeader() {
  return (
    <header className="sticky top-0 z-40 border-b border-border bg-background/80 backdrop-blur">
      <div className="mx-auto flex h-14 w-full max-w-6xl items-center gap-4 px-4 sm:px-6">
        <Link
          href="/"
          className="flex min-h-11 items-center gap-2 font-semibold tracking-tight"
        >
          <span
            aria-hidden="true"
            className="grid size-7 place-items-center rounded-md bg-primary text-xs font-bold text-primary-foreground"
          >
            cr
          </span>
          <span>{site.name}</span>
        </Link>

        <nav aria-label="Main" className="hidden items-center gap-1 sm:flex">
          {navigation.map((item) => (
            <Link
              key={item.href}
              href={item.href}
              className="flex min-h-11 items-center rounded-md px-2.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
            >
              {item.label}
            </Link>
          ))}
        </nav>

        <div className="ml-auto flex items-center gap-1">
          <a
            href={repo.url}
            className="inline-flex min-h-11 min-w-11 items-center justify-center gap-1.5 rounded-md px-2.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
          >
            <GitBranch className="size-4" aria-hidden="true" />
            <span className="hidden sm:inline">GitHub</span>
          </a>
          <ThemeToggle />
          <Button asChild size="sm" className="ml-1 hidden sm:inline-flex">
            <Link href="/docs/installation/">Get started</Link>
          </Button>

          {/*
            The phone menu. `MobileDisclosure` owns the open state, Escape,
            focus restoration and close-on-link, so this stays a plain list of
            links; the panel is fixed under the sticky header (h-14) and is the
            only place the mobile navigation exists.
          */}
          <MobileDisclosure
            label="navigation"
            trigger={<Menu className="size-5" aria-hidden="true" />}
            buttonClassName="sm:hidden"
            backdrop
            panelClassName="absolute inset-x-0 top-14 z-40 border-b border-border bg-background shadow-lg"
            innerClassName="mx-auto flex w-full max-w-6xl flex-col gap-1 px-4 py-3 sm:px-6"
          >
            {navigation.map((item) => (
              <Link
                key={item.href}
                href={item.href}
                className="flex min-h-11 items-center rounded-md px-2.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
              >
                {item.label}
              </Link>
            ))}
          </MobileDisclosure>
        </div>
      </div>
    </header>
  );
}
