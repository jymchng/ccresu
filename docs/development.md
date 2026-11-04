# Development

Everything is Go 1.26 plus a Next.js site under `web/`. There is no build
system beyond the `Makefile`, no code generation step to remember, and no
database server to run: the store is pure-Go SQLite and creates itself.

## Get running

```bash
git clone https://github.com/jymchng/ccresu.git
cd ccresu
go build ./...            # compiles every package
make build                # ./ccresu with version metadata from git
go test ./...             # the whole suite, no network, no fixtures to install
```

`make help` lists every target. The ones you will use:

| Target | Does |
| --- | --- |
| `make build` | `CGO_ENABLED=0` build with `-trimpath` and version/commit/date ldflags. |
| `make fmt` / `make fmt-check` | Format sources / fail if anything is unformatted (CI-safe). |
| `make vet` | `go vet ./...`. |
| `make test` / `make race` | `go test ./...` / `go test -race ./...`. |
| `make cover` | Writes `coverage.out` and prints the total. |
| `make cover-check` | The gate: fails below `COVERAGE_MIN` (80%). |
| `make ci` | `fmt-check vet test race cover-check` in that order. |
| `make web-*` | `web-install`, `web-lint`, `web-typecheck`, `web-test`, `web-build`. |
| `make lint` | `golangci-lint run` (v2 config in `.golangci.yml`). |
| `make clean` | Removes the binary, `bin/`, `dist/` and coverage artifacts. |

## Layout

```text
cmd/ccresu/            main: version metadata, build the app, exit with its code
internal/app/          CLI surface, runtime wiring, daemon and serve loops
internal/config/       YAML schema, defaults, CCRESU_* env, strict validation
internal/logging/      slog handlers and the secret redactor
internal/clock/        Clock interface, real and fake implementations
internal/process/      process identity from /proc, fake manager
internal/storage/      SQLite store, embedded migrations, repositories
internal/claude/       the only package that talks to Claude Code
internal/detector/     normalisation and usage-limit patterns
internal/scheduler/    reset-time parsing, schedules, unknown-reset backoff
internal/recovery/     the state machine, locking, backoff, verifier
internal/telegram/     Bot API client, templates, persistent queue
internal/server/       REST + SSE API, auth, limits
tests/support/         shared fixtures: fake clock, real SQLite, fake provider
tests/e2e/             the full interruption scenario, replayed end to end
tests/integration/     cross-package flows (currently only .gitkeep)
web/                   landing + documentation site (static export)
scripts/               install.sh, uninstall.sh
deployments/systemd/   the user unit
docs/                  the user-facing manual
```

Each package has a `doc.go` that states its responsibility and the invariants it
must not break. Read those first; they are the design notes the code assumes.

## How the layers connect

Nothing in `internal/claude`, `internal/telegram` or `internal/server` knows
about the state machine, and `internal/recovery` knows nothing about HTTP or
`exec`. The joins are interfaces, one file per seam:

| Seam | Interface | Real implementation | Test double |
| --- | --- | --- | --- |
| Time | `clock.Clock` | `clock.System()` | `clock.Fake` |
| Process execution | `claude.Execer` | `claude.OSExecer` | a scripted execer |
| Session discovery/resume | `claude.SessionProvider` | `claude.CLI`/`Provider` | `claude.FakeProvider` |
| Persistence | `storage.Store` (per-repository interfaces in consumers) | the SQLite store | in-memory doubles |
| Notification delivery | `telegram.Sender` | `telegram.Client` | a fake sender |

When you add a feature, put the seam in the consuming package as a small
interface and wire it in `internal/app`. That is what keeps the tests fast and
the `-race` run meaningful.

## Testing

- **Unit tests live beside the code** (`internal/<pkg>/*_test.go`) and use fakes
  for everything outside the package.
- **`tests/support`** builds the fixtures that need to be real: a temporary
  SQLite database (`storage.Open` + migrations), a provider whose transcript
  file actually grows, a fake clock you advance by hand, and an HTTPS Telegram
  stub via `httptest`.
