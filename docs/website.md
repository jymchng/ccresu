# The website

`web/` is a **marketing and documentation site** for ccresu. It is a static
export published on GitHub Pages. It never talks to a running daemon, has no
authentication, holds no state and ships no client-side data fetching.

That distinction matters: ccresu the tool has no web UI. The surfaces are the
CLI, the daemon and the loopback REST/SSE API. If you are looking for a
dashboard to drive sessions from a browser, the honest answer is that there
isn't one — `ccresu` in a terminal, or `curl` against `127.0.0.1:8787`.

## What the site says a recovery does

The site documents **one** resume vector, and it is the one the binary runs: a
single blocking command.

```text
claude --resume <session-id> "<continuation message>"
```

That is `claude.ResumeArgs` (`internal/claude/resume.go`) argument for argument
— `--resume <session-id>`, then the message as a positional argument, last — and
the copy is written to match it rather than to a remembered version of it:

- **The exit status is the verdict.** The command runs the whole turn and returns
  when the continuation is finished. Status 0 marks the attempt recovered; a
  non-zero status is a failed attempt recorded with its exit code and stderr.
  There is no acceptance block to parse and no second source of truth.
- **Growth is a diagnostic.** The transcript is measured once after a successful
  exit and the reading is stored — `proven`, `exit_code`, `transcript_grew`, the
  line and size readings — but it never decides anything. The transcript itself
  is read for discovery and, when a recovery fails, to explain why.
- **No print mode, no stop-then-resume.** `claude -p --resume`,
  `--output-format json` and the retry loop that stopped a session to start it
  again are all gone from the binary and from every page. Nor is there a
  background flag or a session label: the vector above is the whole command.

`web/tests/handoff-contract.test.ts` is what keeps that honest, and it checks the
artefacts rather than a remembered sentence:

1. `internal/claude/resume.go` must name **one** flag and one only: `--resume`.
2. `web/lib/docs/recovery.ts` and `docs/recovery.md` must both carry the
   invocation above character for character, and the page must state that the
   exit status is the verdict.
3. No file a reader can reach — `README.md`, every `docs/*.md`, the `web/lib/docs`
   modules, the landing and static-page copy, the captures module and the
   components (the diagram included) — may still carry one of the retired
   markers the test names: the background flag, the session label, the printed
   status block, the stop command, the print-mode and guardrail flags, and the
   dropped field of the `/recover` payload. The markers exist only inside the
   test file, which is deliberately outside the corpus it scans, so the list
   cannot quietly turn into an exemption list.

The scope boundary is asserted next door, not there: `seo.test.ts` pins the
route table at 15, and `static-pages.test.tsx` pins the privacy page's "no
analytics, one build-time GitHub call" claim. See [Tests](#tests).

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

Nineteen test files under `web/tests`, driven by Vitest with jsdom:

