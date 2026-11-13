# Delivery report

A snapshot of the repository as delivered, with the numbers reproduced from a
clean checkout rather than remembered. Re-run the commands below to confirm them.

## What was built

`ccresu` is one Go binary that resumes the *same* Claude Code session after a
usage limit resets, and proves that it did. It is a CLI, a daemon and a loopback
REST/SSE API. It deliberately ships **no web UI**; `web/` is a static marketing
and documentation site that never talks to a running daemon.

| Area | Where |
| --- | --- |
| CLI and daemon orchestration | `internal/app`, `cmd/ccresu` |
| Cross-cutting: config, logging, clock, process | `internal/{config,logging,clock,process}` |
| Persistence (pure-Go SQLite, embedded migrations) | `internal/storage` |
| Claude Code discovery and resume | `internal/claude` |
| Usage-limit detection | `internal/detector` |
| Reset-time parsing and schedules | `internal/scheduler` |
| State machine, locking, backoff, verifier | `internal/recovery` |
| Telegram Bot API client and queue | `internal/telegram` |
| REST + SSE API | `internal/server` |
| Tests | `internal/*_test.go`, `tests/support`, `tests/e2e` |
| Operational artifacts | `deployments/systemd/`, `scripts/` |
| CI and releases | `.github/workflows/{ci,release,deploy-website}.yml` |
| Documentation | `docs/`, `README.md`, `web/` |

## Verified Claude Code interfaces

Only documented interfaces are used, all verified against Claude Code 2.1.223:

| Purpose | Interface |
| --- | --- |
| Discover sessions | `claude agents --json` (`--all`, `--cwd <path>`) |
| Transcripts | `~/.claude/projects/<slugified-cwd>/<session-id>.jsonl` |
| Per-session environment | `~/.claude/session-env/<session-id>/` |
| Resume the original session | `claude -p --resume <session-id> --output-format json "<message>"` |

The resume argv is built in exactly one place (`claude.ResumeArgs`), is passed as
separate argv elements with no shell, and forks nothing. A session type the CLI
reports but ccresu does not understand becomes `UNRECOVERABLE` with the reported
type in the reason — never an invented command.

## Quality gates, measured

Run at `HEAD` on a clean tree:

| Gate | Command | Result |
| --- | --- | --- |
| Formatting | `gofmt -l .` | no output |
| Vet | `go vet ./...` | clean |
| Tests | `go test ./...` | all 15 Go packages with tests pass |
| Race | `go test -race ./...` | pass |
| Coverage | `make cover-check` | **88.8%** (gate 80%) |
| Website lint | `npm run lint` | clean |
| Website types | `npx tsc --noEmit` | clean |
| Website tests | `npm run test` | 18 files, 143 tests pass |
| Website build | `npm run build` | static export, 15 routes |

Suite size: 83 Go test files, 779 test functions, plus 18 web test files.

Per-package statement coverage, from `go test ./... -cover`:

| Package | Coverage | Target |
| --- | --- | --- |
| `internal/logging` | 100.0% | — |
| `internal/config` | 98.7% | ≥90% |
| `internal/detector` | 98.6% | ≥95% |
| `internal/process` | 98.3% | ≥80% |
| `internal/scheduler` | 96.0% | ≥95% |
| `internal/telegram` | 96.0% | ≥90% |
| `internal/clock` | 93.3% | — |
| `internal/recovery` | 97.7% | ≥90% |
| `internal/claude` | 90.6% | ≥80% |
| `internal/server` | 88.0% | ≥80% |
| `internal/storage` | 85.8% | ≥85% |
| `internal/app` | 84.1% | ≥80% |
| `cmd/ccresu` | 100.0% | — |
| `tests/e2e` | no statements (behavioural) | — |
| `tests/integration` | no statements (behavioural) | — |
| `tests/support` | 0.0% (fixture package) | — |

Two rows move between runs, and the table quotes the aggregate `go test ./... -cover`
run rather than a lucky one. `internal/app` reports 84.0%, 84.1% or 84.2%: the
lines that differ sit inside the daemon's stage functions — `Step`,
`actOnSchedules` and `recoverPending` in `daemon.go` — where a branch is reached
only when a test's timing goes one way. `internal/scheduler` reports 96.0% or
96.2% because `MemoryStore.LoadSchedules` sorts a slice built from a map, so its
comparator takes different branches depending on the iteration order. Every other
row held to the tenth across repeated runs, with one near-miss: `internal/server`
measured 88.0% in isolation and in eight of nine aggregate runs, and 87.8% once
when the whole suite was competing for the machine.

The 88.8% total is statement-weighted over everything `go test ./...` reports in
a clean checkout: the twelve `internal/` packages, `cmd/ccresu` (100.0%, two
statements), `tests/support` (a fixture package the end-to-end tests import,
which earns no credit for another package's test run and so reports 0.0%) and
the two behavioural suites, which have no statements of their own. Over the
`internal/` packages only it is 90.1%.

One caveat about that number, because it depends on whether the website's
dependencies are installed. After `cd web && npm ci`, `go test ./...` also walks
a vendored Go package inside `web/node_modules` (160 statements, no tests of its
own), and the very same run then reports **86.9%**. Both figures are correct for
their tree: CI's Go job runs `make cover-check` before the website job installs
anything, so the gate sees 88.8%, and a developer who has already run `npm ci`
sees 86.9% and the gate still passes.

## The end-to-end scenario

`tests/e2e/scenario_test.go` replays the canonical interruption through every
layer with only the process execution stubbed:

1. Discover the session (`claude agents --json` shape).
2. Match `You've hit your 5-hour limit · resets 12pm (Europe/Berlin)`.
3. Record the paused-goal companion message.
4. Resolve 12:00 **Europe/Berlin** — not the host zone.
5. Persist the schedule and wait.
6. Reach the reset, recheck, resume with the same session id.
7. Prove the resume: id preserved *and* transcript grew.
8. Reach `RESUMED` with the attempt recorded.
9. Queue and deliver exactly one Telegram success message.

Plus two tighter scenarios: a restart in the middle of the wait
(`TestScenarioSurvivesRestartMidWait`) and `ErrLimitNotCleared` — the limit is
still in force, so the session goes back to waiting instead of resuming
(`TestScenarioRefusesWhenTheLimitIsStillInForce`). The attempt-budget path to
Rediscovery after a spent budget is covered by the `internal/recovery` unit tests.

## Known gaps

Tracked as open goals, not hidden:

- `internal/recovery` edge paths (`verify.go`, `manager.go`) are now at 97.7%;
  the statements left are unreachable defensive branches tracked as an open goal.
- `tests/integration` holds three test files and no non-test Go files, so it
  contributes no statements of its own; `cmd/ccresu` is a single test file over a
  two-statement `main`.
- The installer's guarantees are verified by hand, not by an automated test.

All are recorded in [troubleshooting.md](troubleshooting.md) where they are
user-visible, and in the issue tracker otherwise.

## How to verify this yourself

```bash
git clone https://github.com/jymchng/ccresu.git && cd ccresu
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
make cover-check
cd web && npm ci && npm run lint && npx tsc --noEmit && npm run test && npm run build
```

```bash
# a real installation, without touching your config until you say so
./scripts/install.sh --dry-run --version 0.1.0
```
