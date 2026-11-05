import { chromium } from "playwright";

/**
 * Diagnostic pass over every route at every width the site has to work at.
 *
 * Prints, per route and width: whether the document is wider than the viewport,
 * which elements stick out, and which controls are under 44x44. This is the
 * measuring half; `npm run test:mobile` is the pass/fail gate built on it.
 */
const base = process.env.CCRESU_SITE_BASE ?? "http://127.0.0.1:8982/ccresu";
const widths = [320, 375, 414, 768, 1024];
const routes = process.env.CCRESU_ROUTES
  ? process.env.CCRESU_ROUTES.split(",")
  : ["/", "/docs/", "/docs/cli/", "/docs/configuration/", "/docs/deployment/", "/docs/installation/", "/docs/limitations/", "/docs/recovery/", "/docs/security/", "/docs/telegram/", "/docs/troubleshooting/", "/docs/tutorial/"];

const browser = await chromium.launch();
const page = await browser.newPage();

for (const width of widths) {
  await page.setViewportSize({ width, height: 800 });
  for (const route of routes) {
    await page.goto(`${base}${route}`, { waitUntil: "networkidle" });

    const report = await page.evaluate(() => {
      const inner = window.innerWidth;
      const scrollWidth = document.documentElement.scrollWidth;

      const offenders = [];
      for (const el of document.querySelectorAll("body *")) {
        const rect = el.getBoundingClientRect();
        if (rect.width === 0 && rect.height === 0) continue;
        // An element that is clipped by an ancestor scroller is fine: it is
        // reachable, it just scrolls.
        if (rect.right > inner + 1 || rect.left < -1) {
          const scrolls = (() => {
            let parent = el.parentElement;
            while (parent) {
              const style = getComputedStyle(parent);
              if (style.overflowX === "auto" || style.overflowX === "scroll" || style.overflowX === "hidden") {
                return true;
              }
              parent = parent.parentElement;
            }
            return false;
          })();
          if (!scrolls) {
            offenders.push({
              tag: el.tagName.toLowerCase(),
              cls: String(el.className).slice(0, 70),
              left: Math.round(rect.left),
              right: Math.round(rect.right),
              text: (el.textContent ?? "").trim().slice(0, 40),
            });
          }
        }
      }

      const smallTargets = [];
      for (const el of document.querySelectorAll("button, [role='tab'], a, input, select")) {
        const style = getComputedStyle(el);
        if (style.display === "none" || style.visibility === "hidden") continue;
        const rect = el.getBoundingClientRect();
        if (rect.width === 0 && rect.height === 0) continue;
        // Visually-hidden helpers (the skip link) are 1px by design and grow on
        // focus; they are not touch targets, so they are not measured here.
        if (el.className.includes("sr-only") || (rect.width <= 2 && rect.height <= 2)) continue;
        // Inline links inside prose are text, not tap targets.
        if (el.tagName === "A" && style.display === "inline") continue;
        if (rect.height < 44 || rect.width < 44) {
          smallTargets.push({
            tag: el.tagName.toLowerCase(),
            label: (el.getAttribute("aria-label") ?? el.textContent ?? "").trim().slice(0, 30),
            w: Math.round(rect.width),
            h: Math.round(rect.height),
          });
        }
      }

      return { scrollWidth, inner, offenders: offenders.slice(0, 8), smallTargets: smallTargets.slice(0, 12) };
    });

    const overflow = report.scrollWidth > report.inner;
    const small = report.smallTargets.length > 0;
    if (!overflow && !small) continue;

    console.log(`\n== ${route} @ ${width} ==`);
    if (overflow) {
      console.log(`  OVERFLOW scrollWidth=${report.scrollWidth} inner=${report.inner}`);
      for (const o of report.offenders) {
        console.log(`    ${o.tag} [${o.cls}] ${o.left}..${o.right} "${o.text}"`);
      }
    }
    if (small) {
      console.log(`  SMALL TARGETS (${report.smallTargets.length})`);
      for (const t of report.smallTargets) {
        console.log(`    ${t.tag} "${t.label}" ${t.w}x${t.h}`);
      }
    }
  }
}

await browser.close();
