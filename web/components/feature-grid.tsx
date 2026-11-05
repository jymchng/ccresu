import { InlineText } from "@/components/doc-blocks";
import { features } from "@/lib/landing";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export function FeatureGrid() {
  return (
    <section
      id="features"
      aria-labelledby="features-heading"
      className="mx-auto w-full max-w-6xl px-4 py-16 sm:px-6"
    >
      <div className="mb-10 flex max-w-2xl flex-col gap-2">
        <h2 id="features-heading" className="text-2xl font-semibold tracking-tight sm:text-3xl">
          Built around the only thing that matters: the conversation continues
        </h2>
        <p className="text-sm leading-relaxed text-muted-foreground sm:text-base">
          Eight behaviours, all implemented in the binary you install — no
          cloud service, no dashboard to deploy, no hidden agent in your editor.
        </p>
      </div>

      <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {features.map((feature) => (
          <li key={feature.title}>
            <Card className="h-full transition-colors hover:border-primary/40">
              <CardHeader>
                <feature.icon
                  className="mb-2 size-5 text-primary"
                  aria-hidden="true"
                />
                <CardTitle>{feature.title}</CardTitle>
                <CardDescription>
                  <InlineText text={feature.description} />
                </CardDescription>
              </CardHeader>
              <CardContent>
                <p className="text-xs text-muted-foreground">
                  <InlineText text={feature.detail} />
                </p>
              </CardContent>
            </Card>
          </li>
        ))}
      </ul>
    </section>
  );
}
