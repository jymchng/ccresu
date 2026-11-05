import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { docs } from "@/lib/docs";
import {
  absoluteUrl,
  breadcrumbJsonLd,
  collectionPageJsonLd,
  docMetadata,
  fullTitle,
  homeTitle,
  homeUrl,
  licenseUrl,
  pageMetadata,
  routeByPath,
  siteRoutes,
  softwareJsonLd,
  techArticleJsonLd,
  webPageJsonLd,
  websiteJsonLd,
} from "@/lib/seo";
import { staticPages } from "@/lib/static-pages";
import { repo, site } from "@/lib/site";

/**
 * The SEO contract, asserted where it can actually be wrong.
 *
 * Three groups, in increasing distance from the source:
 *
 *  1. the route table and the metadata objects it produces — a page with no
 *     description or a duplicated title fails here, before anything is built;
 *  2. the structured data — parsed as JSON, because a broken `@graph` is only a
 *     broken `@graph` once something reads it;
 *  3. the export on disk (`out/`) — the sitemap and `robots.txt` cross-checked
 *     against the `index.html` files that were actually written. This group is
 *     skipped when `out/` is absent (a clean checkout runs `npm test` before
 *     `npm run build`), and becomes mandatory when `CCRESU_REQUIRE_EXPORT=1`,
 *     which is what the post-build CI step sets. Silently passing here would be
 *     the one failure mode worth avoiding: a sitemap that lists a route nobody
 *     built is exactly the bug this test exists for.
 */

const outDir = join(process.cwd(), "out");
const exportExists = existsSync(join(outDir, "index.html"));
const exportRequired = process.env.CCRESU_REQUIRE_EXPORT === "1";

/** Every page the site publishes, with the slash the export writes. */
const expectedPaths = [
  "/",
  "/docs/",
  ...docs.map((doc) => `/docs/${doc.slug}/`),
  ...staticPages.map((page) => `${page.href}/`),
];

/** The `index.html` files the export wrote, as site paths. */
function exportedPaths(): string[] {
  const paths: string[] = [];
  const walk = (directory: string, prefix: string) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const full = join(directory, entry.name);
      if (entry.isDirectory()) {
        walk(full, `${prefix}${entry.name}/`);
      } else if (entry.name === "index.html") {
        paths.push(prefix === "" ? "/" : `/${prefix}`);
      }
    }
  };
  walk(outDir, "");
  return paths.sort();
}

describe("the route table", () => {
  it("covers every built page exactly once", () => {
    const paths = siteRoutes().map((route) => route.path);
    expect(new Set(paths).size, "duplicate route in the table").toBe(paths.length);

    // Home, the docs index, the ten doc pages and the three static pages.
    expect(paths).toEqual(expectedPaths);
    expect(expectedPaths).toHaveLength(15);
  });

  it("gives every route a unique title and a real description", () => {
    const routes = siteRoutes();
    const titles = routes.map((route) => route.title);
    expect(new Set(titles).size, "two routes share a title").toBe(titles.length);

    for (const route of routes) {
      expect(route.title.trim(), `${route.path} has no title`).not.toBe("");
      expect(route.description.length, `${route.path} description too short`).toBeGreaterThan(40);
      expect(route.description.length, `${route.path} description too long`).toBeLessThan(320);
    }
  });

  it("takes each doc route's title and description from that page's content", () => {
    for (const doc of docs) {
      const route = routeByPath(`/docs/${doc.slug}/`);
      expect(route.title).toBe(doc.title);
      expect(route.description).toBe(doc.summary);
    }
  });
});

