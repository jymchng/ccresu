import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";
import { resolvedRelease } from "@/lib/release";
import { docMetadata } from "@/lib/seo";

/**
 * Title and description come from `lib/docs/installation.ts` through
 * `docMetadata`, so the `<title>`, the `<meta name="description">`, the Open
 * Graph tags, the canonical URL and the `TechArticle` on the page all read the
 * same sentence.
 */
export const metadata: Metadata = docMetadata("installation");

/**
 * The install commands on this page name a release, so the version is read from
 * the file `scripts/resolve-release.mjs` wrote before the build started.
 * Rendering fetches nothing, which is what keeps a warm `.next` from serving an
 * older version; when the release could not be read, `lib/release.ts` falls back
 * to the version pinned in `lib/site.ts` and says so in the build log.
 */
export const dynamic = "force-static";

export default function Page() {
  const release = resolvedRelease();
  return <DocPageView slug="installation" version={release.version} />;
}
