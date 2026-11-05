#!/usr/bin/env node
import { chromium } from "playwright";
import { createReadStream, existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import path from "node:path";

/**
 * The real-browser gate: `npm run test:mobile`.
 *
 * Everything else in the suite is a claim about markup. jsdom has no layout
 * engine, so `h-11` could be a typo, a `min-w-0` could be missing, and every
 * Vitest test would still go green. This walks the built export in Chromium and
 * measures the page the way a phone does, then keeps the pictures.
 *
 * What it asserts, per route and width:
 *
 *  1. `document.scrollWidth <= innerWidth` — nothing widens the page. Elements
 *     clipped by an ancestor with `overflow-x: auto|scroll|hidden` are exempt:
 *     they are reachable, they just scroll.
 *  2. every visible `button`, `[role=tab]`, `input`, `select` and non-inline
 *     link is at least 44x44 CSS pixels. `sr-only` skip links (1px until
 *     focused) and links inside prose are text, not targets, so they are
 *     excluded by the same rule the audit uses.
 *  3. on phone widths, the header menu and the docs menu really open,
 *     navigate, and close — `aria-expanded` flips, the panel appears, the link
 *     count matches the desktop sidebar, choosing a page lands on that page
 *     with the panel closed, and Escape closes it and hands focus back.
 *
 * It then writes a full-page screenshot per route at 320px and 1024px plus the
 * two menu-open states, and a JSON report with a SHA-256 per image, so the
 * evidence is inspectable instead of asserted.
 *
 * Usage:
 *   npm run test:mobile               # builds if needed, serves out/, checks
 *   CCRESU_SITE_BASE=https://... npm run test:mobile   # check a live site
 *
 * The export uses `basePath: "/ccresu"`, so a bare static server rooted at
 * `out/` serves every asset from the wrong URL and the pages arrive unstyled —
 * which looks like a layout bug rather than a serving bug. The built-in server
 * mounts `out/` under `/ccresu` for exactly that reason.
 */

const ROUTES = [
  "/",
  "/about/",
  "/docs/",
  "/docs/cli/",
  "/docs/configuration/",
  "/docs/deployment/",
  "/docs/installation/",
  "/docs/limitations/",
  "/docs/recovery/",
  "/docs/security/",
  "/docs/telegram/",
  "/docs/troubleshooting/",
  "/docs/tutorial/",
  "/privacy/",
  "/terms/",
];

const WIDTHS = [320, 375, 414, 768, 1024];
/** Below `sm` the header nav collapses into the menu button. */
const HEADER_MENU_WIDTHS = [320, 375, 414];
/** Below `lg` the docs sidebar is `hidden lg:block`, so the docs menu must exist. */
const DOCS_MENU_WIDTHS = [320, 375, 414, 768];
const MIN_TARGET = 44;
const DOC_ROUTES = ROUTES.filter((route) => route.startsWith("/docs/"));

const webRoot = path.resolve(import.meta.dirname, "..", "..");
const outDir = path.join(webRoot, "out");
const artifactDir = path.join(import.meta.dirname, "artifacts");
const shotDir = path.join(artifactDir, "screenshots");

const failures = [];
const checks = { overflow: 0, targets: 0, menu: 0, backdrop: 0, legibility: 0, seo: 0 };

function fail(subject, detail) {
  failures.push({ subject, detail });
  console.log(`FAIL ${subject} — ${detail}`);
}

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".ico": "image/x-icon",
  ".txt": "text/plain; charset=utf-8",
  ".woff2": "font/woff2",
  ".woff": "font/woff",
  ".map": "application/json",
};

/** Serve `out/` under `/ccresu`, the way GitHub Pages will. */
function serveExport(root, prefix) {
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://127.0.0.1");
    let pathname = decodeURIComponent(url.pathname);
    if (!pathname.startsWith(prefix)) {
      res.writeHead(404).end("outside the export");
      return;
    }
    let rel = pathname.slice(prefix.length);
    if (rel === "" || rel.endsWith("/")) rel += "index.html";

    let file = path.normalize(path.join(root, rel));
    if (!file.startsWith(root)) {
      res.writeHead(403).end("outside the export");
      return;
    }
    if (!existsSync(file) && existsSync(`${file}.html`)) file = `${file}.html`;
    if (!existsSync(file) || statSync(file).isDirectory()) {
      res.writeHead(404).end("not found");
      return;
    }
    res.writeHead(200, { "content-type": MIME[path.extname(file)] ?? "application/octet-stream" });
    createReadStream(file).pipe(res);
  });

  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      resolve({ base: `http://127.0.0.1:${port}${prefix}`, close: () => server.close() });
    });
  });
}

