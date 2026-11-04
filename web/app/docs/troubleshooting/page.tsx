import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'Troubleshooting',
  description: 'Diagnose with doctor, inspect and history, then match the symptom to the fix.',
};

export default function Page() {
  return <DocPageView slug='troubleshooting' />;
}