| File | Covers |
| --- | --- |
| `backdrop.test.ts` | The CSS page gradient exists in both themes and costs no contrast. |
| `captures.test.tsx` | Captured command output: the renderer shows the captured bytes exactly, and the capture module still matches the fixtures. |
| `content.test.tsx` | The copy itself: no placeholder text, the executed `claude --resume …` vector with no retired flag, the landing surfaces stating the exit status as the verdict, the message-last ordering argued on the recovery page, the "no web UI" line, the GitHub Pages base path, the pre-paint theme bootstrap, and a usable 404. |
| `copy-button.test.tsx` | Copy-to-clipboard, including the failure path. |
| `doc-figures.test.tsx` | The two figure blocks: a capture renders the captured bytes, a diagram is inline SVG with an accessible name, and every figure id resolves. |
| `docs.test.tsx` | Every documentation page renders its headings and body blocks. |
| `handoff-contract.test.ts` | The resume contract, read from the artefacts: `internal/claude/resume.go` builds `--resume` and no other flag; `web/lib/docs/recovery.ts` and `docs/recovery.md` both carry `claude --resume <session-id> "<continuation message>"`, and the page says the exit status is the verdict; and no file in the corpus — the manual, the site's doc, landing and static-page modules, the captures module and the components — still carries one of the retired markers the test names (the background flag, the session label, its status block, the stop command, the print-mode and guardrail flags, the dropped `/recover` field), which live only in the test file, deliberately outside the corpus it scans. |
| `install-commands.test.tsx` | The install snippets and their exact release asset names. |
| `install-one-liner.test.ts` | The site and CI agree about what a documented command is, by rendering the commands the way CI does. |
| `landing.test.tsx` | Hero copy, the eight feature cards, the how-it-works steps. |
| `markdown.test.tsx` | The "copy this page as Markdown" promise, for a page and for the index. |
| `mobile-nav.test.tsx` | The navigation a phone actually gets. |
| `nav.test.tsx` | Header/footer links and the documentation sidebar. |
| `release.test.ts` | The version the install commands name: release resolution, normalisation and the fallback. |
| `responsive.test.tsx` | What markup can prove about "usable at 320px": the 44px control floor, a table that scrolls in its own region, breakable long URLs. |
| `seo.test.ts` | The SEO contract: the route table — home, the docs index, ten doc pages and three static pages, fifteen in all, each exactly once — route metadata, parsed structured data, and the exported sitemap and `robots.txt`. |
| `static-pages.test.tsx` | The about, privacy and terms pages, the links they share with the docs, and the privacy page's real data-flow claims: no analytics, one build-time GitHub call, and Telegram as the binary's only outbound host. |
| `terms-liability.test.tsx` | The liability limitation, asserted as things a reader must be able to find on `/terms/`. |
| `theme-toggle.test.tsx` | Dark-first default, toggling, and persistence. |

Testing Library is configured in `vitest.setup.ts`, which installs
`@testing-library/jest-dom` matchers and stubs `navigator.clipboard` so the copy
tests are deterministic. `tests/helpers.ts` holds the shared link helpers, and
`tests/fixtures/captures/` holds the committed command output that
`scripts/web-captures.sh` writes. The layout measurements jsdom cannot make live
in `tests/mobile/`, which runs from its own `npm run test:mobile` script rather
than from Vitest.

## Deployment

`.github/workflows/deploy-website.yml` publishes on **every push to `main`** (no
`paths:` filter — a README- or docs-only change republishes too) and on manual
dispatch:

1. `npm ci`, lint, type-check, test, build — the same gates as CI.
2. The exported `web/out` is force-pushed to the **`gh-pages` branch**, which is
   what Pages actually serves.

Publishing a branch rather than calling `actions/deploy-pages` is deliberate.
The artifact path was tried first and never worked here: the deployment stayed
`queued` → `in_progress` with an empty status until the 10-minute poll aborted
it, twice, and the site kept returning 404. A branch push needs nothing but a
`contents: write` token, and the published tree can be inspected afterwards
(`git ls-tree -r --name-only origin/gh-pages`).

### The Pages setting must match, or the site 404s silently

Pages serves the repository only when its source is the `gh-pages` branch.
Check it whenever a green "Deploy website" run still leaves the site 404 —
that is exactly what happened on 2026-11-04, when the repository reported
`build_type: "workflow"` while the workflow was pushing a branch: the push
succeeded, the landing page returned 404.

```bash
gh api repos/jymchng/ccresu/pages --jq '{status, build_type, source}'
# good: {"status":"built","build_type":"legacy","source":{"branch":"gh-pages","path":"/"}}

# fix it (or use Settings → Pages → Deploy from a branch → gh-pages / root):
gh api -X PUT repos/jymchng/ccresu/pages \
  -f build_type=legacy -f 'source[branch]=gh-pages' -f 'source[path]=/'
gh api -X POST repos/jymchng/ccresu/pages/builds    # rebuild now
gh api repos/jymchng/ccresu/pages/builds/latest --jq .status   # → "built"
```

A correct deploy is verifiable from the outside in one command — and that curl
is the check the workflow itself cannot do:

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://jymchng.github.io/ccresu/docs/installation/
# 200, served as /index.html because trailingSlash is true
```

To preview the real export locally:

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