describe("per-page metadata", () => {
  it("puts every canonical under the site's base path", () => {
    for (const route of siteRoutes()) {
      const metadata = pageMetadata(route);
      const canonical = String(metadata.alternates?.canonical);
      expect(canonical.startsWith(`${site.url}/`), canonical).toBe(true);
      expect(canonical.endsWith("/"), `${canonical} should be a directory URL`).toBe(true);
      // The base path must survive: a canonical of https://jymchng.github.io/docs/
      // would point at a page that does not exist.
      expect(canonical).toContain(site.basePath);
    }
    expect(homeUrl).toBe("https://jymchng.github.io/ccresu/");
  });

  it("publishes an og/twitter pair per route whose title is the page's own", () => {
    const openGraphTitles = new Set<string>();
    for (const route of siteRoutes()) {
      const metadata = pageMetadata(route);
      const url = absoluteUrl(route.path);
      expect(metadata.openGraph?.url).toBe(url);
      expect(metadata.twitter?.description).toBe(route.description);
      expect(metadata.openGraph?.description).toBe(route.description);
      expect(metadata.twitter?.title).toBe(metadata.openGraph?.title);
      openGraphTitles.add(String(metadata.openGraph?.title));
    }
    // One per page: the generic inherited og:title was the bug this replaced.
    expect(openGraphTitles.size).toBe(siteRoutes().length);
  });

  it("does not repeat the site name in a title", () => {
    expect(homeTitle).toBe(`${site.name} — ${site.tagline}`);
    expect(fullTitle(homeTitle)).toBe(homeTitle);
    expect(fullTitle("CLI reference")).toBe(`CLI reference · ${site.name}`);
    for (const route of siteRoutes()) {
      const title = String(pageMetadata(route).openGraph?.title);
      expect(title.match(new RegExp(site.name, "g"))?.length, title).toBe(1);
    }
  });

  it("declares the site language on the canonical URL instead of leaving it to guesswork", () => {
    const metadata = pageMetadata(routeByPath("/docs/cli/"));
    expect(metadata.alternates?.languages).toEqual({
      en: "https://jymchng.github.io/ccresu/docs/cli/",
      "x-default": "https://jymchng.github.io/ccresu/docs/cli/",
    });
  });

  it("marks documentation pages as articles and the rest as pages", () => {
    // `openGraph` is a union in Next's types (`website` and `article` carry
    // different fields), so the discriminant is read through one narrow shape.
    const ogType = (path: string) =>
      (pageMetadata(routeByPath(path)).openGraph as { type?: string } | undefined)?.type;

    expect(ogType("/docs/cli/")).toBe("article");
    expect(ogType("/docs/")).toBe("website");
    expect(ogType("/about/")).toBe("website");
  });

  it("generates a doc page's metadata from its slug", () => {
    const doc = docs.find((candidate) => candidate.slug === "recovery")!;
    const metadata = docMetadata("recovery");
    expect(metadata.description).toBe(doc.summary);
    expect(String(metadata.alternates?.canonical)).toBe(
      "https://jymchng.github.io/ccresu/docs/recovery/",
    );
    expect(() => docMetadata("no-such-page")).toThrow(/no documentation page/);
  });
});

describe("structured data", () => {
  it("describes the software with the MIT licence and the resolved version", () => {
    const data = softwareJsonLd("1.4.2");
    // A crawler reads JSON, so the test does too: this is the round trip that
    // would fail if the component escaped something it should not have.
    const parsed = JSON.parse(JSON.stringify(data));

    expect(parsed["@context"]).toBe("https://schema.org");
    expect(parsed["@type"]).toBe("SoftwareApplication");
    expect(parsed.name).toBe("ccresu");
    expect(parsed.softwareVersion).toBe("1.4.2");
    expect(parsed.license).toBe(licenseUrl);
    expect(parsed.license.toLowerCase()).toContain("mit");
    expect(parsed.codeRepository).toBe(repo.url);
    expect(parsed.url).toBe(homeUrl);
    expect(parsed.downloadUrl).toBe(`${repo.url}/releases/download/v1.4.2/ccresu_1.4.2_linux_amd64.tar.gz`);
    expect(parsed.offers.price).toBe("0");
    expect(parsed.isAccessibleForFree).toBe(true);
    expect(parsed.description.length).toBeGreaterThan(40);
  });

  it("names the site as a website with the site's own URL and language", () => {
    const parsed = JSON.parse(JSON.stringify(websiteJsonLd()));
    expect(parsed["@type"]).toBe("WebSite");
    expect(parsed.url).toBe(homeUrl);
    expect(parsed.inLanguage).toBe("en");
  });

  it("builds a sequential breadcrumb trail with absolute item URLs", () => {
    const trail = ["/", "/docs/", "/docs/recovery/"];
    const parsed = JSON.parse(JSON.stringify(breadcrumbJsonLd(
      trail.map((path, index) => ({ name: `Step ${index}`, path })),
    )));

    expect(parsed["@type"]).toBe("BreadcrumbList");
    expect(parsed.itemListElement).toHaveLength(3);
    parsed.itemListElement.forEach((item: { position: number; item: string }, index: number) => {
      expect(item.position).toBe(index + 1);
      expect(item.item).toBe(absoluteUrl(trail[index]));
      expect(item.item.startsWith("https://")).toBe(true);
    });
  });

  it("marks documentation pages as TechArticle and the rest as WebPage/CollectionPage", () => {
    const article = JSON.parse(JSON.stringify(techArticleJsonLd(routeByPath("/docs/recovery/"))));
    expect(article["@type"]).toBe("TechArticle");
    expect(article.headline).toBe("How recovery works");
    expect(article.url).toBe("https://jymchng.github.io/ccresu/docs/recovery/");
    expect(article.inLanguage).toBe("en");
    expect(article.isPartOf.name).toBe("ccresu");

    const webpage = JSON.parse(JSON.stringify(webPageJsonLd(routeByPath("/terms/"))));
    expect(webpage["@type"]).toBe("WebPage");
    expect(webpage.url).toBe("https://jymchng.github.io/ccresu/terms/");

    const index = JSON.parse(JSON.stringify(collectionPageJsonLd(routeByPath("/docs/"))));
    expect(index["@type"]).toBe("CollectionPage");
    expect(index.hasPart).toHaveLength(docs.length);
  });
});

