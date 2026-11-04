import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'Configuration',
  description: 'The YAML file, the environment overrides that win, and every section and default ccresu validates.',
};

export default function Page() {
  return <DocPageView slug='configuration' />;
}
