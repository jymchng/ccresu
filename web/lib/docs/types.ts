/**
 * Structural types for the documentation pages.
 *
 * Content is data, not markup: every page is a list of blocks, so the renderer
 * (components/doc-blocks.tsx) is the only place that decides how prose, lists,
 * code and tables look, and the tests can assert real content without parsing
 * HTML.
 */

export type Block =
  | { kind: "p"; text: string }
  | { kind: "h2"; text: string }
  | { kind: "ul"; items: string[] }
  | { kind: "ol"; items: string[] }
  | { kind: "code"; label?: string; code: string }
  | { kind: "note"; tone: "info" | "warning"; title: string; text: string }
  | { kind: "table"; head: string[]; rows: string[][] };

export type DocPage = {
  slug: string;
  title: string;
  summary: string;
  blocks: Block[];
};

export type DocRegistry = readonly DocPage[];
