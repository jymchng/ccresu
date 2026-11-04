import { ArrowRight, CircleSlash, GitBranch, Server, ShieldCheck, Sparkles } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";

import { FeatureGrid } from "@/components/feature-grid";
import { Hero } from "@/components/hero";
import { HowItWorks } from "@/components/how-it-works";
import { InstallCommands } from "@/components/install-commands";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { repo, site } from "@/lib/site";

export const metadata: Metadata = {
  title: `${site.name} \u2014 ${site.tagline}`,
  description: site.description,
};

const guarantees = [
  {
    icon: ShieldCheck,
    title: "No keystroke automation",
    description:
      "Resume runs as `claude -p --resume <session-id>` with the message as an argument. No pty, no simulated typing, no fork.",
  },
  {
    icon: CircleSlash,
    title: "No limit bypass",
    description:
      "ccresu never tries to lift a quota. It reads the reset time from the message, waits for it, and acts only afterwards.",
  },
  {
    icon: Server,
    title: "No hosted service",
    description:
      "State lives in a local SQLite file. The optional API binds 127.0.0.1 and the binary ships no web UI.",
  },
] as const;

export default function HomePage() {
  return (
    <>
      <Hero />
      <FeatureGrid />
      <HowItWorks />

      <section
        id="installation"
        aria-labelledby="installation-heading"
        className="mx-auto w-full max-w-6xl px-4 py-16 sm:px-6"
      >
        <div className="grid gap-8 lg:grid-cols-[0.9fr_1.1fr] lg:items-start">
          <div className="flex flex-col gap-3">
            <Badge variant="success" className="w-fit">
              <Sparkles className="size-3" aria-hidden="true" />
              One binary, no dependencies
            </Badge>
            <h2
              id="installation-heading"
              className="text-2xl font-semibold tracking-tight sm:text-3xl"
            >
              Install in one command
            </h2>
            <p className="max-w-md text-sm leading-relaxed text-muted-foreground">
              Download the release asset for your platform, verify it against
              the published {" "}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em]">
                checksums.txt
              </code>
              , and put the binary on your {" "}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em]">
                PATH
              </code>
              . The SQLite driver is pure Go, so there is nothing else to
              install.
            </p>
            <ul className="mt-1 flex flex-col gap-1.5 text-sm text-muted-foreground">
              <li>
                Assets: {" "}
                <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em]">
                  ccresu_&lt;version&gt;_&lt;os&gt;_&lt;arch&gt;.tar.gz
                </code>{" "}
                ({""}
                <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em]">
                  .zip
                </code>{" "}
                on Windows)
              </li>
              <li>
                Targets: linux, darwin and windows on amd64 and arm64
              </li>
              <li>
                Or build from source with Go 1.26:{" "}
                <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em]">
                  make build
                </code>
              </li>
            </ul>
            <div className="mt-2 flex flex-wrap gap-3">
              <Button asChild>
                <Link href="/docs/installation/">
                  Full installation guide
                  <ArrowRight aria-hidden="true" />
                </Link>
              </Button>
              <Button asChild variant="outline">
                <a href={repo.latestRelease}>
                  <GitBranch aria-hidden="true" />
                  Latest release
                </a>
              </Button>
            </div>
          </div>

          <InstallCommands />
        </div>
      </section>

      <section
        aria-labelledby="guarantees-heading"
        className="border-t border-border bg-muted/20"
      >
        <div className="mx-auto w-full max-w-6xl px-4 py-16 sm:px-6">
          <h2
            id="guarantees-heading"
            className="mb-8 text-2xl font-semibold tracking-tight sm:text-3xl"
          >
            What it will never do
          </h2>
          <ul className="grid gap-4 md:grid-cols-3">
            {guarantees.map((item) => (
              <li
                key={item.title}
                className="flex flex-col gap-2 rounded-xl border border-border bg-card p-5"
              >
                <item.icon className="size-5 text-primary" aria-hidden="true" />
                <h3 className="text-sm font-semibold">{item.title}</h3>
                <p className="text-sm leading-relaxed text-muted-foreground">
                  {item.description}
                </p>
              </li>
            ))}
          </ul>
        </div>
      </section>

      <section className="mx-auto w-full max-w-6xl px-4 py-16 sm:px-6">
        <div className="flex flex-col items-start justify-between gap-6 rounded-2xl border border-border bg-card p-8 sm:flex-row sm:items-center">
          <div className="flex max-w-xl flex-col gap-2">
            <h2 className="text-2xl font-semibold tracking-tight">
              Read the mechanism before you trust it
            </h2>
            <p className="text-sm leading-relaxed text-muted-foreground">
              Every claim on this page is implemented in {" "}
              {site.name} and documented: the exact resume command, the
              verification that proves the conversation continued, the state
              machine, and the limitations we know about.
            </p>
          </div>
          <div className="flex flex-wrap gap-3">
            <Button asChild>
              <Link href="/docs/recovery/">How recovery works</Link>
            </Button>
            <Button asChild variant="outline">
              <Link href="/docs/limitations/">Known limitations</Link>
            </Button>
          </div>
        </div>
      </section>
    </>
  );
}
