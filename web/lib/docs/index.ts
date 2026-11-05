import { cliDoc } from "./cli";
import { configurationDoc } from "./configuration";
import { deploymentDoc } from "./deployment";
import { fallbackReleaseVersion } from "@/lib/site";

import { createInstallationDoc } from "./installation";
import { limitationsDoc } from "./limitations";
import { recoveryDoc } from "./recovery";
import { securityDoc } from "./security";
import { telegramDoc } from "./telegram";
import { troubleshootingDoc } from "./troubleshooting";
import { tutorialDoc } from "./tutorial";
import type { DocPage } from "./types";

/**
 * The documentation pages, in the order the navigation shows them.
 *
 * Everything here is static except installation, which carries the release
 * version; the entry below uses the fallback so navigation, the index and the
 * tests have a page to show, and `getDoc` rebuilds it with the real version when
 * the page asks for one.
 */
export const docs: readonly DocPage[] = [
  createInstallationDoc(fallbackReleaseVersion),
  tutorialDoc,
  configurationDoc,
  cliDoc,
  recoveryDoc,
  telegramDoc,
  deploymentDoc,
  securityDoc,
  troubleshootingDoc,
  limitationsDoc,
];

/**
 * Look up one page by slug; returns undefined for an unknown slug.
 *
 * `version` only affects the installation page, whose commands name a release.
 * Passing the resolved version keeps that page in step with GitHub without
 * turning the rest of the content into a function.
 */
export function getDoc(
  slug: string,
  version: string = fallbackReleaseVersion,
): DocPage | undefined {
  if (slug === "installation") {
    return createInstallationDoc(version);
  }
  return docs.find((doc) => doc.slug === slug);
}

export type { Block, DocPage } from "./types";
