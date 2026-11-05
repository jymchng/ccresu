import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Hero } from "@/components/hero";
import { LandingPage } from "@/components/landing-page";
import { features, workflow } from "@/lib/landing";
import { fallbackReleaseVersion, repo, site } from "@/lib/site";
import { normalizeHref } from "./helpers";

describe("landing page", () => {
  it("leads with the promise and the interrupted banner", () => {
    render(<LandingPage releaseVersion={fallbackReleaseVersion} />);

    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(
      "Your Claude Code session, resumed after the limit resets.",
    );
    const banner = screen.getByText(/You've hit your 5-hour limit/);
    expect(banner).toBeInTheDocument();
    expect(banner.closest("code")).toHaveTextContent(
      "resets 12pm (Europe/Berlin)",
    );
  });

  it("renders all eight feature cards", () => {
    render(<LandingPage releaseVersion={fallbackReleaseVersion} />);

    expect(features).toHaveLength(8);
    for (const feature of features) {
      const heading = screen.getByRole("heading", { name: feature.title });
      expect(heading).toBeInTheDocument();

      // Inline `code` spans split the text node, so compare the card's text.
      const card = heading.closest("[data-slot='card']");
      expect(card).not.toBeNull();
      expect((card as HTMLElement).textContent).toContain(
        feature.description.replace(/`/g, ""),
      );
      expect((card as HTMLElement).textContent).toContain(
        feature.detail.replace(/`/g, ""),
      );
    }
  });

  it("renders the five how-it-works steps in order", () => {
    render(<LandingPage releaseVersion={fallbackReleaseVersion} />);

    const section = screen.getByRole("region", { name: "How it works" });
    const steps = within(section).getAllByRole("listitem");
    expect(steps).toHaveLength(workflow.length);
    steps.forEach((step, index) => {
      expect(step).toHaveTextContent(workflow[index]!.title);
      expect(step).toHaveTextContent(String(index + 1).padStart(2, "0"));
    });
  });

  it("shows the installation section with the real asset convention", () => {
    render(<LandingPage releaseVersion={fallbackReleaseVersion} />);

    const section = screen.getByRole("region", { name: "Install in one command" });
    expect(section).toHaveTextContent("ccresu_<version>_<os>_<arch>.tar.gz");
    expect(section).toHaveTextContent("checksums.txt");
    expect(
      within(section).getByRole("link", { name: /Latest release/ }),
    ).toHaveAttribute("href", repo.latestRelease);
  });

  it("links to the documentation with no relative dead ends", () => {
    render(<LandingPage releaseVersion={fallbackReleaseVersion} />);

    const internal = Array.from(document.querySelectorAll("a"))
      .map((anchor) => anchor.getAttribute("href") ?? "")
      .filter((href) => href.startsWith("/"));

    expect(internal.length).toBeGreaterThan(0);
    for (const href of internal) {
      expect(
        [
          "/",
          "/docs",
          "/docs/installation",
          "/docs/recovery",
          "/docs/limitations",
        ],
      ).toContain(normalizeHref(href));
    }
  });

  it("names the tool and states the no-bypass guarantee", () => {
    render(<LandingPage releaseVersion={fallbackReleaseVersion} />);

    expect(screen.getAllByText(new RegExp(site.name, "i")).length).toBeGreaterThan(0);
    expect(
      screen.getByRole("heading", { name: "What it will never do" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/No limit bypass/i)).toBeInTheDocument();
  });
});

/**
 * The hero's width guards, pinned as markup.
 *
 * The failure this prevents is not a scrollbar: the hero `<section>` clips its
 * overflow, so a too-wide column is *cut off* instead of scrollable — the
 * headline simply runs off the screen at 320px. The cause is CSS `min-width:
 * auto` on grid items: without `min-w-0` the terminal panels' min-content width
 * (their longest `<pre>` line, 509px when this was written) sizes the whole
 * column, and every sibling inherits it.
 *
 * A browser measurement is the real proof (tests/mobile measures
 * `document.scrollWidth <= innerWidth` and the geometry of the hero column);
 * this test is the cheap guard that fails in a normal `npm test` run if one of
 * those classes is dropped during a refactor.
 */
describe("hero width guards", () => {
  it("lets both hero columns shrink below their content's min-content width", () => {
    const { container } = render(<Hero />);

    const grid = container.querySelector<HTMLElement>("section > div.relative");
    expect(grid).not.toBeNull();

    const columns = Array.from(grid?.children ?? []) as HTMLElement[];
    expect(columns).toHaveLength(2);
    for (const column of columns) {
      expect(column.className).toContain("min-w-0");
    }
  });

  it("scrolls the terminal output inside its own card", () => {
    const { container } = render(<Hero />);

    const pres = Array.from(container.querySelectorAll("pre"));
    expect(pres.length).toBeGreaterThanOrEqual(2);

    for (const pre of pres) {
      // The long command lines scroll here...
      expect(pre.className).toContain("overflow-x-auto");
      expect(pre.className).toContain("max-w-full");

      // ...inside a card that is itself allowed to be narrower than its text.
      const card = pre.parentElement;
      expect(card).not.toBeNull();
      expect(card?.className).toContain("min-w-0");
      expect(card?.className).toContain("max-w-full");
      expect(card?.className).toContain("overflow-hidden");
    }
  });

  it("lets the three-column fact list shrink with the column", () => {
    const { container } = render(<Hero />);

    const list = container.querySelector("dl");
    expect(list).not.toBeNull();
    expect(list?.className).toContain("grid-cols-3");

    const cells = Array.from(list?.children ?? []) as HTMLElement[];
    expect(cells.length).toBe(3);
    for (const cell of cells) {
      expect(cell.className).toContain("min-w-0");
    }
  });
});
