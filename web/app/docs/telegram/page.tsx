import type { Metadata } from "next";

import { DocPageView } from "@/components/doc-page-view";

export const metadata: Metadata = {
  title: 'Telegram notifications',
  description: 'Create a bot, configure the chat, and understand delivery, escaping and deduplication.',
};

export default function Page() {
  return <DocPageView slug='telegram' />;
}
