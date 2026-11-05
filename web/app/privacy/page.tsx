import type { Metadata } from "next";

import { StaticPageView } from "@/components/static-page-view";
import { privacyPage } from "@/lib/static-pages";

export const metadata: Metadata = {
  title: privacyPage.title,
  description: privacyPage.summary,
};

export default function PrivacyPage() {
  return <StaticPageView page={privacyPage} />;
}
