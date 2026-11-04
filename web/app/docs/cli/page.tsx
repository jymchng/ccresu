import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'CLI reference',
  description: 'Every command, the global flags, and the exit codes a script can branch on.',
};

export default function Page() {
  return <DocPageView slug='cli' />;
}
