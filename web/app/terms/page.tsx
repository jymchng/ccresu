import type { Metadata } from "next";

import { StaticPageView } from "@/components/static-page-view";
import { termsPage } from "@/lib/static-pages";

export const metadata: Metadata = {
  title: termsPage.title,
  description: termsPage.summary,
};

export default function TermsPage() {
  return <StaticPageView page={termsPage} />;
}
