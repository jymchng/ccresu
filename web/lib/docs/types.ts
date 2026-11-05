import type { captures } from "../captures";

/**
 * Structural types for the documentation pages.
 *
 * Content is data, not markup: every page is a list of blocks, so the renderer
 * (components/doc-blocks.tsx) is the only place that decides how prose, lists,
 * code and tables look, and the tests can assert real content without parsing
 * HTML.
 */

/**
 * The id of a real capture in `lib/captures`.
 *
 * Derived from the capture module rather than re-listed here, so a capture that
 * `scripts/web-captures.sh` stops producing is a type error in the content
 * that still references it — and a page cannot link to a figure that does not
 * exist.
 */
export type CaptureId = keyof typeof captures;

/**
 * The hand-drawn figures in `components/doc-diagrams.tsx`.
 *
 * A closed list, because each id is hand-authored geometry: a new diagram is a
 * new drawing, not a new string.
 */
export type DiagramId = "state-machine" | "stop-then-resume";

export type Block =
  | { kind: "p"; text: string }
  | { kind: "h2"; text: string }
  | { kind: "ul"; items: string[] }
  | { kind: "ol"; items: string[] }
  | { kind: "code"; label?: string; code: string }
  | { kind: "note"; tone: "info" | "warning"; title: string; text: string }
  | { kind: "table"; head: string[]; rows: string[][] }
  /**
   * Command output the binary actually printed, captured by
   * `scripts/web-captures.sh` rather than typed into the page. Referenced by id
   * so the page and the capture cannot drift apart.
   */
  | { kind: "capture"; id: CaptureId }
  /**
   * An inline SVG figure. `label` becomes the accessible name and the heading
   * of the figure; `description` becomes the accessible description and the
   * caption, so the picture is legible to someone who cannot see it — and it is
   * inline markup, never a bitmap under `public/`.
   */
  | { kind: "diagram"; id: DiagramId; label: string; description: string };

export type DocPage = {
  slug: string;
  title: string;
  summary: string;
  blocks: Block[];
};

export type DocRegistry = readonly DocPage[];
