import Link from "next/link";

import { Button } from "@/components/ui/button";

export default function NotFound() {
  return (
    <div className="mx-auto flex w-full max-w-2xl flex-col items-start gap-4 px-4 py-24 sm:px-6">
      <p className="text-sm font-semibold text-primary">404</p>
      <h1 className="text-3xl font-bold tracking-tight">
        That page does not exist
      </h1>
      <p className="text-sm leading-relaxed text-muted-foreground">
        The documentation has nine pages and the landing page is one more. If you
        followed a link from somewhere else, it is out of date — the table
        of contents below is the whole site.
      </p>
      <div className="flex flex-wrap gap-3">
        <Button asChild>
          <Link href="/">Back to the landing page</Link>
        </Button>
        <Button asChild variant="outline">
          <Link href="/docs/">Browse the documentation</Link>
        </Button>
      </div>
    </div>
  );
}
