import type { Metadata } from "next";

import { LandingPage } from "@/components/landing-page";
import { JsonLd } from "@/components/structured-data";
import { resolveLatestRelease } from "@/lib/release";
import { pageMetadata, routeByPath, softwareJsonLd, websiteJsonLd } from "@/lib/seo";

export const metadata: Metadata = pageMetadata(routeByPath("/"));

/**
 * Resolved at build time (the site is a static export), so the copyable install
 * command names the newest published release on the day of the deploy rather
 * than a version somebody remembered to edit. `lib/release.ts` falls back to the
 * pinned version — and says so in the build log — if GitHub cannot be read.
 *
 * The same resolved version goes into the `SoftwareApplication` structured
 * data, so the JSON-LD and the install command on the page cannot disagree.
 */
export default async function HomePage() {
  const release = await resolveLatestRelease();

  return (
    <>
      <JsonLd data={[softwareJsonLd(release.version), websiteJsonLd()]} />
      <LandingPage releaseVersion={release.version} />
    </>
  );
}
