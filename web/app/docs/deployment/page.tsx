import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'Deployment',
  description: 'Run ccresu as a user-level systemd unit, keep it alive, and upgrade it safely.',
};

export default function Page() {
  return <DocPageView slug='deployment' />;
}
