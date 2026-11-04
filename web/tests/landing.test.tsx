import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import HomePage from "@/app/page";
import { features, workflow } from "@/lib/landing";
import { repo, site } from "@/lib/site";
import { normalizeHref } from "./helpers";

describe("landing page", () => {
  it("leads with the promise and the interrupted banner", () => {
    render(<HomePage />);

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
    render(<HomePage />);

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
    render(<HomePage />);

    const section = screen.getByRole("region", { name: "How it works" });
    const steps = within(section).getAllByRole("listitem");
    expect(steps).toHaveLength(workflow.length);
    steps.forEach((step, index) => {
      expect(step).toHaveTextContent(workflow[index]!.title);
      expect(step).toHaveTextContent(String(index + 1).padStart(2, "0"));
    });
  });

  it("shows the installation section with the real asset convention", () => {
    render(<HomePage />);

    const section = screen.getByRole("region", { name: "Install in one command" });
    expect(section).toHaveTextContent("ccresu_<version>_<os>_<arch>.tar.gz");
    expect(section).toHaveTextContent("checksums.txt");
    expect(
      within(section).getByRole("link", { name: /Latest release/ }),
    ).toHaveAttribute("href", repo.latestRelease);
  });

  it("links to the documentation with no relative dead ends", () => {
    render(<HomePage />);

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
    render(<HomePage />);

    expect(screen.getAllByText(new RegExp(site.name, "i")).length).toBeGreaterThan(0);
    expect(
      screen.getByRole("heading", { name: "What it will never do" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/No limit bypass/i)).toBeInTheDocument();
  });
});
