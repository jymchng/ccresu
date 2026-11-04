import { InlineText } from "@/components/doc-blocks";
import { workflow } from "@/lib/landing";

export function HowItWorks() {
  return (
    <section
      id="how-it-works"
      aria-labelledby="how-it-works-heading"
      className="border-y border-border bg-muted/20"
    >
      <div className="mx-auto w-full max-w-6xl px-4 py-16 sm:px-6">
        <div className="mb-10 flex max-w-2xl flex-col gap-2">
          <h2
            id="how-it-works-heading"
            className="text-2xl font-semibold tracking-tight sm:text-3xl"
          >
            How it works
          </h2>
          <p className="text-sm leading-relaxed text-muted-foreground sm:text-base">
            Five steps, each one observable from the CLI. Nothing happens
            silently: every step is a persisted state transition you can read
            back.
          </p>
        </div>

        <ol className="grid gap-4 md:grid-cols-5">
          {workflow.map((step, index) => (
            <li
              key={step.title}
              className="relative flex flex-col gap-2 rounded-xl border border-border bg-card p-4"
            >
              <span className="text-xs font-semibold text-primary">
                {String(index + 1).padStart(2, "0")}
              </span>
              <h3 className="text-sm font-semibold">{step.title}</h3>
              <code className="break-all rounded bg-muted px-1.5 py-0.5 font-mono text-[0.7rem] text-muted-foreground">
                {step.command}
              </code>
              <p className="text-xs leading-relaxed text-muted-foreground">
                <InlineText text={step.description} />
              </p>
            </li>
          ))}
        </ol>
      </div>
    </section>
  );
}
