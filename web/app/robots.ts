import type { MetadataRoute } from "next";

import { sitemapUrl } from "@/lib/seo";
import { site } from "@/lib/site";

/**
 * `/robots.txt`.
 *
 * Everything on the site is public documentation, so there is nothing to
 * disallow — the value here is the `Sitemap:` line, which is how a crawler
 * finds the twelve pages without guessing at URLs. `_next/` is crawlable on
 * purpose: the CSS and JS are what a rendering crawler needs to see the page as
 * a reader does.
 */
/**
 * `output: "export"` requires metadata routes to be declared static; without
 * this Next refuses to collect the route and the whole build fails.
 */
export const dynamic = "force-static";

export default function robots(): MetadataRoute.Robots {
  return {
    rules: [{ userAgent: "*", allow: "/" }],
    sitemap: sitemapUrl,
    host: site.url,
  };
}
