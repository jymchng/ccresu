import type { Metadata } from "next";

import { StaticPageView } from "@/components/static-page-view";
import { aboutPage } from "@/lib/static-pages";

export const metadata: Metadata = {
  title: aboutPage.title,
  description: aboutPage.summary,
};

export default function AboutPage() {
  return <StaticPageView page={aboutPage} />;
}
