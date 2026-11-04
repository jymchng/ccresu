import type { Metadata } from "next";
import Link from "next/link";

import { DocsNav } from "@/components/docs-nav";
import { docs } from "@/lib/docs";
import { repo } from "@/lib/site";

export const metadata: Metadata = {
  title: "Documentation",
  description:
    "Installation, a hands-on tutorial, configuration, CLI reference, recovery mechanism, Telegram setup, deployment, security, troubleshooting and known limitations for ccresu.",
};

export default function DocsIndexPage() {
  return (
    <div className="mx-auto grid w-full max-w-6xl gap-10 px-4 py-10 sm:px-6 lg:grid-cols-[220px_1fr]">
      <aside className="hidden lg:block">
        <div className="sticky top-20">
          <DocsNav activeSlug="" />
        </div>
      </aside>

      <div>
        <h1 className="text-3xl font-bold tracking-tight">Documentation</h1>
        <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted-foreground">
          Everything ccresu does, in the order you need it: install it, configure
          it, learn the commands, understand the recovery state machine, then
          deploy and troubleshoot it. The same material ships in the repository
          under <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em]">docs/</code>.
        </p>

        <ul className="mt-8 grid gap-4 sm:grid-cols-2">
          {docs.map((doc, index) => (
            <li key={doc.slug}>
              <Link
                href={`/docs/${doc.slug}/`}
                className="flex h-full flex-col gap-1.5 rounded-xl border border-border bg-card p-5 transition-colors hover:border-primary/40"
              >
                <span className="text-xs font-semibold text-primary">
                  {String(index + 1).padStart(2, "0")}
                </span>
                <span className="font-semibold">{doc.title}</span>
                <span className="text-sm leading-relaxed text-muted-foreground">
                  {doc.summary}
                </span>
              </Link>
            </li>
          ))}
        </ul>

        <p className="mt-8 text-sm text-muted-foreground">
          Release notes live on the {" "}
          <a href={repo.releases} className="text-primary hover:underline">
            releases page
          </a>
          . Found something wrong in these docs?{" "}
          <a href={repo.issues} className="text-primary hover:underline">
            Open an issue
          </a>
          .
        </p>
      </div>
    </div>
  );
}
