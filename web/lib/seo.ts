import type { Metadata } from "next";

import { docs, getDoc } from "./docs";
import { staticPages } from "./static-pages";
import { assetUrl, repo, site } from "./site";

/**
 * Everything a search engine or a link unfurler reads, in one place.
 *
 * Three rules made this a module rather than metadata sprinkled through the
 * `app/` tree:
 *
 *  - **One route table.** The sitemap, `robots.txt` and the per-page metadata
 *    are generated from `siteRoutes()`, which is itself derived from the real
 *    content modules (`lib/docs`, `lib/static-pages`). A page cannot be added
 *    without a title and a description, and a route cannot appear in the
 *    sitemap without existing as a route.
 *  - **Absolute URLs, built from `site.url`.** The site is served from a
 *    project path (`https://jymchng.github.io/ccresu`), so a `next`-resolved
 *    relative canonical would silently drop `/ccresu`. Every URL here goes
 *    through `absoluteUrl`.
 *  - **The title template is applied once.** The root layout appends
 *    `· ccresu` to a child title; Open Graph and Twitter titles bypass that
 *    template, so `fullTitle()` appends the same suffix only when it is not
 *    already there. No surface ends up reading `ccresu · ccresu`.
 *
 * Nothing here invents content: titles and descriptions are the pages' own
 * `title`/`summary` fields, which the tests already assert against the rendered
 * markup.
 */

/** The one description the documentation index uses, in every surface. */
export const docsIndexDescription =
  "Installation, a hands-on tutorial, configuration, CLI reference, recovery mechanism, Telegram setup, deployment, security, troubleshooting and known limitations for ccresu.";

export type RouteKind = "home" | "docs-index" | "doc" | "static";

export type SiteRoute = {
  /** Path on the site, always with the trailing slash the export uses. */
  path: string;
  kind: RouteKind;
  /** Page title without the site suffix. */
  title: string;
  /** One sentence, used as `<meta name="description">` and `og:description`. */
  description: string;
  /** Sitemap hint, 0–1. */
  priority: number;
  changeFrequency: "weekly" | "monthly" | "yearly";
};

/**
 * Resolve a site path to the absolute URL a crawler should index.
 *
 * `site.url` already carries the `/ccresu` base path, and the export writes one
 * directory per route, so every page URL ends in a slash. File URLs (the
 * sitemap and robots) do not, and are the one exception.
 */
export function absoluteUrl(path = "/"): string {
  const withLeadingSlash = path.startsWith("/") ? path : `/${path}`;
  const isFile = /\.[a-z0-9]+$/i.test(withLeadingSlash);
  const normalised =
    isFile || withLeadingSlash.endsWith("/") ? withLeadingSlash : `${withLeadingSlash}/`;
  return `${site.url}${normalised}`;
}

/** The canonical home page URL, e.g. `https://jymchng.github.io/ccresu/`. */
export const homeUrl = absoluteUrl("/");

/** Absolute URL of the sitemap, as `robots.txt` must point at it. */
export const sitemapUrl = absoluteUrl("/sitemap.xml");

/** `Title · ccresu`, unless the title already names the site. */
export function fullTitle(title: string): string {
  if (title.includes(site.name)) {
    return title;
  }
  return `${title} · ${site.name}`;
}

/** The home page title: the site name and what it does, not a slogan. */
export const homeTitle = `${site.name} — ${site.tagline}`;

/**
 * Every page the site publishes, in navigation order.
 *
 * Derived from the content modules rather than typed out here, so a doc page
 * added to `lib/docs` appears in the sitemap (and gets a title and description)
 * without anyone remembering to update a second list.
 */
export function siteRoutes(): SiteRoute[] {
  const docRoutes: SiteRoute[] = docs.map((doc) => ({
    path: `/docs/${doc.slug}/`,
    kind: "doc",
    title: doc.title,
    description: doc.summary,
    priority: 0.8,
    changeFrequency: "monthly",
  }));

  const staticRoutes: SiteRoute[] = staticPages.map((page) => ({
    path: `${page.href}/`,
    kind: "static",
    title: page.title,
    description: page.summary,
    priority: 0.4,
    changeFrequency: "yearly",
  }));

  return [
    {
      path: "/",
      kind: "home",
      title: homeTitle,
      description: site.description,
      priority: 1,
      changeFrequency: "weekly",
    },
    {
      path: "/docs/",
      kind: "docs-index",
      title: "Documentation",
      description: docsIndexDescription,
      priority: 0.9,
      changeFrequency: "monthly",
    },
    ...docRoutes,
    ...staticRoutes,
  ];
}

/** One page's SEO fields, or a thrown error if the route does not exist. */
export function routeByPath(path: string): SiteRoute {
  const route = siteRoutes().find((candidate) => candidate.path === path);
  if (!route) {
    throw new Error(`no SEO route registered for ${path}`);
  }
  return route;
}

/**
 * The metadata object for one route.
 *
 * `title` is left as the bare page title so the root layout's template adds
 * `· ccresu` exactly once; the Open Graph and Twitter titles are pre-composed
 * with `fullTitle()` because those tags do not pass through the template. The
 * canonical is absolute because the base path would otherwise be lost, and
 * `languages` publishes the same URL as `en` and `x-default` — the site is
 * single-language, and saying so explicitly is what keeps a crawler from
 * treating `/ccresu/` and `/ccresu/en/` as alternatives it should guess between.
 */
