import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'Installation',
  description: 'Install a release binary or build from source, then verify the checksum and run the first-run checks.',
};

export default function Page() {
  return <DocPageView slug='installation' />;
}
