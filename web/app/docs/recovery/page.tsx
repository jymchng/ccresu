import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'How recovery works',
  description: 'The persisted state machine, the exact resume command, and the verification that proves the conversation continued.',
};

export default function Page() {
  return <DocPageView slug='recovery' />;
}
