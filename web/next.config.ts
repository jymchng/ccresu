import type { NextConfig } from "next";

/**
 * The site is a static export published on GitHub Pages.
 *
 * `basePath` matters: GitHub Pages serves a project site from
 * https://<owner>.github.io/ccresu, so every asset and `next/link` href has to
 * carry the prefix. `trailingSlash` keeps the export compatible with the
 * static file server Pages uses (each route becomes <route>/index.html).
 */
const basePath = "/ccresu";

const nextConfig: NextConfig = {
  output: "export",
  basePath,
  trailingSlash: true,
  reactStrictMode: true,
  images: { unoptimized: true },
  env: { NEXT_PUBLIC_BASE_PATH: basePath },
};

export default nextConfig;
