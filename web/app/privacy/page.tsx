import type { Metadata } from "next";

import { StaticPageView } from "@/components/static-page-view";
import { privacyPage } from "@/lib/static-pages";
import { staticPageMetadata } from "@/lib/seo";

/**
 * Title, description, canonical and Open Graph tags all come from the page's
 * own content module, so the `<head>` cannot drift from what the page says.
 */
export const metadata: Metadata = staticPageMetadata(privacyPage.href);

export default function PrivacyPage() {
  return <StaticPageView page={privacyPage} />;
}
