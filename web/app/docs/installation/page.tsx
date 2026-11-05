import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";
import { resolveLatestRelease } from "@/lib/release";

export const metadata: Metadata = {
  title: 'Installation',
  description: 'Install a release binary or build from source, then verify the checksum and run the first-run checks.',
};

/**
 * The install commands on this page name a release, so the newest one is read
 * from GitHub at build time and passed into the page. The fallback in
 * `lib/release.ts` keeps the export building when GitHub is unreachable.
 */
export default async function Page() {
  const release = await resolveLatestRelease();
  return <DocPageView slug='installation' version={release.version} />;
}
