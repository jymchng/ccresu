# The gate fails on the tree it was written for

`npm run test:mobile` is only worth having if it can fail. This is the recorded
run against the commit that precedes the 320px work, `2d353c7`
("feat(web): figures, mobile-safe landing, build-time release version"), with
the current gate copied in unchanged.

## Reproducing it

```bash
git worktree add /tmp/prefix 2d353c7
cp node_modules -R /tmp/prefix/web/node_modules      # or `npm ci` inside it
cp web/tests/mobile/gate.mjs /tmp/prefix/web/tests/mobile/
cd /tmp/prefix/web && npx next build
node tests/mobile/gate.mjs; echo "exit=$?"
```

## Result

```
60 route/width pairs:            58/60 fit the viewport   (2 overflow)
44x44 tap targets:                0/60 clean              (12 undersized per page)
menu open/navigate/close:         0/80 passed             (neither menu existed)
```

With 143 failures and `exit=1`. On the finished tree the same command prints:

```
60/60 route/width pairs fit the viewport
60/60 route/width pairs have no target under 44x44
80/80 menu open/navigate/close checks passed
47 screenshots in web/tests/mobile/artifacts/screenshots/

PASS
```

## What it caught, verbatim

The two overflowing pages were real, and neither was on the page the work
started from:

```
FAIL /docs/installation/ @ 320px — document is 339px wide in a 320px viewport
       span [] 16..339 "Releases are published on the GitHub rel"
FAIL /docs/telegram/ @ 320px — document is 370px wide in a 320px viewport
       code [rounded bg-muted px-1.5 py-0.5 font-mono ...] 36..370 "curl https://api.telegram.org/bot<token>"
```

Both are the same root cause — one unbreakable token in prose — which is why the
fix landed as `[overflow-wrap:anywhere]` on `InlineText` rather than as two
per-page patches.

The tap targets were uniformly too small, so this is the whole first page's
worth rather than a corner case:

```
FAIL /docs/installation/ @ 320px — 12 target(s) under 44x44
       a "crccresu" 92x28
       a "GitHub" 36x28
       button "Switch to light theme" 40x40
       a "Home" 20x36
       a "Documentation" 20x36
       a "Install" 20x36
       a "How it works" 20x76
       a "Docs" 29x16
       button "Copy to clipboard" 68x26      (x4)
```

And the navigation simply was not there:

```
FAIL / @ 320px — the header menu trigger is not visible
FAIL /docs/ @ 320px — the docs menu trigger is not visible
```

## Note on route count

The goal text says "11 doc slugs"; the site has ten (`cli`, `configuration`,
`deployment`, `installation`, `limitations`, `recovery`, `security`, `telegram`,
`troubleshooting`, `tutorial`) plus the index, which is the twelve routes the
gate walks. The list lives in `gate.mjs` next to the export it measures, so a
new page has to be added there — the tap-target and overflow loops follow it
automatically.
