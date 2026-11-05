/**
 * Renders JSON-LD structured data into the page.
 *
 * A `<script type="application/ld+json">` is the one way to give a crawler
 * machine-readable facts (licence, version, breadcrumb trail) without changing
 * anything a reader sees. Two details matter:
 *
 *  - **One tag per graph.** `data` takes an object or a list; a list is emitted
 *    as a single `@graph` so a page with a breadcrumb *and* an article stays one
 *    coherent block instead of two unrelated scripts.
 *  - **`<` is escaped.** The payload is built from content strings, and an
 *    unescaped `</script>` inside a description would end the tag early. The
 *    escape is applied to the serialised JSON, so the parsed value is unchanged
 *    — `\u003c` and `<` are the same character to a JSON parser.
 *
 * The element is a server component with no state: it renders to static HTML at
 * build time, which is what a static export needs.
 */

type JsonLdValue = Record<string, unknown>;

function graph(data: JsonLdValue | JsonLdValue[]): string {
  const payload = Array.isArray(data) ? { "@context": "https://schema.org", "@graph": data } : data;
  return JSON.stringify(payload).replace(/</g, "\\u003c");
}

export function JsonLd({ data }: { data: JsonLdValue | JsonLdValue[] }) {
  return (
    <script
      type="application/ld+json"
      // The payload is our own, already serialised; escaping `<` is the guard
      // that matters, and it is applied above rather than left to the caller.
      dangerouslySetInnerHTML={{ __html: graph(data) }}
    />
  );
}
