# The website

`web/` is a **marketing and documentation site** for ccresu. It is a static
export published on GitHub Pages. It never talks to a running daemon, has no
authentication, holds no state and ships no data-fetching code.

That distinction matters: ccresu the tool has no web UI. The surfaces are the
CLI, the daemon and the loopback REST/SSE API. If you are looking for a
dashboard to drive sessions from a browser, the honest answer is that there
isn't one — `ccresu` in a terminal, or `curl` against `127.0.0.1:8787`.

## Stack

| Choice | Why |
| --- | --- |
| Next.js 16 App Router | Static export with file-based routing and no server to run. |
| TypeScript, strict | `tsc --noEmit` is a CI gate, so a doc page cannot rot into `any`. |
| Tailwind CSS 3 | Utility classes only; no stylesheet to keep in sync with markup. |
| Lucide icons + a small shadcn-style `ui/` layer | Icons and primitives without pulling in a component framework. |
| Vitest + Testing Library | Component tests that render the real pages and assert on visible text. |

## Layout

```text
web/
  app/
    page.tsx                 landing page
    layout.tsx               fonts, theme provider, metadata
    globals.css              Tailwind layers and theme variables
    docs/page.tsx            documentation index
    docs/<slug>/page.tsx     one route per document
  components/
    hero.tsx feature-grid.tsx how-it-works.tsx install-commands.tsx
    site-header.tsx site-footer.tsx theme-toggle.tsx copy-button.tsx
    docs-nav.tsx doc-page-view.tsx doc-blocks.tsx ui/
  lib/
    site.ts                  every URL, and the release asset-name rules
    landing.ts               landing copy: features, workflow, install steps
    docs/*.ts                documentation content, one module per page
    theme.ts utils.ts
  tests/                     component tests
  next.config.ts             output: "export", basePath "/ccresu"
```

Content is TypeScript data, not MDX: `lib/docs/<slug>.ts` exports a `DocPage`
object that `app/docs/<slug>/page.tsx` renders through `DocPageView`. The
`DocPage` shape is a small block vocabulary — heading, paragraph, list, code,
note — so every page gets the same typography and anchor behaviour for free.

## Two rules the code enforces

1. **Nothing hard-codes a URL.** Every link, asset name and download path comes
   from `lib/site.ts`. `assetUrl()` builds
   `https://github.com/jymchng/ccresu/releases/download/v<version>/ccresu_<version>_<os>_<arch>.<ext>`
   — the *pinned tag* form. Appending a tag to the `latest` form
   (`/releases/latest/download/<tag>/<asset>`) is a 404; that mistake is why the
   helper exists and why it is tested.
2. **basePath is `/ccresu`.** Pages serves a project site from
   `https://<owner>.github.io/ccresu`, so every asset and `next/link` href
   carries the prefix, and `trailingSlash: true` turns each route into
   `<route>/index.html` for the static file server.

## Working on it

```bash
cd web
npm ci             # from the lockfile, never `npm install` in CI
npm run dev        # http://localhost:3000/ccresu
npm run lint       # eslint (flat config, next/core-web-vitals)
npm run typecheck  # tsc --noEmit
npm run test       # vitest run
npm run build      # static export into web/out
```

All five are gates: CI runs them on every push and pull request, and the deploy
workflow re-runs them before publishing, so a broken page is never deployed.

The Makefile wraps the same commands from the repository root:

```bash
make web-install web-lint web-typecheck web-test web-build
```

## Tests

Seven files under `web/tests`, driven by Vitest with jsdom:

| File | Covers |
| --- | --- |
| `landing.test.tsx` | Hero copy, the eight feature cards, the how-it-works steps. |
| `docs.test.tsx` | Every documentation page renders its headings and body blocks. |
| `content.test.tsx` | The content modules: no empty sections, no duplicate slugs. |
| `nav.test.tsx` | Header/footer links and the documentation sidebar. |
| `theme-toggle.test.tsx` | Dark-first default, toggling, and persistence. |
| `install-commands.test.tsx` | The install snippets and their exact release asset names. |
| `copy-button.test.tsx` | Copy-to-clipboard, including the failure path. |

Testing Library is configured in `vitest.setup.ts`, which installs
`@testing-library/jest-dom` matchers and stubs `navigator.clipboard` so the copy
tests are deterministic.

## Deployment

`.github/workflows/deploy-website.yml` publishes only from `main`, and only when
something under `web/**` (or the workflow itself) changes, or on manual
dispatch:

1. `npm ci`, lint, type-check, test, build — the same gates as CI.
2. `actions/configure-pages` + `upload-pages-artifact` with `web/out`.
3. `actions/deploy-pages` in a separate job whose only permissions are
   `pages: write` and `id-token: write`.

The first deploy needs the repository's Pages source set to **GitHub Actions**;
after that, publishing is automatic. To preview the real export locally:

```bash
cd web && npm run build
npx serve out     # out/ccresu/index.html is the landing page
```

## What the site is not allowed to do

- No dashboard routes, no session viewer, no fake data, no "coming soon" button.
- No client-side call to `127.0.0.1:8787` — a public site cannot reach your
  daemon, and pretending otherwise would be a security bug.
- No analytics or third-party scripts.
- No invented CLI flags or release asset names: `web/tests/install-commands.test.tsx`
  asserts against the same convention `release.yml` produces and
  `lib/site.ts` documents.
