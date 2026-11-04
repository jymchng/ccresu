/**
 * `next/link` normalises a trailing slash away from the rendered `href`, while
 * the static export writes `<route>/index.html`. Comparisons therefore ignore a
 * trailing slash, which is what a browser and GitHub Pages both do.
 */
export function normalizeHref(href: string): string {
  if (href.length > 1 && href.endsWith("/")) {
    return href.slice(0, -1);
  }
  return href;
}
