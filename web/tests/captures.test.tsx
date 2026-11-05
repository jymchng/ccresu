import { readFileSync } from "node:fs";
import path from "node:path";

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { CaptureFigure } from "@/components/capture-figure";
import { captureList, captureSource, getCapture } from "@/lib/captures";

/**
 * Two different guarantees, deliberately kept apart.
 *
 * 1. **Renderer parity** — what the page shows must be the captured text, byte
 *    for byte. A `<pre>` is easy to break subtly (trimming, wrapping, an
 *    ellipsis added "for layout"), and a figure that quietly shortens command
 *    output is worse than no figure.
 * 2. **Capture parity** — the capture module must still agree with the fixtures
 *    that `scripts/web-captures.sh` wrote beside it. Editing one without the
 *    other fails here, which is what stops a hand-tweaked "nicer" command line
 *    from looking like real output.
 */
function fixtureFor(id: string): string {
  // The fixtures sit beside this file; `process.cwd()` is `web/` under vitest.
  const fixture = path.join(process.cwd(), "tests", "fixtures", "captures", `${id}.txt`);
  return readFileSync(fixture, "utf8");
}

describe("captured command output", () => {
  it("registers the captures the pages can reference", () => {
    expect(captureList.length).toBeGreaterThanOrEqual(5);
    const ids = captureList.map((capture) => capture.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids).toEqual(["help", "status", "sessions", "history", "e2e"]);
    expect(captureSource.generator).toBe("scripts/web-captures.sh");
    expect(getCapture("help")).toBeDefined();
    expect(getCapture("not-a-capture")).toBeUndefined();
  });

  it("renders every capture as exactly the text that was captured", () => {
    for (const capture of captureList) {
      const { container, unmount } = render(<CaptureFigure capture={capture} />);
      const pre = container.querySelector("pre code");

      expect(pre).not.toBeNull();
      // Byte-for-byte: no trim, no ellipsis, no re-wrapping.
      expect(pre?.textContent).toBe(capture.output);
      expect(pre?.textContent?.length).toBe(capture.output.length);
      unmount();
    }
  });

  it("agrees with the fixtures the capture script wrote", () => {
    for (const capture of captureList) {
      expect(fixtureFor(capture.id)).toBe(capture.output);
    }
  });

  it("labels each figure and repeats the command that produced it", () => {
    for (const capture of captureList) {
      const { container, unmount } = render(<CaptureFigure capture={capture} />);

      const figure = container.querySelector("figure");
      expect(figure).not.toBeNull();
      const caption = figure?.querySelector("figcaption");
      expect(caption).not.toBeNull();

      // The caption names the figure and carries the command that produced it.
      expect(caption?.textContent).toContain(capture.label);
      expect(caption?.textContent).toContain(capture.command);
      expect(within(caption as HTMLElement).getByText(capture.label)).toBeInTheDocument();
      expect(screen.getByText(capture.description)).toBeInTheDocument();
      expect(
        within(caption as HTMLElement).getByRole("button", {
          name: /copy to clipboard/i,
        }),
      ).toBeInTheDocument();

      unmount();
    }
  });

  it("shows real output rather than placeholder copy", () => {
    const help = getCapture("help")?.output ?? "";
    const status = getCapture("status")?.output ?? "";
    const history = getCapture("history")?.output ?? "";
    const e2e = getCapture("e2e")?.output ?? "";

    // Markers only the binary produces.
    expect(help).toContain("Usage:");
    expect(help).toContain("--config PATH");
    expect(help).toContain("Exit codes:");
    expect(status).toContain("sessions:");
    expect(status).toContain("recovery:");
    expect(history).toContain("USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET");
    // The proof shows the resume, the stop and the identical resume.
    expect(e2e).toContain("CALL");
    expect(e2e).toContain("--resume");
    expect(e2e).toContain("stop");
    expect(e2e).toContain("PASSED");

    for (const capture of captureList) {
      expect(capture.output.length).toBeGreaterThan(40);
      expect(capture.output.toLowerCase()).not.toContain("lorem ipsum");
      expect(capture.output).not.toContain("TODO");
    }
  });

  it("scrubs the machine-local values it claims to scrub", () => {
    // A capture must not leak the machine it was taken on.
    for (const capture of captureList) {
      expect(capture.output).not.toContain("/tmp/");
      expect(capture.output).not.toContain("/root/");
      expect(capture.output).not.toContain("/home/");
    }
    expect(getCapture("sessions")?.output).toContain("<project>");
    expect(getCapture("history")?.output).toContain("(captured-at)");
  });
});
