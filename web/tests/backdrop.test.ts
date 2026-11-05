import { readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

/**
 * The page backdrop is CSS, so jsdom cannot render it — but it can be read.
 *
 * Two things about a gradient are worth a test rather than an eyeball:
 *
 *  1. **It exists in both themes.** A `--page-gradient` added only to `:root`
 *     would leave the default (dark) theme flat, and the light one is the
 *     exception here, not the rule.
 *  2. **It does not cost contrast.** A gradient is a background wash that every
 *     muted label sits on, so "it looks tasteful" is not the bar: the lightest
 *     composite of every layer has to keep `--muted-foreground` readable. The
 *     test composites the worst case — both radial glows at their strongest,
 *     over each linear stop, plus the 5% veil the banded sections add — and
 *     computes WCAG contrast, so raising an alpha until the page looks richer
 *     fails here before it ships.
 */

const webRoot = path.resolve(import.meta.dirname, "..");
const css = readFileSync(path.join(webRoot, "app", "globals.css"), "utf8");
const layout = readFileSync(path.join(webRoot, "app", "layout.tsx"), "utf8");

/** The declarations of one selector block, `{` … matching `}` not required. */
function block(selector: string): string {
  const start = css.indexOf(`\n${selector} {`);
  expect(start, `${selector} block`).toBeGreaterThan(-1);
  const open = css.indexOf("{", start);
  const close = css.indexOf("\n}", open);
  return css.slice(open + 1, close);
}

function custom(scope: string, name: string): string {
  const match = new RegExp(`--${name}:\\s*([^;]+);`).exec(block(scope));
  expect(match, `${scope} --${name}`).not.toBeNull();
  return (match as RegExpExecArray)[1].trim();
}

const root = ":root";
const dark = ".dark";

const themes = [
  { name: "light", scope: root },
  { name: "dark", scope: dark },
] as const;

type Rgb = [number, number, number];

function hex(value: string): Rgb {
  const clean = value.replace("#", "");
  return [
    Number.parseInt(clean.slice(0, 2), 16),
    Number.parseInt(clean.slice(2, 4), 16),
    Number.parseInt(clean.slice(4, 6), 16),
  ];
}

function channels(value: string): Rgb {
  const parts = value.split(/\s+/).map(Number);
  expect(parts).toHaveLength(3);
  return parts as Rgb;
}

function linear(channel: number): number {
  const c = channel / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance([r, g, b]: Rgb): number {
  return 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b);
}

function contrast(a: Rgb, b: Rgb): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** Source-over compositing of `top` at `alpha` onto `base`. */
function over(base: Rgb, top: Rgb, alpha: number): Rgb {
  return base.map((channel, index) =>
    Math.round(channel * (1 - alpha) + top[index] * alpha),
  ) as Rgb;
}

describe("page backdrop", () => {
  it("defines a gradient for both themes, and they are not the same gradient", () => {
    const light = custom(root, "page-gradient");
    const darkGradient = custom(dark, "page-gradient");

    for (const [name, value] of [
      ["light", light],
      ["dark", darkGradient],
    ] as const) {
      // Three layers: two radial glows over a vertical blend.
      expect(value, `${name} layers`).toMatch(/radial-gradient/);
      expect(value.match(/radial-gradient/g) ?? []).toHaveLength(2);
      expect(value, `${name} ground`).toMatch(/linear-gradient/);
      // ...built from the theme's own tokens, so a palette change carries.
      expect(value, `${name} background token`).toContain("var(--background)");
    }

    expect(light).not.toBe(darkGradient);
    expect(light).toContain("var(--page-top)");
    expect(darkGradient).toContain("var(--page-top)");
  });

  it("is applied to the page shell as one continuous, non-fixed wash", () => {
    // The class exists, points at the variable, and stretches once rather than
    // repeating per section.
    const rule = /\.page-backdrop \{([^}]+)\}/.exec(css);
    expect(rule, ".page-backdrop rule").not.toBeNull();
    const body = (rule as RegExpExecArray)[1];

    expect(body).toContain("background-image: var(--page-gradient)");
    // Per-layer sizing: pixel-sized glows pinned to the top of the page, one
    // stretched wash for the ground.
    expect(body).toContain("background-size: 1100px 620px, 900px 560px, 100% 100%");
    expect(body).toContain("background-position: 50% -150px, 100% -140px, 0 0");
    expect(body).toContain("background-repeat: no-repeat");
    // iOS ignores `fixed` and other engines repaint on every scroll frame.
    expect(body).not.toContain("background-attachment: fixed");
    // No rule may declare it (the comment above mentions it by name).
    expect(css).not.toMatch(/^\s*background-attachment:\s*fixed/m);

    // The shell names it, and the shell is the element that owns the viewport.
    expect(layout).toContain('className="page-backdrop flex min-h-screen flex-col"');
    expect(layout).toContain('import "./globals.css"');
    // Nothing on <body> may paint an opaque background over the gradient.
    expect(layout).not.toMatch(/<body[^>]*bg-background/);
  });

  it("maps every theme colour through an alpha-aware channel", () => {
    // Tailwind v3 silently drops `bg-muted/40` when the colour is a bare
    // `var(--muted)`. That is not a style preference: with the bare form the
    // sticky header had no background at all and table headers no tint, while
    // every test passed. The `<alpha-value>` form is what makes the opacity
    // utilities exist.
    const config = readFileSync(path.join(webRoot, "tailwind.config.ts"), "utf8");
    for (const name of [
      "background",
      "foreground",
      "card",
      "muted",
      "muted-foreground",
      "primary",
      "secondary",
      "accent",
      "border",
      "ring",
    ]) {
      expect(config, `${name} mapping`).toContain(
        `"rgb(var(--${name}-rgb) / <alpha-value>)"`,
      );
    }
    expect(config).not.toMatch(/`"var\(/);
    // ...and each of those channel variables is defined in both themes.
    for (const theme of themes) {
      for (const name of ["background", "foreground", "muted", "muted-foreground", "primary", "border"]) {
        const value = custom(theme.scope, `${name}-rgb`);
        expect(value.split(/\s+/), `${theme.name} --${name}-rgb`).toHaveLength(3);
      }
    }
  });

  it("draws the top glow once, from the page, not again inside the hero", () => {
    // The hero's own `primary/12%` radial stacked with the page glow and made
    // the first screen twice as tinted as the rest of the page.
    const hero = readFileSync(path.join(webRoot, "components", "hero.tsx"), "utf8");
    expect(hero).not.toContain("radial-gradient");
  });

  it("keeps the wash behind the bands instead of restarting it per section", () => {
    // The section fills were `bg-muted/20`, an opaque-ish neutral that cut the
    // gradient off at every border. They are now a 5% veil, which keeps the
    // grouping cue without replacing the colour underneath.
    const banded = ["components/site-footer.tsx", "components/how-it-works.tsx", "components/landing-page.tsx"];
    for (const file of banded) {
      const source = readFileSync(path.join(webRoot, file), "utf8");
      expect(source, `${file} band`).toContain("bg-foreground/5");
      expect(source, `${file} leftover flat band`).not.toContain("bg-muted/20");
    }
  });

  it("keeps text contrast on the lightest composite of every gradient layer", () => {
    for (const theme of themes) {
      const glow = channels(custom(theme.scope, "page-glow"));
      const glow2 = channels(custom(theme.scope, "page-glow-2"));
      const gradient = custom(theme.scope, "page-gradient");

      // The strongest alpha each glow reaches in this theme.
      const alpha = (variable: string) => {
        const match = new RegExp(`rgb\\(var\\(--${variable}\\) / ([\\d.]+)%\\)`).exec(
          gradient,
        );
        expect(match, `${theme.name} --${variable} alpha`).not.toBeNull();
        return Number((match as RegExpExecArray)[1]) / 100;
      };

      const foreground = channels(custom(theme.scope, "foreground-rgb"));
      const muted = channels(custom(theme.scope, "muted-foreground-rgb"));

      for (const stop of ["page-top", "page-bottom"] as const) {
        const ground = hex(custom(theme.scope, stop));

        // Worst case: the page ground, both glows at full strength, then the
        // 5% section veil (which composes the foreground colour in).
        const washed = over(over(over(ground, glow2, alpha("page-glow-2")), glow, alpha("page-glow")), foreground, 0.05);

        const mutedRatio = contrast(washed, muted);
        const foregroundRatio = contrast(washed, foreground);

        expect(
          mutedRatio,
          `${theme.name} muted-foreground on ${stop} (${washed.join(",")})`,
        ).toBeGreaterThanOrEqual(4.5);
        expect(
          foregroundRatio,
          `${theme.name} foreground on ${stop} (${washed.join(",")})`,
        ).toBeGreaterThanOrEqual(7);
      }
    }
  });
});
