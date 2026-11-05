import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";
import { docMetadata } from "@/lib/seo";

/**
 * Title and description come from `lib/docs/telegram.ts` through `docMetadata`,
 * so the `<title>`, the `<meta name="description">`, the Open Graph tags, the
 * canonical URL and the `TechArticle` on the page all read the same sentence —
 * and the sitemap cannot list a title that the page does not show.
 */
export const metadata: Metadata = docMetadata("telegram");

export default function Page() {
  return <DocPageView slug="telegram" />;
}