export function pageMetadata(route: SiteRoute): Metadata {
  const url = absoluteUrl(route.path);
  const title = fullTitle(route.title);

  return {
    title: route.kind === "home" ? { absolute: route.title } : route.title,
    description: route.description,
    alternates: {
      canonical: url,
      languages: { en: url, "x-default": url },
    },
    openGraph: {
      type: route.kind === "doc" ? "article" : "website",
      url,
      siteName: site.name,
      locale: "en",
      title,
      description: route.description,
    },
    twitter: {
      card: "summary",
      title,
      description: route.description,
    },
  };
}

/**
 * Metadata for a documentation page, from that page's own content.
 *
 * A missing slug is a programming error, not a 404: the 10 doc routes are a
 * closed set generated from `lib/docs`.
 */
export function docMetadata(slug: string, version?: string): Metadata {
  const doc = getDoc(slug, version);
  if (!doc) {
    throw new Error(`no documentation page for slug ${slug}`);
  }
  return pageMetadata(routeByPath(`/docs/${doc.slug}/`));
}

/** Metadata for one of the three non-documentation pages (About/Terms/Privacy). */
export function staticPageMetadata(href: string): Metadata {
  return pageMetadata(routeByPath(`${href.replace(/\/$/, "")}/`));
}

type JsonLd = Record<string, unknown>;

/** The licence as structured data, in the form schema.org expects (a URL). */
export const licenseUrl = "https://opensource.org/license/mit/";

/**
 * `SoftwareApplication` for the tool itself.
 *
 * The version is the one `lib/release.ts` resolved at build time, so the
 * structured data and the install command on the page can never disagree. The
 * download URL points at a real release asset for the current version rather
 * than at the repository root, which is what a "download" link means.
 */
export function softwareJsonLd(version: string): JsonLd {
  return {
    "@context": "https://schema.org",
    "@type": "SoftwareApplication",
    name: site.name,
    applicationCategory: "DeveloperApplication",
    applicationSubCategory: "Session recovery CLI",
    operatingSystem: "Linux, macOS, Windows",
    description: site.description,
    softwareVersion: version,
    license: licenseUrl,
    url: homeUrl,
    codeRepository: repo.url,
    downloadUrl: assetUrl(version, "linux", "amd64"),
    releaseNotes: repo.releases,
    programmingLanguage: "Go",
    isAccessibleForFree: true,
    offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
    author: {
      "@type": "Person",
      name: repo.owner,
      url: `https://github.com/${repo.owner}`,
    },
  };
}

/** `WebSite` for the documentation site, with the site's own name and URL. */
export function websiteJsonLd(): JsonLd {
  return {
    "@context": "https://schema.org",
    "@type": "WebSite",
    name: site.name,
    alternateName: `${site.name} documentation`,
    url: homeUrl,
    description: site.description,
    inLanguage: "en",
    publisher: { "@type": "Person", name: repo.owner, url: `https://github.com/${repo.owner}` },
  };
}

/** A breadcrumb trail; `items` are in reading order, home first. */
export function breadcrumbJsonLd(items: ReadonlyArray<{ name: string; path: string }>): JsonLd {
  return {
    "@context": "https://schema.org",
    "@type": "BreadcrumbList",
    itemListElement: items.map((item, index) => ({
      "@type": "ListItem",
      position: index + 1,
      name: item.name,
      item: absoluteUrl(item.path),
    })),
  };
}

/** `CollectionPage` for the documentation index, which lists rather than explains. */
export function collectionPageJsonLd(route: SiteRoute): JsonLd {
  return {
    "@context": "https://schema.org",
    "@type": "CollectionPage",
    name: fullTitle(route.title),
    description: route.description,
    url: absoluteUrl(route.path),
    inLanguage: "en",
    isPartOf: { "@type": "WebSite", name: site.name, url: homeUrl },
    hasPart: docs.map((doc) => ({
      "@type": "TechArticle",
      headline: doc.title,
      description: doc.summary,
      url: absoluteUrl(`/docs/${doc.slug}/`),
    })),
  };
}

/**
 * `TechArticle` for a documentation page.
 *
 * `isPartOf` names the `WebSite` and `inLanguage` matches `<html lang>`, which
 * is what tells a crawler these ten pages are one English documentation set
 * rather than ten unrelated articles.
 */
export function techArticleJsonLd(route: SiteRoute): JsonLd {
  const url = absoluteUrl(route.path);
  return {
    "@context": "https://schema.org",
    "@type": "TechArticle",
    headline: route.title,
    description: route.description,
    url,
    mainEntityOfPage: { "@type": "WebPage", "@id": url },
    inLanguage: "en",
    isPartOf: { "@type": "WebSite", name: site.name, url: homeUrl },
    author: { "@type": "Person", name: repo.owner, url: `https://github.com/${repo.owner}` },
    publisher: { "@type": "Person", name: repo.owner, url: `https://github.com/${repo.owner}` },
    license: licenseUrl,
  };
}

/** `WebPage` for About/Terms/Privacy, which are pages rather than articles. */
export function webPageJsonLd(route: SiteRoute): JsonLd {
  return {
    "@context": "https://schema.org",
    "@type": "WebPage",
    name: fullTitle(route.title),
    description: route.description,
    url: absoluteUrl(route.path),
    inLanguage: "en",
    isPartOf: { "@type": "WebSite", name: site.name, url: homeUrl },
  };
}
