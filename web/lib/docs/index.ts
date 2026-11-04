import { cliDoc } from "./cli";
import { configurationDoc } from "./configuration";
import { deploymentDoc } from "./deployment";
import { installationDoc } from "./installation";
import { limitationsDoc } from "./limitations";
import { recoveryDoc } from "./recovery";
import { securityDoc } from "./security";
import { telegramDoc } from "./telegram";
import { troubleshootingDoc } from "./troubleshooting";
import type { DocPage } from "./types";

/** The documentation pages, in the order the navigation shows them. */
export const docs: readonly DocPage[] = [
  installationDoc,
  configurationDoc,
  cliDoc,
  recoveryDoc,
  telegramDoc,
  deploymentDoc,
  securityDoc,
  troubleshootingDoc,
  limitationsDoc,
];

/** Look up one page by slug; returns undefined for an unknown slug. */
export function getDoc(slug: string): DocPage | undefined {
  return docs.find((doc) => doc.slug === slug);
}

export type { Block, DocPage } from "./types";
