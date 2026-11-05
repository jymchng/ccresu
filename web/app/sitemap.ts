import type { MetadataRoute } from "next";

import { absoluteUrl, siteRoutes } from "@/lib/seo";

/**
 * `/sitemap.xml`, generated at build time from the same route table the pages
 * use.
 *
 * `output: "export"` means this is a file in `out/sitemap.xml`, not a handler —
 * which is exactly what GitHub Pages can serve. The route list comes from
 * `lib/seo.siteRoutes()`, itself derived from the docs and static-page content
 * modules, so a page cannot be published and left out of the sitemap.
 *
 * The 404 page is deliberately absent: `/404.html` and `/_not-found` are error
 * surfaces, and listing an error page invites it to be indexed.
 */
/**
 * `output: "export"` requires metadata routes to be declared static; without
 * this Next refuses to collect the route and the whole build fails.
 */
export const dynamic = "force-static";

export default function sitemap(): MetadataRoute.Sitemap {
  return siteRoutes().map((route) => ({
    url: absoluteUrl(route.path),
    changeFrequency: route.changeFrequency,
    priority: route.priority,
  }));
}
