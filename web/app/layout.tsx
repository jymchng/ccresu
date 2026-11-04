import type { Metadata } from "next";

import { SiteFooter } from "@/components/site-footer";
import { SiteHeader } from "@/components/site-header";
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
  keywords: [
    "Claude Code",
    "usage limit",
    "session resume",
    "developer tooling",
    "ccresu",
  ],
  openGraph: {
    type: "website",
    url: site.url,
    siteName: site.name,
    title: `${site.name} \u2014 ${site.tagline}`,
    description: site.description,
  },
  twitter: {
    card: "summary",
    title: `${site.name} \u2014 ${site.tagline}`,
    description: site.description,
  },
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
      <body className="flex min-h-screen flex-col">
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
