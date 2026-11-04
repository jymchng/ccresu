import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'Known limitations',
  description: 'What ccresu deliberately does not do, and where it reports failure instead of guessing.',
};

export default function Page() {
  return <DocPageView slug='limitations' />;
}