/** The measurement itself, run inside the page. */
function measurePage() {
  const inner = window.innerWidth;
  const scrollWidth = document.documentElement.scrollWidth;

  const clippedByScroller = (el) => {
    let parent = el.parentElement;
    while (parent) {
      const style = getComputedStyle(parent);
      if (["auto", "scroll", "hidden"].includes(style.overflowX)) return true;
      parent = parent.parentElement;
    }
    return false;
  };

  const offenders = [];
  for (const el of document.querySelectorAll("body *")) {
    const rect = el.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) continue;
    if (rect.right > inner + 1 || rect.left < -1) {
      if (clippedByScroller(el)) continue;
      offenders.push({
        tag: el.tagName.toLowerCase(),
        cls: String(el.className).slice(0, 70),
        left: Math.round(rect.left),
        right: Math.round(rect.right),
        text: (el.textContent ?? "").trim().slice(0, 40),
      });
    }
  }

  const smallTargets = [];
  for (const el of document.querySelectorAll("button, [role='tab'], a, input, select")) {
    const style = getComputedStyle(el);
    if (style.display === "none" || style.visibility === "hidden") continue;
    const rect = el.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) continue;
    if (el.className.includes("sr-only") || (rect.width <= 2 && rect.height <= 2)) continue;
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
}