- **`tests/e2e`** replays the canonical interruption end to end: the
  `You've hit your 5-hour limit · resets 12pm (Europe/Berlin)` banner plus the
  paused-goal companion message, through detection, scheduling, the real state
  machine, a fake `claude`, verification and the notification queue, asserting
  the session id is preserved, no new conversation is started, the state reaches
  `RESUMED`, and the Telegram message says so.
- **Failures are part of the suite**: `ErrLimitNotCleared` (the limit is still in
  force), `ErrSessionNotPreserved`, an exhausted attempt budget, a restart in the
  middle of a wait.

```bash
go test ./internal/recovery/ -run TestVerifier -v
go test ./tests/e2e/ -v
go test -race -count=1 ./...
```

Never make a test depend on the wall clock, the network, your `~/.claude`, or
`$HOME`. If you need time, take a `clock.Clock`; if you need HTTP, use
`httptest`; if you need a database, use `t.TempDir()`.

Coverage expectations, enforced in review and partly in CI:

| Package | Target |
| --- | --- |
| total | ≥80% (`make cover-check`, also in CI and the release gate) |
| `internal/detector`, `internal/scheduler` | ≥95% |
| `internal/config`, `internal/recovery`, `internal/telegram` | ≥90% |
| `internal/storage` | ≥85% |
| `internal/claude`, `internal/server`, `internal/app` | ≥80% |

```bash
go test -coverprofile=coverage.out -covermode=atomic ./...
go tool cover -func=coverage.out | sort -k3 -n | head
go tool cover -html=coverage.out -o coverage.html
```

## The website

```bash
cd web
npm ci
npm run dev        # http://localhost:3000/ccresu
npm run lint && npm run typecheck && npm run test && npm run build
```

Content lives in `web/lib/docs/*.ts` as typed data; URLs and the release
asset-name rules live in `web/lib/site.ts` and nowhere else. See
[website.md](website.md).

## Changing a detector pattern

1. Reproduce the banner as a fixture in `internal/detector/testdata` (or inline
   as a string) — include the real punctuation, including the `·`.
2. Add or adjust the rule, and make sure normalisation (lowercase, single
   spaces, folded quotes/dashes) is what the pattern sees.
3. Add a **negative** case too: prose *about* a limit must not match.
4. `go test ./internal/detector/ -v` and check the confidence the event carries.

Users can do the same without a release, via `detector.custom_patterns`; see
[configuration.md](configuration.md).

## Changing the schema

1. Add `internal/storage/migrations/000N_name.sql` with both directions, numbered
   contiguously from 1 — `loadMigrations` rejects gaps and duplicates.
2. Keep it forward-only at runtime: an upgraded binary adopts an older database,
   an older binary cannot read a newer one.
3. Add a migration test: apply, assert the table/column, roll back, assert it is
   gone, and reopen the database to prove persistence.

## Conventions

- `gofmt` only; no other formatter. CI runs the non-mutating check.
- Errors are wrapped with `%w` and matched with `errors.Is`; exported sentinels
  live next to the package that raises them and every message is safe to log (no
  tokens, no raw URLs).
- No shell, ever: `exec.Command` with one argv element per value. There is a test
  asserting this.
- Time is always injected; no `time.Now()` outside `internal/clock`.
- Comments explain *why* a thing must hold, not what the next line does.
- New user-facing behaviour means a doc change in the same commit — the pages
  under `docs/` are checked against the code, not written from memory.

## What CI runs

`ci.yml` on every push to `main` and every pull request:

1. `make fmt-check`, `go vet ./...`, `go build ./...`
2. `go test ./...`, then `go test -race ./...`
3. `make cover-check` (80% gate) and a coverage artifact
4. the website: `npm ci`, lint, `tsc --noEmit`, `npm run test`, `npm run build`,
   and the static export as an artifact
5. `shellcheck -x scripts/*.sh`

`release.yml` re-runs vet, tests and the coverage gate before anything is built.
