import { chromium } from "playwright";

/**
 * Real-browser proof that the phone navigation works.
 *
 * The Vitest suite covers the same behaviours in jsdom, but jsdom never lays
 * anything out: `size-11` could be a typo and every test would still pass. This
 * measures the boxes and the document width in Chromium against the built
 * `out/` export.
 *
 * The export uses `basePath: "/ccresu"`, so it has to be served under that
 * prefix (any static server with `out/` mounted at `/ccresu`) or every
 * stylesheet 404s and the page renders unstyled — which is exactly the mistake
 * this script made the first time it ran.
 */
const base = process.env.CCRESU_SITE_BASE ?? "http://127.0.0.1:8982/ccresu";

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 375, height: 720 } });
const results = [];

function check(name, ok, detail = "") {
  results.push({ name, ok });
  console.log(`${ok ? "PASS" : "FAIL"} ${name}${detail ? ` — ${detail}` : ""}`);
}

const headerTrigger = page.locator("header button[aria-label$='navigation']");
const docsTrigger = page.locator("button[aria-label$='documentation menu']");

const overflow = () =>
  page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));

await page.goto(`${base}/docs/installation/`, { waitUntil: "networkidle" });

const wide = await overflow();
check("no horizontal overflow on /docs/installation/ at 375px", wide.scrollWidth <= wide.innerWidth, `${wide.scrollWidth} <= ${wide.innerWidth}`);

const headerBox = await headerTrigger.boundingBox();
check("header menu trigger is at least 44x44", headerBox.height >= 44 && headerBox.width >= 44, JSON.stringify(headerBox));
check("header menu starts closed", (await headerTrigger.getAttribute("aria-expanded")) === "false");

const panelId = await headerTrigger.getAttribute("aria-controls");
const panel = page.locator(`[id="${panelId}"]`);
check("panel exists while closed, so aria-controls resolves", (await panel.count()) === 1);
check("panel is hidden while closed", !(await panel.isVisible()));

await headerTrigger.click();
check("header menu opens", (await headerTrigger.getAttribute("aria-expanded")) === "true");
check("panel is visible when open", await panel.isVisible());

const link = panel.getByRole("link", { name: "Documentation" });
const linkBox = await link.boundingBox();
check("menu links are at least 44px tall", linkBox.height >= 44, JSON.stringify(linkBox));
check("menu link is not clipped by the viewport", linkBox.x >= 0 && linkBox.x + linkBox.width <= 375, JSON.stringify(linkBox));

await page.getByTestId("disclosure-backdrop").click({ position: { x: 5, y: 600 } });
check("a tap outside closes the menu", (await headerTrigger.getAttribute("aria-expanded")) === "false");
check("panel is hidden again", !(await panel.isVisible()));

await headerTrigger.click();
await page.keyboard.press("Escape");
check("Escape closes the menu", (await headerTrigger.getAttribute("aria-expanded")) === "false");
check(
  "Escape puts focus back on the trigger",
  (await page.evaluate(() => document.activeElement?.getAttribute("aria-label"))) === "Open navigation",
  await page.evaluate(() => document.activeElement?.getAttribute("aria-label") ?? "none"),
);

const docsBox = await docsTrigger.boundingBox();
check("docs menu trigger is at least 44px tall", docsBox.height >= 44, JSON.stringify(docsBox));
check("docs menu starts closed", (await docsTrigger.getAttribute("aria-expanded")) === "false");

await docsTrigger.click();
const docsPanelId = await docsTrigger.getAttribute("aria-controls");
const docsLinks = page.locator(`[id="${docsPanelId}"] a`);
// The desktop sidebar renders the same DocsNav, so it is the honest reference
// for "every page" — a hardcoded count would drift the moment a page is added.
const expectedDocs = await page.locator("aside nav[aria-label='Documentation'] a").count();
check("docs menu lists every page the sidebar does", (await docsLinks.count()) === expectedDocs, `${await docsLinks.count()} of ${expectedDocs} links`);
const docsLinkBox = await docsLinks.first().boundingBox();
check("docs menu links are at least 44px tall", docsLinkBox.height >= 44, JSON.stringify(docsLinkBox));
check("docs menu fits the viewport", docsLinkBox.x >= 0 && docsLinkBox.x + docsLinkBox.width <= 375, JSON.stringify(docsLinkBox));

await page.locator(`[id="${docsPanelId}"] a`, { hasText: "How recovery works" }).click();
await page.waitForURL(/\/docs\/recovery\//);
check("choosing a page navigates", page.url().endsWith("/docs/recovery/"), page.url());
check("docs menu is closed on the new page", (await docsTrigger.getAttribute("aria-expanded")) === "false");
const wideRecovery = await overflow();
check("no horizontal overflow on /docs/recovery/ at 375px", wideRecovery.scrollWidth <= wideRecovery.innerWidth, `${wideRecovery.scrollWidth} <= ${wideRecovery.innerWidth}`);

await page.setViewportSize({ width: 1024, height: 800 });
check("header menu trigger is hidden at 1024px", !(await headerTrigger.isVisible()));
check("docs menu control is hidden at 1024px", !(await docsTrigger.isVisible()));
check("desktop docs sidebar is visible at 1024px", await page.locator("aside nav[aria-label='Documentation']").first().isVisible());

await page.setViewportSize({ width: 375, height: 720 });
check("desktop docs sidebar is hidden at 375px", !(await page.locator("aside nav[aria-label='Documentation']").first().isVisible()));
check("docs menu control is visible at 375px", await docsTrigger.isVisible());

for (const width of [320, 414]) {
  await page.setViewportSize({ width, height: 720 });
  const measured = await overflow();
  check(`no horizontal overflow on /docs/recovery/ at ${width}px`, measured.scrollWidth <= measured.innerWidth, `${measured.scrollWidth} <= ${measured.innerWidth}`);
}

await browser.close();
const failed = results.filter((result) => !result.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length === 0 ? 0 : 1);
