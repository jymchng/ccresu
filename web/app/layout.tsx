import type { Metadata } from "next";

import { SiteFooter } from "@/components/site-footer";
import { SiteHeader } from "@/components/site-header";
import { homeUrl } from "@/lib/seo";
import { repo, site } from "@/lib/site";
import { themeBootstrapScript } from "@/lib/theme";

import "./globals.css";

export const metadata: Metadata = {
  metadataBase: new URL(site.url),
  title: {
    default: `${site.name} \u2014 ${site.tagline}`,
    template: `%s \u00b7 ${site.name}`,
  },
  description: site.description,
  applicationName: site.name,
  authors: [{ name: repo.owner, url: repo.url }],
  creator: repo.owner,
  keywords: [
    "Claude Code",
    "usage limit",
    "session resume",
    "developer tooling",
    "ccresu",
  ],
  referrer: "strict-origin-when-cross-origin",
  formatDetection: { telephone: false, address: false, email: false },
  // Every real route overrides these through `lib/seo.pageMetadata()`; the
  // values here are the defaults for the one surface that has no route of its
  // own, the 404 page. They are deliberately the home page's values rather than
  // a second, drifting description of the site.
  openGraph: {
    type: "website",
    url: homeUrl,
    siteName: site.name,
    locale: "en",
    title: `${site.name} \u2014 ${site.tagline}`,
    description: site.description,
  },
  twitter: {
    card: "summary",
    title: `${site.name} \u2014 ${site.tagline}`,
    description: site.description,
  },
  robots: { index: true, follow: true },
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" className="dark" suppressHydrationWarning>
      <head>
        {/* Applies a stored light preference before first paint; dark is the default. */}
        <script dangerouslySetInnerHTML={{ __html: themeBootstrapScript }} />
      </head>
      <body className="page-backdrop flex min-h-screen flex-col">
        <a
          href="#main"
          className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-background focus:px-3 focus:py-2 focus:text-sm focus:shadow"
        >
          Skip to content
        </a>
        <SiteHeader />
        <main id="main" className="flex-1">
          {children}
        </main>
        <SiteFooter />
      </body>
    </html>
  );
}