function slug(route) {
  return route === "/" ? "home" : route.replace(/^\/|\/$/g, "").replace(/\//g, "-");
}

async function main() {
  if (!existsSync(outDir) && !process.env.CCRESU_SITE_BASE) {
    console.log("no out/ export found — run `npm run build` first (or set CCRESU_SITE_BASE)");
    process.exit(1);
  }

  const server = process.env.CCRESU_SITE_BASE
    ? { base: process.env.CCRESU_SITE_BASE.replace(/\/$/, ""), close: () => {} }
    : await serveExport(outDir, "/ccresu");

  console.log(`serving ${process.env.CCRESU_SITE_BASE ? "" : "out/ "}at ${server.base}`);
  mkdirSync(shotDir, { recursive: true });

  const browser = await chromium.launch();
  const page = await browser.newPage();
  const shots = [];
  const shot = async (name, fullPage) => {
    const file = path.join(shotDir, `${name}.png`);
    await page.screenshot({ path: file, fullPage });
    const bytes = statSync(file).size;
    shots.push({ name, file: path.relative(webRoot, file), bytes, sha256: sha256(file) });
  };

  // 1 + 2: every route at every width. One full-page capture per breakpoint
  // the design actually branches at.
  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 800 });
    for (const route of ROUTES) {
      await page.goto(`${server.base}${route}`, { waitUntil: "networkidle" });
      const report = await page.evaluate(measurePage);

      if (report.scrollWidth > report.inner) {
        fail(
          `${route} @ ${width}px`,
          `document is ${report.scrollWidth}px wide in a ${report.inner}px viewport` +
            report.offenders.map((o) => `\n       ${o.tag} [${o.cls}] ${o.left}..${o.right} "${o.text}"`).join(""),
        );
      } else {
        checks.overflow += 1;
      }

      if (report.smallTargets.length > 0) {
        fail(
          `${route} @ ${width}px`,
          `${report.smallTargets.length} target(s) under ${MIN_TARGET}x${MIN_TARGET}` +
            report.smallTargets.map((t) => `\n       ${t.tag} "${t.label}" ${t.w}x${t.h}`).join(""),
        );
      } else {
        checks.targets += 1;
      }

      if (width === 320 || width === 1024) await shot(`${width}-${slug(route)}`, true);
    }
  }

  const headerTrigger = () => page.locator("header button[aria-label$='navigation']");
  const docsTrigger = () => page.locator("button[aria-label$='documentation menu']");
  const expanded = (locator) => locator.getAttribute("aria-expanded");
  const panelOf = async (locator) => {
    const id = await locator.getAttribute("aria-controls");
    return page.locator(`[id="${id}"]`);
  };

  // 3a: the header menu, on every route, below `sm` — the width range where it
  // is the only navigation. Above `sm` the nav is inline and the button is gone.
  for (const width of HEADER_MENU_WIDTHS) {
    await page.setViewportSize({ width, height: 800 });
    for (const route of ROUTES) {
      await page.goto(`${server.base}${route}`, { waitUntil: "networkidle" });
      const trigger = headerTrigger();
      if (!(await trigger.isVisible())) {
        fail(`${route} @ ${width}px`, "the header menu trigger is not visible");
        continue;
      }
      const panel = await panelOf(trigger);
      if ((await expanded(trigger)) !== "false") {
        fail(`${route} @ ${width}px`, "the header menu does not start closed");
      }
      if (await panel.isVisible()) {
        fail(`${route} @ ${width}px`, "the header menu panel is visible while closed");
      }

      await trigger.click();
      if ((await expanded(trigger)) !== "true") {
        fail(`${route} @ ${width}px`, "the header menu did not report aria-expanded=true when opened");
      } else if (!(await panel.isVisible())) {
        fail(`${route} @ ${width}px`, "the header menu panel is not visible when open");
      }
      const link = panel.getByRole("link", { name: "Documentation" });
      const box = await link.boundingBox();
      if (!box || box.height < MIN_TARGET) {
        fail(`${route} @ ${width}px`, `a header menu link is ${box ? `${Math.round(box.width)}x${Math.round(box.height)}` : "missing"}`);
      }
      if (!box || box.x < 0 || box.x + box.width > width) {
        fail(`${route} @ ${width}px`, "a header menu link is clipped by the viewport");
      }
      if (width === 375) await shot(`menu-open-header-${slug(route)}`, false);

      await link.click();
      await page.waitForURL(/\/docs\/$/);
      if ((await expanded(headerTrigger())) !== "false") {
        fail(`${route} @ ${width}px`, "the header menu stayed open after choosing Documentation");
      }

      // Escape closes it and gives focus back to the button that opened it.
      const escapeTrigger = headerTrigger();
      await escapeTrigger.click();
      await page.keyboard.press("Escape");
      if ((await expanded(escapeTrigger)) !== "false") {
        fail(`${route} @ ${width}px`, "Escape did not close the header menu");
      } else if ((await page.evaluate(() => document.activeElement?.getAttribute("aria-label"))) !== "Open navigation") {
        fail(`${route} @ ${width}px`, "Escape did not restore focus to the header menu trigger");
      }

      checks.menu += 1;
    }
  }

  // 3b: the docs menu, on every doc route, below `lg` — where the sidebar is
  // `hidden lg:block` and the page list would otherwise be unreachable.
  for (const width of DOCS_MENU_WIDTHS) {
    await page.setViewportSize({ width, height: 800 });
    for (const route of DOC_ROUTES) {
      await page.goto(`${server.base}${route}`, { waitUntil: "networkidle" });
      const trigger = docsTrigger();
      if (!(await trigger.isVisible())) {
        fail(`${route} @ ${width}px`, "the docs menu trigger is not visible");
        continue;
      }
      if (await page.locator("aside nav[aria-label='Documentation']").isVisible()) {
        fail(`${route} @ ${width}px`, "the desktop sidebar is visible below lg as well as the docs menu");
      }

      // The honest reference for "every page" is the sidebar's own list, so
      // adding a doc page cannot leave this assertion quietly stale.
      const sidebarLinks = await page.locator("aside nav[aria-label='Documentation'] a").count();
      const panel = await panelOf(trigger);
      await trigger.click();
      if ((await expanded(trigger)) !== "true") {
        fail(`${route} @ ${width}px`, "the docs menu did not report aria-expanded=true when opened");
      }
      const panelLinks = await panel.locator("a").count();
      if (panelLinks !== sidebarLinks) {
        fail(`${route} @ ${width}px`, `the docs menu lists ${panelLinks} pages, the sidebar lists ${sidebarLinks}`);
      }
      const firstBox = await panel.locator("a").first().boundingBox();
      if (!firstBox || firstBox.height < MIN_TARGET) {
        fail(`${route} @ ${width}px`, `a docs menu link is ${firstBox ? `${Math.round(firstBox.width)}x${Math.round(firstBox.height)}` : "missing"}`);
      }
      if (width === 375) await shot(`menu-open-docs-${slug(route)}`, false);

      await panel.getByRole("link", { name: "How recovery works" }).click();
      await page.waitForURL(/\/docs\/recovery\/$/);
      await docsTrigger().waitFor();
      if ((await expanded(docsTrigger())) !== "false") {
        fail(`${route} @ ${width}px`, "the docs menu stayed open after choosing a page");
      }

      const escapeTrigger = docsTrigger();
      await escapeTrigger.click();
      await page.keyboard.press("Escape");
      if ((await expanded(escapeTrigger)) !== "false") {
        fail(`${route} @ ${width}px`, "Escape did not close the docs menu");
      } else if ((await page.evaluate(() => document.activeElement?.getAttribute("aria-label"))) !== "Open documentation menu") {
        fail(`${route} @ ${width}px`, "Escape did not restore focus to the docs menu trigger");
      }

      checks.menu += 1;
    }
  }

  // 3c: above the breakpoints the same controls must be gone, or the inline nav
  // and the sidebar would be competing with a duplicate menu.
  for (const [width, expectHeaderMenu, expectDocsMenu] of [
    [768, false, true],
    [1024, false, false],
  ]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto(`${server.base}/docs/recovery/`, { waitUntil: "networkidle" });
    if ((await headerTrigger().isVisible()) !== expectHeaderMenu) {
      fail(`/docs/recovery/ @ ${width}px`, `header menu visibility should be ${expectHeaderMenu}`);
    }
    if ((await docsTrigger().isVisible()) !== expectDocsMenu) {
      fail(`/docs/recovery/ @ ${width}px`, `docs menu visibility should be ${expectDocsMenu}`);
    }
    if (width === 1024 && !(await page.locator("aside nav[aria-label='Documentation']").isVisible())) {
      fail(`/docs/recovery/ @ ${width}px`, "the desktop sidebar is not visible at lg");
    }
  }

  // 4: the page backdrop. A gradient is CSS the jsdom suite can only read, so
  // this asserts the *built* stylesheet actually paints one: two radial washes
  // over a vertical blend, on <body>, in both themes, and never with
  // `background-attachment: fixed` (which iOS ignores and other engines
  // repaint on every scroll frame).
  await page.setViewportSize({ width: 1024, height: 800 });
  await page.goto(`${server.base}/`, { waitUntil: "networkidle" });

  const backdrop = async () =>
    page.evaluate(() => {
      const style = getComputedStyle(document.body);
      const image = style.backgroundImage;
      return {
        image,
        radial: (image.match(/radial-gradient/g) ?? []).length,
        linear: (image.match(/linear-gradient/g) ?? []).length,
        attachment: style.backgroundAttachment,
        size: style.backgroundSize,
        repeat: style.backgroundRepeat,
        theme: document.documentElement.classList.contains("dark") ? "dark" : "light",
      };
    });

  const checkBackdrop = (state, label) => {
    if (state.radial !== 2 || state.linear !== 1) {
      fail("page backdrop", `${label}: ${state.radial} radial + ${state.linear} linear layer(s), expected 2 + 1`);
      return false;
    }
    if (state.attachment.includes("fixed")) {
      fail("page backdrop", `${label}: background-attachment is ${state.attachment}`);
      return false;
    }
    // Three layers, each sized explicitly: two pixel-sized glows and one
    // stretched ground. A single `100% 100%` would stretch the glows over the
    // whole document and push them above the fold.
    const sizes = state.size.split(",").map((value) => value.trim());
    if (sizes.length !== 3 || sizes[2] !== "100% 100%" || !sizes[0].includes("px")) {
      fail("page backdrop", `${label}: background-size ${state.size}`);
      return false;
    }
    if (!state.repeat.includes("no-repeat")) {
      fail("page backdrop", `${label}: background-repeat ${state.repeat}`);
      return false;
    }
    return true;
  };

  // 5: the hero's "Local-first watchdog" pill. It was 12px muted-grey text in a
  // transparent hairline pill (5.95:1 in light), which read as washed out on the
  // gradient. It must now be foreground-coloured, at least 14px, and have a fill
  // of its own; the contrast is computed against the theme's own tokens.
  const heroBadge = async () =>
    page.evaluate(() => {
      const el = [...document.querySelectorAll("span")].find(
        (node) => node.textContent.trim() === "Local-first watchdog for Claude Code",
      );
      if (!el) return null;
      const style = getComputedStyle(el);
      const root = getComputedStyle(document.documentElement);
      const hex = (value) => {
        const clean = value.trim().replace("#", "");
        return [
          Number.parseInt(clean.slice(0, 2), 16),
          Number.parseInt(clean.slice(2, 4), 16),
          Number.parseInt(clean.slice(4, 6), 16),
        ];
      };
      const rgb = (value) => (value.match(/[\d.]+/g) ?? []).map(Number).slice(0, 3);
      return {
        color: rgb(style.color),
        muted: (root.getPropertyValue("--muted-foreground-rgb").trim().split(/\s+/) ?? []).map(Number),
        background: (root.getPropertyValue("--background-rgb").trim().split(/\s+/) ?? []).map(Number),
        fontSize: Number.parseFloat(style.fontSize),
        fill: rgb(style.backgroundColor),
        fillAlpha: Number((style.backgroundColor.match(/[\d.]+/g) ?? ["0"])[3] ?? 1),
        theme: document.documentElement.classList.contains("dark") ? "dark" : "light",
      };
    });

  const contrastOf = ([r1, g1, b1], [r2, g2, b2]) => {
    const lum = ([r, g, b]) => {
      const channel = (c) => {
        const value = c / 255;
        return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
      };
      return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
    };
    const [hi, lo] = [lum([r1, g1, b1]), lum([r2, g2, b2])].sort((a, b) => b - a);
    return (hi + 0.05) / (lo + 0.05);
  };

  const checkBadge = (state) => {
    if (!state) {
      fail("hero badge", "the \"Local-first watchdog\" pill was not found");
      return false;
    }
    const problems = [];
    const ratio = contrastOf(state.color, state.background);
    if (state.fontSize < 14) problems.push(`${state.fontSize}px text, expected >= 14px`);
    if (ratio < 7) problems.push(`contrast ${ratio.toFixed(2)}:1 against --background, expected >= 7`);
    if (state.fillAlpha === 0) problems.push("no fill of its own (transparent pill)");
    if (state.color.join() === state.muted.join()) problems.push("still using --muted-foreground");
    if (problems.length > 0) {
      fail("hero badge", `${state.theme} theme: ${problems.join("; ")}`);
      return false;
    }
    return true;
  };

  const firstTheme = await backdrop();
  const firstOk = checkBackdrop(firstTheme, `${firstTheme.theme} theme`);
  const firstBadge = checkBadge(await heroBadge());

  // Toggle the theme: the light and dark washes are separate gradients, so the
  // computed value must change and must still be a gradient.
  await page.locator("header button[aria-label$='theme']").click();

  // Wait for the colour transition to finish rather than guessing a delay. The
  // badge carries `transition-colors`, so for ~150ms after the toggle it is
  // mid-animation between the two themes while the `--*-rgb` custom properties
  // have already flipped — reading then compares a half-way colour against the
  // new background and reports a contrast that is not the one a reader sees.
  // Two identical consecutive samples mean the animation is over.
  let previousColour = null;
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const colour = await page.evaluate(() => {
      const el = [...document.querySelectorAll("span")].find(
        (node) => node.textContent.trim() === "Local-first watchdog for Claude Code",
      );
      return el ? getComputedStyle(el).color : null;
    });
    if (colour !== null && colour === previousColour) {
      break;
    }
    previousColour = colour;
    await page.waitForTimeout(50);
  }

  const secondTheme = await backdrop();
  const secondOk = checkBackdrop(secondTheme, `${secondTheme.theme} theme`);
  const secondBadge = checkBadge(await heroBadge());

  if (firstBadge && secondBadge) checks.legibility += 2;

  if (firstOk && secondOk) {
    if (firstTheme.theme === secondTheme.theme) {
      fail("page backdrop", `the theme toggle did not switch themes (still ${firstTheme.theme})`);
    } else if (firstTheme.image === secondTheme.image) {
      fail("page backdrop", "light and dark resolve to the same gradient");
    } else {
      checks.backdrop += 2;
    }
  }

  // 6: SEO, measured in a browser rather than trusted to the build. Every route
  // must carry its own canonical URL (base path intact), a unique og:title, a
  // description, and JSON-LD a parser accepts — a broken `@graph` is invisible
  // to Vitest but not to `JSON.parse` on the rendered tag.
  await page.setViewportSize({ width: 1024, height: 800 });
  const seoTitles = new Set();
  for (const route of ROUTES) {
    await page.goto(`${server.base}${route}`, { waitUntil: "networkidle" });
    const seo = await page.evaluate(() => {
      const attr = (selector, name) => document.querySelector(selector)?.getAttribute(name) ?? null;
      const blocks = [...document.querySelectorAll('script[type="application/ld+json"]')];
      let jsonLd = null;
      try {
        jsonLd = blocks
          .map((block) => JSON.parse(block.textContent ?? "null"))
          .flatMap((parsed) => (parsed && parsed["@graph"]) || [parsed])
          .map((node) => node?.["@type"] ?? null);
      } catch (error) {
        jsonLd = `parse error: ${error.message}`;
      }
      return {
        canonical: attr('link[rel="canonical"]', "href"),
        ogTitle: attr('meta[property="og:title"]', "content"),
        ogUrl: attr('meta[property="og:url"]', "content"),
        description: attr('meta[name="description"]', "content"),
        twitterTitle: attr('meta[name="twitter:title"]', "content"),
        lang: document.documentElement.getAttribute("lang"),
        h1: document.querySelectorAll("h1").length,
        jsonLd,
      };
    });

    const expected = `https://jymchng.github.io/ccresu${route}`;
    const problems = [];
    if (seo.canonical !== expected) problems.push(`canonical ${seo.canonical}, expected ${expected}`);
    if (seo.ogUrl !== expected) problems.push(`og:url ${seo.ogUrl}`);
    if (!seo.ogTitle) problems.push("no og:title");
    if (!seo.twitterTitle) problems.push("no twitter:title");
    if (!seo.description) problems.push("no description");
    if (seo.lang !== "en") problems.push(`html lang is ${seo.lang}`);
    if (seo.h1 !== 1) problems.push(`${seo.h1} h1 elements`);
    if (!Array.isArray(seo.jsonLd) || seo.jsonLd.length === 0) {
      problems.push(`JSON-LD ${JSON.stringify(seo.jsonLd)}`);
    }
    if (seoTitles.has(seo.ogTitle)) problems.push(`og:title ${seo.ogTitle} is not unique`);
    seoTitles.add(seo.ogTitle);

    if (problems.length > 0) {
      fail(`seo ${route}`, problems.join("; "));
    } else {
      checks.seo += 1;
    }
  }

  // Taken after the toggle, so this is the light wash; the dark one is in
  // every other screenshot in the suite.
  await shot("backdrop-light", false);

  await browser.close();
  server.close();

  const report = {
    base: server.base,
    widths: WIDTHS,
    routes: ROUTES,
    minTarget: MIN_TARGET,
    checksPassed: checks,
    failures,
    screenshots: shots,
    generatedAt: new Date().toISOString(),
  };
  writeFileSync(path.join(artifactDir, "report.json"), `${JSON.stringify(report, null, 2)}\n`);

  console.log(
    `\n${checks.overflow}/${ROUTES.length * WIDTHS.length} route/width pairs fit the viewport` +
      `\n${checks.targets}/${ROUTES.length * WIDTHS.length} route/width pairs have no target under ${MIN_TARGET}x${MIN_TARGET}` +
      `\n${checks.menu}/${DOC_ROUTES.length * DOCS_MENU_WIDTHS.length + ROUTES.length * HEADER_MENU_WIDTHS.length} menu open/navigate/close checks passed` +
      `\n${checks.backdrop}/2 page-backdrop checks passed (gradient applied per theme, not fixed)` +
      `\n${checks.legibility}/2 hero-badge legibility checks passed (theme foreground, >= 14px, own fill)` +
      `\n${checks.seo}/${ROUTES.length} pages carry their own canonical, og/twitter pair, one h1 and parseable JSON-LD` +
      `\n${shots.length} screenshots in web/tests/mobile/artifacts/screenshots/`,
  );
  console.log(failures.length === 0 ? "\nPASS" : `\n${failures.length} failure(s)`);
  process.exit(failures.length === 0 ? 0 : 1);
}

const sha256 = (file) => createHash("sha256").update(readFileSync(file)).digest("hex");

await main();
