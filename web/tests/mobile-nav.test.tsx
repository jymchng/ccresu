import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { DocPageView } from "@/components/doc-page-view";
import { SiteHeader } from "@/components/site-header";
import DocsIndexPage from "@/app/docs/page";
import { docs } from "@/lib/docs";
import { fallbackReleaseVersion } from "@/lib/site";

/**
 * The navigation a phone actually gets.
 *
 * Three behaviours are load-bearing and none of them were possible with
 * `<details>/<summary>`: the trigger reports whether the panel is open, Escape
 * closes it and hands focus back to the button, and taking a link closes it so
 * the menu is not left covering the page you just navigated to.
 *
 * jsdom does not run the Tailwind pipeline (see vitest.config.ts), so these
 * assertions are about structure and behaviour, never computed layout: the
 * `hidden` attribute is what the tests read, and it is also what removes the
 * panel from the accessibility tree in a real browser.
 */

function panelFor(trigger: HTMLElement): HTMLElement {
  const id = trigger.getAttribute("aria-controls");
  expect(id).toBeTruthy();

  const panel = document.getElementById(id as string);
  expect(panel).not.toBeNull();
  return panel as HTMLElement;
}

function headerTrigger(): HTMLElement {
  return screen.getByRole("button", { name: /(open|close) navigation/i });
}

describe("mobile navigation", () => {
  it("is a button that reports its state, not a details element", () => {
    const { container } = render(<SiteHeader />);

    expect(container.querySelector("details")).toBeNull();
    expect(container.querySelector("summary")).toBeNull();

    const trigger = headerTrigger();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    // The controlled panel exists while closed, so aria-controls is never dangling.
    expect(panelFor(trigger)).toHaveAttribute("hidden");
  });

  it("opens and closes when the trigger is activated", async () => {
    const user = userEvent.setup();
    render(<SiteHeader />);

    const trigger = headerTrigger();
    const panel = panelFor(trigger);

    await user.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(panel).not.toHaveAttribute("hidden");
    expect(within(panel).getByRole("link", { name: "Documentation" })).toBeInTheDocument();

    await user.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(panel).toHaveAttribute("hidden");
  });

  it("closes on Escape and gives focus back to the trigger", async () => {
    const user = userEvent.setup();
    render(<SiteHeader />);

    const trigger = headerTrigger();
    const panel = panelFor(trigger);

    await user.click(trigger);
    expect(panel).not.toHaveAttribute("hidden");

    await user.keyboard("{Escape}");

    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(panel).toHaveAttribute("hidden");
    // The point of the focus move: a keyboard user does not land back at the
    // top of the document with no idea where they are.
    expect(trigger).toHaveFocus();
  });

  it("closes when a link inside it is taken", async () => {
    const user = userEvent.setup();
    render(<SiteHeader />);

    const trigger = headerTrigger();
    const panel = panelFor(trigger);

    await user.click(trigger);
    const link = within(panel).getByRole("link", { name: "Documentation" });
    await user.click(link);

    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(panel).toHaveAttribute("hidden");
  });

  it("closes when the backdrop is tapped", async () => {
    const user = userEvent.setup();
    render(<SiteHeader />);

    const trigger = headerTrigger();
    const panel = panelFor(trigger);

    await user.click(trigger);
    expect(screen.getByTestId("disclosure-backdrop")).toBeInTheDocument();

    await user.click(screen.getByTestId("disclosure-backdrop"));

    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(panel).toHaveAttribute("hidden");
    expect(screen.queryByTestId("disclosure-backdrop")).toBeNull();
  });

  it("offers 44px targets in the panel and on the trigger", async () => {
    const user = userEvent.setup();
    render(<SiteHeader />);

    const trigger = headerTrigger();
    expect(trigger.className).toContain("size-11");

    await user.click(trigger);
    const panel = panelFor(trigger);
    const links = within(panel).getAllByRole("link");

    expect(links.length).toBeGreaterThan(0);
    for (const link of links) {
      // 11 * 0.25rem = 44px, the minimum comfortable touch target.
      expect(link.className).toContain("min-h-11");
    }
  });

  it("exposes the whole documentation list below lg on a doc page", async () => {
    const user = userEvent.setup();
    render(<DocPageView slug="installation" version={fallbackReleaseVersion} />);

    const trigger = screen.getByRole("button", {
      name: /open documentation menu/i,
    });
    const panel = panelFor(trigger);

    await user.click(trigger);
    expect(panel).not.toHaveAttribute("hidden");

    const links = within(panel).getAllByRole("link");
    expect(links).toHaveLength(docs.length);
    for (const doc of docs) {
      // Next normalises the trailing slash away, so compare the path it renders.
      expect(
        within(panel).getByRole("link", { name: doc.title }),
      ).toHaveAttribute("href", `/docs/${doc.slug}`);
    }

    // The current page is marked, so the menu answers "where am I" too.
    expect(
      within(panel).getByRole("link", { name: "Installation" }),
    ).toHaveAttribute("aria-current", "page");
  });

  it("closes the documentation menu after a page is chosen", async () => {
    const user = userEvent.setup();
    render(<DocPageView slug="installation" version={fallbackReleaseVersion} />);

    const trigger = screen.getByRole("button", {
      name: /open documentation menu/i,
    });
    const panel = panelFor(trigger);

    await user.click(trigger);
    await user.click(within(panel).getByRole("link", { name: "How recovery works" }));

    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(panel).toHaveAttribute("hidden");
  });

  it("offers the same list on the documentation index", async () => {
    const user = userEvent.setup();
    render(<DocsIndexPage />);

    const trigger = screen.getByRole("button", {
      name: /open documentation menu/i,
    });
    const panel = panelFor(trigger);

    await user.click(trigger);
    expect(within(panel).getAllByRole("link")).toHaveLength(docs.length);
  });
});
