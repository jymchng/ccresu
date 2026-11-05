import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";
import { resolveLatestRelease } from "@/lib/release";
import { docMetadata } from "@/lib/seo";

/**
 * Title and description come from `lib/docs/installation.ts` through
 * `docMetadata`, so the `<title>`, the `<meta name="description">`, the Open
 * Graph tags, the canonical URL and the `TechArticle` on the page all read the
 * same sentence.
 */
export const metadata: Metadata = docMetadata("installation");

/**
 * The install commands on this page name a release, so the newest one is read
 * from GitHub at build time and passed into the page. The fallback in
 * `lib/release.ts` keeps the export building when GitHub is unreachable.
 */
export default async function Page() {
  const release = await resolveLatestRelease();
  return <DocPageView slug="installation" version={release.version} />;
}
