# ccresu website

The landing page and documentation site for ccresu. It is a static export
(`output: 'export'`) published to GitHub Pages at
<https://jymchng.github.io/ccresu>, so every route is prerendered HTML and the
site has no server, no API and no dashboard.

The binary ships **no** web UI: this site is documentation only. The runtime
interfaces are the CLI and the local REST/SSE API.

## Requirements

- Node.js 22 or newer (the toolchain is Next.js 16, React 19, Tailwind CSS 3).

## Scripts

```bash
npm ci             # install from the lockfile
npm run dev        # local development server on http://localhost:3000/ccresu
npm run lint       # ESLint (flat config, next/core-web-vitals)
npm run typecheck  # tsc --noEmit
npm run test       # vitest + Testing Library in jsdom
npm run build      # static export into ./out
```

`npm run build` writes `out/`, which is served from the `basePath` `/ccresu`.
To preview the export exactly as GitHub Pages will serve it:

```bash
rm -rf /tmp/site && mkdir -p /tmp/site
cp -r out /tmp/site/ccresu
python3 -m http.server 8899 --directory /tmp/site
# http://127.0.0.1:8899/ccresu/
```

## Layout

| Path | Contents |
| --- | --- |
| `app/page.tsx` | Landing page: hero, eight feature cards, how-it-works, installation, guarantees. |
| `app/docs/` | Documentation routes; each page is a thin wrapper around `DocPageView`. |
| `app/layout.tsx` | Shell: metadata, theme bootstrap script, header, footer. |
| `components/` | Site chrome, landing sections and the shadcn/ui primitives in `components/ui`. |
| `lib/site.ts` | Single source of truth for the repository links and the release asset naming convention. |
| `lib/docs/` | The documentation content, as typed blocks (`p`, `h2`, `ul`, `ol`, `code`, `note`, `table`). |
| `lib/landing.ts` | Feature cards and workflow steps. |
| `tests/` | Component and content tests. |
| `public/` | Static files copied verbatim into the export. |

## Content rules

- Nothing on the site may claim a behaviour the Go implementation does not have.
  The resume vector is the clearest case: every page describes the single command
  `claude --resume <session-id> "<continuation message>"` — one blocking run in
  the session's own working directory, the message last — which is
  `claude.ResumeArgs` argument for argument, and the verdict is the exit status
  of that process. The background handoff it replaced, print mode
  (`claude -p --resume`), `--output-format json`, stop-then-resume retries and
  "wait for the transcript to grow" are retired: they are not described anywhere,
  and the one print-mode sentence the copy keeps is the negation that explains
  why there is no second path: print mode and the background flag cannot be
  combined.
- Transcript growth is documented as a diagnostic, never as proof: a resume that
  exits 0 is recovered even when the transcript did not grow.
- `tests/handoff-contract.test.ts` enforces that rule from the artefacts rather
  than from memory. It pins the argument order against `internal/claude/resume.go`,
  fails on any retired claim returning to a doc, landing or diagram module, and
  pins the scope boundary: 15 routes, no dashboard, no auth, no analytics, and no
  client-side request to the loopback API.
- No placeholder copy, no fake metrics, no dead links: `tests/` fails on
  placeholder text, on an internal link outside the known routes, and on
  external links that do not point at the repository.
- Release asset names live in `lib/site.ts` and must match what
  `.github/workflows/release.yml` publishes:
  `ccresu_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) plus `checksums.txt`.

## Tests

`npm run test` runs the Vitest suite in jsdom; `npm run test:mobile` drives the
built export in a real browser. The suite that matters most for this directory's
credibility is `tests/handoff-contract.test.ts` — it is the reason a page cannot
quietly go back to describing the recovery the binary stopped running. The rest
are listed in `docs/website.md`.

## Deployment

`.github/workflows/deploy-website.yml` builds this directory and publishes
`out/` with GitHub Pages. See `docs/deployment.md` in the repository root for the
runtime deployment of the binary itself.