describe("the export on disk", () => {
  it.skipIf(!exportExists && !exportRequired)(
    "sitemaps exactly the pages that were built, and nothing else",
    () => {
      const sitemapPath = join(outDir, "sitemap.xml");
      expect(existsSync(sitemapPath), "out/sitemap.xml is missing — run npm run build").toBe(true);

      const sitemap = readFileSync(sitemapPath, "utf8");
      const locations = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((match) => match[1]);

      // Absolute, base path included, one per built page.
      expect(locations).toEqual(expectedPaths.map((path) => absoluteUrl(path)));

      // And the reverse direction: every index.html the export wrote is listed.
      // `404`/`_not-found` are error surfaces and must not be indexed.
      const built = exportedPaths().filter(
        (path) => !path.startsWith("/404/") && !path.startsWith("/_not-found/"),
      );
      expect(built.sort()).toEqual([...expectedPaths].sort());
      for (const path of built) {
        expect(locations, `${path} is built but missing from sitemap.xml`).toContain(
          absoluteUrl(path),
        );
      }
    },
  );

  it.skipIf(!exportExists && !exportRequired)("points robots.txt at that sitemap", () => {
    const robotsPath = join(outDir, "robots.txt");
    expect(existsSync(robotsPath), "out/robots.txt is missing — run npm run build").toBe(true);

    const robots = readFileSync(robotsPath, "utf8");
    expect(robots).toMatch(/User-Agent: \*/);
    expect(robots).toMatch(/Allow: \//);
    expect(robots).toContain(`Sitemap: ${absoluteUrl("/sitemap.xml")}`);
  });

  it.skipIf(!exportExists && !exportRequired)(
    "gives every built page its own title, description, canonical and og/twitter pair",
    () => {
      const titles = new Set<string>();
      for (const path of expectedPaths) {
        const file = join(outDir, path === "/" ? "" : path, "index.html");
        expect(existsSync(file), `missing export for ${path}`).toBe(true);
        const html = readFileSync(file, "utf8");
        const head = html.slice(0, html.indexOf("</head>"));

        const title = head.match(/<title[^>]*>([^<]*)<\/title>/)?.[1] ?? "";
        expect(title, `${path} has no <title>`).not.toBe("");
        // "ccresu · ccresu" or "About · ccresu · ccresu" is the template bug.
        expect(title.match(/ccresu/g)?.length, `${path}: ${title}`).toBe(1);
        titles.add(title);

        expect(head, `${path} has no description`).toMatch(/<meta name="description" content="[^"]+"/);
        expect(head, `${path} canonical`).toContain(
          `<link rel="canonical" href="${absoluteUrl(path)}"`,
        );
        expect(head, `${path} hreflang en`).toContain('hrefLang="en"');
        expect(head, `${path} hreflang x-default`).toContain('hrefLang="x-default"');
        expect(head, `${path} og:url`).toContain(
          `<meta property="og:url" content="${absoluteUrl(path)}"`,
        );
        expect(head, `${path} og:title`).toMatch(/<meta property="og:title" content="[^"]+"/);
        expect(head, `${path} og:description`).toMatch(
          /<meta property="og:description" content="[^"]+"/,
        );
        expect(head, `${path} twitter:card`).toContain('name="twitter:card"');
        expect(head, `${path} twitter:title`).toMatch(/name="twitter:title" content="[^"]+"/);
      }
      expect(titles.size, "two built pages share a <title>").toBe(expectedPaths.length);
    },
  );

  it.skipIf(!exportExists && !exportRequired)(
    "embeds JSON-LD that parses, on the pages that claim it",
    () => {
      const read = (path: string) =>
        readFileSync(join(outDir, path === "/" ? "" : path, "index.html"), "utf8");

      const blocks = (html: string) =>
        [...html.matchAll(/<script type="application\/ld\+json">(.*?)<\/script>/gs)].map(
          (match) => JSON.parse(match[1]),
        );

      const home = blocks(read("/"));
      const nodes = home.flatMap((block) => block["@graph"] ?? [block]);
      const software = nodes.find((node) => node["@type"] === "SoftwareApplication");
      expect(software, "home page is missing SoftwareApplication").toBeTruthy();
      expect(software.license).toBe(licenseUrl);
      // The version in the markup is whatever the build resolved — assert the
      // shape rather than a number that changes when a release is cut.
      expect(software.softwareVersion).toMatch(/^\d+\.\d+\.\d+/);

      const recovery = blocks(read("/docs/recovery/")).flatMap(
        (block) => block["@graph"] ?? [block],
      );
      expect(recovery.map((node) => node["@type"])).toEqual(["BreadcrumbList", "TechArticle"]);
      const breadcrumb = recovery[0];
      expect(breadcrumb.itemListElement.map((item: { name: string }) => item.name)).toEqual([
        "Home",
        "Documentation",
        "How recovery works",
      ]);

      const terms = blocks(read("/terms/")).flatMap((block) => block["@graph"] ?? [block]);
      expect(terms.map((node) => node["@type"])).toEqual(["BreadcrumbList", "WebPage"]);
    },
  );

  it.skipIf(!exportExists && !exportRequired)("has exactly one h1 on every built page", () => {
    for (const path of expectedPaths) {
      const html = readFileSync(join(outDir, path === "/" ? "" : path, "index.html"), "utf8");
      expect(html.match(/<h1[\s>]/g)?.length, `${path} h1 count`).toBe(1);
    }
  });
});
