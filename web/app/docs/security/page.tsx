import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'Security',
  description: 'Loopback binding, bearer tokens, CSRF reasoning, redaction, and what ccresu never does.',
};

export default function Page() {
  return <DocPageView slug='security' />;
}
