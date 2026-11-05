import { existsSync, readdirSync } from "node:fs";
import path from "node:path";

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { DocBlocks } from "@/components/doc-blocks";
import { captureList, getCapture } from "@/lib/captures";
import { docs } from "@/lib/docs";
import type { Block, DiagramId } from "@/lib/docs/types";

/**
 * The two figure blocks.
 *
 * A `capture` is real command output and a `diagram` is hand-authored SVG, and
 * both exist so that documentation cannot quietly become a picture of software
 * that no longer behaves that way. These tests pin the parts that make that
 * true rather than the parts that are merely pretty:
 *
 *  - a capture renders the captured bytes exactly, under a label that names it;
 *  - a diagram is an `img` with an accessible name and description, drawn
 *    inline, with no bitmap and no external reference to fetch or break;
 *  - every id used in the content resolves, so no page can point at a figure
 *    that is not there.
 */

function captureOrThrow(id: string) {
  const capture = getCapture(id);
  if (!capture) {
    throw new Error(`no capture registered for id ${id}`);
  }
  return capture;
}

const diagrams = docs
  .flatMap((doc) => doc.blocks)
  .filter((block): block is Extract<Block, { kind: "diagram" }> => block.kind === "diagram");

const captured = docs
  .flatMap((doc) => doc.blocks)
  .filter((block): block is Extract<Block, { kind: "capture" }> => block.kind === "capture");

describe("documentation figures", () => {
  it("renders a capture block as a labelled figure with the exact captured text", () => {
    const capture = captureOrThrow("status");
    const { container } = render(<DocBlocks blocks={[{ kind: "capture", id: "status" }]} />);

    const figure = container.querySelector("figure[data-capture='status']");
    expect(figure).not.toBeNull();

    // Byte-for-byte, the same guarantee tests/captures.test.tsx makes directly.
    const pre = figure?.querySelector("pre code");
    expect(pre?.textContent).toBe(capture.output);

    // The label is visible, and it is also the figure's accessible name.
    expect(figure?.textContent).toContain(capture.label);
    const labelledBy = figure?.getAttribute("aria-labelledby");
    expect(labelledBy).toBeTruthy();
    expect(container.querySelector(`#${labelledBy}`)?.textContent).toContain(capture.label);
  });

  it("renders every capture the content references, with its real output", () => {
    expect(captured.length).toBeGreaterThanOrEqual(4);

    for (const block of captured) {
      const capture = captureOrThrow(block.id);
      const { container, unmount } = render(<DocBlocks blocks={[block]} />);
      const pre = container.querySelector(`figure[data-capture='${block.id}'] pre code`);

      expect(pre?.textContent).toBe(capture.output);
      unmount();
    }
  });

  it("renders a diagram as an image with an accessible name and description", () => {
    const block: Block = {
      kind: "diagram",
      id: "state-machine",
      label: "The chain, the retry loop and the two ways out",
      description: "A failed attempt with budget left returns to the session check.",
    };
    render(<DocBlocks blocks={[block]} />);

    const image = screen.getByRole("img", { name: block.label });
    expect(image.tagName.toLowerCase()).toBe("svg");
    expect(image).toHaveAccessibleName(block.label);
    expect(image).toHaveAccessibleDescription(block.description);

    // Inline and scalable: a viewBox, no raster data, nothing to download.
    expect(image.getAttribute("viewBox")).toBeTruthy();
    expect(image.querySelectorAll("image").length).toBe(0);
    expect(image.querySelector("[href], [xlink\\:href]")).toBeNull();

    // The visible caption carries the same words the accessible name does.
    // (`<desc>` repeats them for a screen reader, so query the caption itself.)
    const caption = image.closest("figure")?.querySelector("figcaption");
    expect(caption?.textContent).toContain(block.label);
    expect(caption?.textContent).toContain(block.description);
  });

  it("draws every diagram in the content with a name, a description and real labels", () => {
    expect(diagrams.length).toBeGreaterThanOrEqual(2);

    const drawn = new Set<DiagramId>();
    for (const block of diagrams) {
      drawn.add(block.id);
      expect(block.label.length).toBeGreaterThan(10);
      expect(block.description.length).toBeGreaterThan(40);

      const { container, unmount } = render(<DocBlocks blocks={[block]} />);
      const image = container.querySelector("svg[role='img']");

      expect(image).not.toBeNull();
      expect(image?.getAttribute("viewBox")).toMatch(/^0 0 /);

      // The drawing states its own states in text, so it is searchable and
      // selectable rather than an unreadable bitmap.
      const text = Array.from(image?.querySelectorAll("text") ?? []).map(
        (node) => node.textContent ?? "",
      );
      expect(text.length).toBeGreaterThan(4);
      expect(text.join(" ")).toMatch(/[A-Z_]{4,}/);

      unmount();
    }

    // Both hand-authored figures are actually used somewhere.
    expect(Array.from(drawn).sort()).toEqual(["state-machine", "stop-then-resume"]);
  });

  it("keeps every referenced capture registered", () => {
    const ids = new Set(captureList.map((capture) => capture.id));
    for (const block of captured) {
      expect(ids.has(block.id)).toBe(true);
    }
  });

  it("ships no bitmap figures under public/", () => {
    // The figures are markup. A PNG here would mean a second asset per
    // breakpoint, a blurry 3x phone screen, and a drawing nobody can update.
    const publicDir = path.join(process.cwd(), "public");
    if (!existsSync(publicDir)) {
      return;
    }

    const binary = readdirSync(publicDir).filter((entry) =>
      /\.(png|jpe?g|gif|webp|avif|bmp|tiff?)$/i.test(entry),
    );
    expect(binary).toEqual([]);
  });
});
