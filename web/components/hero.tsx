import { ArrowRight, GitBranch, Terminal } from "lucide-react";
import Link from "next/link";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { repo, site } from "@/lib/site";

/**
 * The hero tells the whole story in one screen: the message that interrupts
 * your work, and the promise that the same session continues afterwards.
 */
export function Hero() {
  return (
    <section className="relative overflow-hidden border-b border-border">
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 bg-[radial-gradient(60%_50%_at_50%_0%,theme(colors.primary/12%),transparent)]"
      />
      <div className="relative mx-auto grid w-full max-w-6xl gap-10 px-4 py-16 sm:px-6 lg:grid-cols-[1.1fr_1fr] lg:py-24">
        <div className="flex flex-col items-start gap-5">
          <Badge variant="outline" className="gap-1.5">
            <span className="size-1.5 rounded-full bg-emerald-500" aria-hidden="true" />
            Local-first watchdog for Claude Code
          </Badge>

          <h1 className="text-balance text-4xl font-bold tracking-tight sm:text-5xl">
            Your Claude Code session, resumed after the limit resets.
          </h1>

          <p className="max-w-xl text-pretty text-base leading-relaxed text-muted-foreground sm:text-lg">
            {site.name} watches your sessions, recognises the usage-limit
            message, waits for the reset time in the right timezone, then
            resumes <strong className="text-foreground">the same</strong>{" "}
            conversation with {" "}
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em]">
              claude -p --resume
            </code>{" "}
            — preserving history, working directory and active goal — and
            verifies the resume actually happened.
          </p>

          <div className="flex flex-wrap items-center gap-3">
            <Button asChild size="lg">
              <Link href="/docs/installation/">
                Install {site.name}
                <ArrowRight aria-hidden="true" />
              </Link>
            </Button>
            <Button asChild size="lg" variant="outline">
              <a href={repo.url}>
                <GitBranch aria-hidden="true" />
                View source
              </a>
            </Button>
          </div>

          <dl className="mt-2 grid w-full max-w-lg grid-cols-3 gap-4 border-t border-border pt-5 text-sm">
            {[
              { term: "Runs", detail: "on your machine" },
              { term: "Interface", detail: "CLI + REST/SSE" },
              { term: "Telemetry", detail: "none" },
            ].map((item) => (
              <div key={item.term} className="flex flex-col gap-0.5">
                <dt className="text-xs uppercase tracking-wide text-muted-foreground">
                  {item.term}
                </dt>
                <dd className="font-medium">{item.detail}</dd>
              </div>
            ))}
          </dl>
        </div>

        <div className="flex flex-col gap-3 self-center">
          <div className="overflow-hidden rounded-xl border border-border bg-card shadow-sm">
            <div className="flex items-center gap-2 border-b border-border bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
              <Terminal className="size-3.5" aria-hidden="true" />
              what interrupts your work
            </div>
            <pre className="overflow-x-auto px-4 py-4 text-xs leading-relaxed">
              <code>{`You've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)
Goal paused \u00b7 usage limit reached \u00b7 send a message after it resets`}</code>
            </pre>
          </div>

          <div className="overflow-hidden rounded-xl border border-border bg-card shadow-sm">
            <div className="flex items-center gap-2 border-b border-border bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
              <Terminal className="size-3.5" aria-hidden="true" />
              what {site.name} does about it
            </div>
            <pre className="overflow-x-auto px-4 py-4 text-xs leading-relaxed text-muted-foreground">
              <code>{`$ ccresu status
sessions:      3 (3 enabled)
pending:       1
schedules:     1
recovery:      yes (max attempts 10)

$ ccresu history 4d1f9c2a\nWAITING_FOR_RESET  reset at 12:00 Europe/Berlin
RESET_TIME_REACHED  limit cleared
RESUMING            claude -p --resume 4d1f9c2a
RESUMED             same session, transcript grew`}</code>
            </pre>
          </div>
        </div>
      </div>
    </section>
  );
}
