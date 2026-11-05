import type { Metadata } from "next";

import { LandingPage } from "@/components/landing-page";
import { resolveLatestRelease } from "@/lib/release";
import { site } from "@/lib/site";

export const metadata: Metadata = {
  title: `${site.name} \u2014 ${site.tagline}`,
  description: site.description,
};

/**
 * Resolved at build time (the site is a static export), so the copyable install
 * command names the newest published release on the day of the deploy rather
 * than a version somebody remembered to edit. `lib/release.ts` falls back to the
 * pinned version — and says so in the build log — if GitHub cannot be read.
 */
export default async function HomePage() {
  const release = await resolveLatestRelease();

  return <LandingPage releaseVersion={release.version} />;
}
