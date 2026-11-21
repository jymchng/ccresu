import type { Metadata } from "next";

import { LandingPage } from "@/components/landing-page";
import { JsonLd } from "@/components/structured-data";
import { resolvedRelease } from "@/lib/release";
import { pageMetadata, routeByPath, softwareJsonLd, websiteJsonLd } from "@/lib/seo";

export const metadata: Metadata = pageMetadata(routeByPath("/"));

/**
 * The version is fixed by the prebuild step, not by this render: `npm run
 * build` runs `scripts/resolve-release.mjs`, which writes the release the pages
 * advertise, and `lib/release.ts` reads that file. Nothing is fetched here, so
 * Next's build data cache has nothing to serve and a warm `.next` cannot bake an
 * older version. A build that could not read the release falls back to the
 * version pinned in `lib/site.ts` and says so in the build log.
 *
 * The same resolved version goes into the `SoftwareApplication` structured
 * data, so the JSON-LD and the install command on the page cannot disagree.
 *
 * `dynamic = "force-static"` is a guard rather than a necessity: if a future
 * edit makes this route dynamic, the static export should refuse the build
 * outright instead of quietly rendering something else.
 */
export const dynamic = "force-static";

export default function HomePage() {
  const release = resolvedRelease();

  return (
    <>
      <JsonLd data={[softwareJsonLd(release.version), websiteJsonLd()]} />
      <LandingPage releaseVersion={release.version} />
    </>
  );
}
