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
| Resume the original session | `claude -p --resume <session-id> "<message>" --output-format json` |

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
| Tests | `go test ./...` | all 14 Go packages with tests pass |
| Race | `go test -race ./...` | pass |
| Coverage | `make cover-check` | **86.2%** (gate 80%) |
| Website lint | `npm run lint` | clean |
| Website types | `npx tsc --noEmit` | clean |
| Website tests | `npm run test` | 7 files, 36 tests pass |
| Website build | `npm run build` | static export, 11 routes |

Suite size: 62 Go test files, 625 test functions, plus 7 web test files.

Per-package statement coverage:

| Package | Coverage | Target |
| --- | --- | --- |
| `internal/logging` | 100.0% | — |
| `internal/config` | 98.6% | ≥90% |
| `internal/detector` | 98.6% | ≥95% |
| `internal/process` | 98.3% | ≥80% |
| `internal/scheduler` | 96.0% | ≥95% |
| `internal/telegram` | 95.8% | ≥90% |
| `internal/clock` | 93.3% | — |
| `internal/recovery` | 92.3% | ≥90% |
| `internal/claude` | 90.0% | ≥80% |
| `internal/server` | 87.9% | ≥80% |
| `internal/storage` | 87.5% | ≥85% |
| `internal/app` | 81.0% | ≥80% |
| `tests/e2e` | no statements (behavioural) | — |

The 86.2% total is measured over everything `go test ./...` reports, which
includes `cmd/ccresu` (0.0%, no test files), `tests/support` (a fixture package)
and a vendored Go file inside `web/node_modules`. Over the `internal/` packages
only it is ~91%.

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
`PERMANENTLY_FAILED` is covered by the `internal/recovery` unit tests.

## Known gaps

Tracked as open goals, not hidden:

- `internal/recovery` edge paths (`verify.go`, `manager.go`) can go deeper than
  the current 92.3%.
- `telegram.parse_mode` still accepts `MarkdownV2` while the templates are
  HTML-only; the safe value is `HTML` (the default).
- A failed pre-resume status check can attempt an illegal
  `CHECKING_SESSION → PERMANENTLY_FAILED` transition instead of `RETRY_WAIT`.
- Global flags are accepted before the subcommand only (`ccresu --json status`
  works, `ccresu status --json` exits 2).
- `tests/integration` is empty; `cmd/ccresu` has no tests of its own.
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
