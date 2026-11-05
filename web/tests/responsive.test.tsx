import { render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

import { CopyButton } from "@/components/copy-button";
import { DocBlocks } from "@/components/doc-blocks";
import { InstallCommands } from "@/components/install-commands";
import { SiteFooter } from "@/components/site-footer";
import { Button, buttonVariants } from "@/components/ui/button";
import type { Block } from "@/lib/docs/types";
import { fallbackReleaseVersion } from "@/lib/site";

/**
 * The parts of "usable at 320px" that markup can prove.
 *
 * jsdom has no layout engine, so these assertions are about the classes that
 * produce the layout: a 44px floor on controls, a table that scrolls inside its
 * own labelled region instead of widening the page, and prose that can break a
 * long URL. The measurements themselves live in `tests/mobile/`.
 */

describe("small-screen ergonomics", () => {
  it("gives buttons a 44px height by default", () => {
    expect(buttonVariants({ size: "default" })).toContain("h-11");
    expect(buttonVariants({ size: "sm" })).toContain("h-11");
    expect(buttonVariants({ size: "icon" })).toContain("size-11");

    render(<Button>Get started</Button>);
    expect(screen.getByRole("button", { name: "Get started" }).className).toContain(
      "h-11",
    );
  });

  it("renders footer links as 44px rows", () => {
    const { container } = render(<SiteFooter />);
    const links = Array.from(container.querySelectorAll("a"));

    expect(links.length).toBeGreaterThan(0);
    for (const link of links) {
      expect(link.className).toContain("min-h-11");
    }
  });

  it("gives the copy button a 44px target", () => {
    render(<CopyButton value="ccresu version" />);
    const button = screen.getByRole("button", { name: /copy to clipboard/i });

    expect(button.className).toContain("min-h-11");
    expect(button.className).toContain("min-w-11");
  });

  it("gives the install platform tabs a 44px target", () => {
    render(<InstallCommands version={fallbackReleaseVersion} />);
    const tabs = screen.getAllByRole("tab");

    expect(tabs.length).toBeGreaterThan(0);
    for (const tab of tabs) {
      expect(tab.className).toContain("min-h-11");
    }
  });

  it("scrolls a wide table in a labelled region instead of widening the page", () => {
    const blocks: Block[] = [
      {
        kind: "table",
        head: ["Flag", "Meaning"],
        rows: [["--dry-run", "Print the plan and change nothing at all."]],
      },
    ];
    render(<DocBlocks blocks={blocks} />);

    const region = screen.getByRole("region", { name: "Flag, Meaning" });
    expect(region.className).toContain("overflow-x-auto");
    // Focusable, so a keyboard user can scroll it too.
    expect(region).toHaveAttribute("tabindex", "0");
    expect(screen.getByRole("table")).toBeInTheDocument();
    // The hint is only useful where the table actually has to scroll.
    expect(
      screen.getByText(/swipe the table sideways/i).className,
    ).toContain("sm:hidden");
  });

  it("lets a long word break rather than push the page wide", () => {
    const blocks: Block[] = [
      {
        kind: "p",
        text: "Releases live at https://github.com/jymchng/ccresu/releases and nowhere else.",
      },
    ];
    const { container } = render(<DocBlocks blocks={blocks} />);

    const spans = Array.from(container.querySelectorAll("span"));
    expect(spans.length).toBeGreaterThan(0);
    for (const span of spans) {
      expect(span.className).toContain("[overflow-wrap:anywhere]");
    }
  });

  it("keeps the sticky header from covering an anchored heading", () => {
    const css = readFileSync(
      path.join(import.meta.dirname, "..", "app", "globals.css"),
      "utf8",
    );
    const htmlRule = css.slice(css.indexOf("  html {"), css.indexOf("  body {"));

    expect(htmlRule).toContain("scroll-padding-top");
  });
});
