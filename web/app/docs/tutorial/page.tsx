import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: "Tutorial: your first recovered session",
  description:
    "Thirteen steps from an empty machine to a session that was resumed for you, then the same state driven by hand and over the REST/SSE API.",
};

export default function Page() {
  return <DocPageView slug="tutorial" />;
}
