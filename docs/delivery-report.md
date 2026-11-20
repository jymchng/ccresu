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
| Resume the original session | `claude --bg --resume <session-id> --name ccresu-<first-8-hex> "<message>"` |
| Prove the resume | the CLI's `backgrounded · <short-id> · <name>` block, then the published `state` from `claude agents --json --all` |

The resume argv is built in exactly one place (`claude.HandoffArgs`,
`internal/claude/handoff.go`), is passed as separate argv elements with no shell,
and forks nothing — the continuation goes to Claude Code's background supervisor,
not to a process ccresu owns. The CLI answers a handoff with a
`backgrounded · <short-id> · <name>` block; a handoff that prints no block is
`ErrHandoffNotAcknowledged`, because ccresu could not then name what it created.
`--bg` and `-p` cannot be combined, so the retired print-mode vector is absent by
construction rather than by convention. A session type the CLI reports but ccresu
does not understand becomes `UNRECOVERABLE` with the reported type in the reason —
never an invented command.

## Quality gates, measured

Run at `HEAD` on a clean tree:

| Gate | Command | Result |
| --- | --- | --- |
| Formatting | `gofmt -l .` | no output |
| Vet | `go vet ./...` | clean |
| Tests | `go test ./...` | all 16 Go packages with tests pass |
| Race | `go test -race ./...` | pass |
| Coverage | `make cover-check` | **86.9%** (gate 80%) |
| Website lint | `npm run lint` | clean |
| Website types | `npx tsc --noEmit` | clean |
| Website tests | `npm run test` | 18 files, 143 tests pass |
| Website build | `npm run build` | static export, 15 routes |

Suite size: 93 Go test files, 835 test functions, plus 18 web test files.

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
| `internal/recovery` | 94.5% | ≥90% |
| `internal/claude` | 89.7% | ≥80% |
| `internal/server` | 88.0% | ≥80% |
| `internal/storage` | 85.6% | ≥85% |
| `internal/app` | 84.7% | ≥80% |
| `cmd/ccresu` | 100.0% | — |
| `tests/deployment` | no statements (artefact test) | — |
| `tests/e2e` | no statements (behavioural) | — |
| `tests/integration` | no statements (behavioural) | — |
| `tests/support` | 0.0% (fixture package) | — |

Two rows move between runs, and the table quotes the aggregate `go test ./... -cover`
run rather than a lucky one. `internal/app` reports 84.5%, 84.6% or 84.7%: the
lines that differ sit inside the daemon's stage functions — `Step`,
`actOnSchedules` and `recoverPending` in `daemon.go` — where a branch is reached
only when a test's timing goes one way. `internal/scheduler` reports 96.0% or
96.2% because `MemoryStore.LoadSchedules` sorts a slice built from a map, so its
comparator takes different branches depending on the iteration order. Every other
row held to the tenth across repeated runs, with one near-miss: `internal/server`
measured 88.0% in isolation and in eight of nine aggregate runs, and 87.8% once
when the whole suite was competing for the machine.

The total is statement-weighted over everything `go test ./...` reports: the
twelve `internal/` packages, `cmd/ccresu` (100.0%, two statements), `tests/support`
(a fixture package the end-to-end tests import, which earns no credit for another
package's test run and so reports 0.0%) and the behavioural and artefact suites,
which have no statements of their own. Over the `internal/` packages only it is 90.0%. That
tree measures **88.6%**.

One caveat about that number, because it depends on whether the website's
dependencies are installed. After `cd web && npm ci`, `go test ./...` also walks
a vendored Go package inside `web/node_modules` (one file, 160 statements, no
tests of its own), and the very same run then reports **86.8%** — the figure in
the table above, and what `make cover-check` prints in this working tree. Both
figures are correct for their tree: CI's Go job runs `make cover-check` before the
website job installs anything, so the gate sees 88.6%, and a developer who has
already run `npm ci` sees 86.8% and the gate still passes.

## The end-to-end scenario

`tests/e2e/scenario_test.go` replays the canonical interruption through every
layer with only the process execution stubbed:

1. Discover the session (`claude agents --json` shape).
2. Match `You've hit your 5-hour limit · resets 12pm (Europe/Berlin)`.
3. Record the paused-goal companion message.
4. Resolve 12:00 **Europe/Berlin** — not the host zone.
5. Persist the schedule and wait.
6. Reach the reset, recheck, and hand the continuation to the supervisor, exactly
   once, for the same session id and under a `ccresu-<first-8-hex>` label derived
   from it: `--bg` first, `--resume <session-id>`, `--name <label>`, the message
   last, and no print-mode flag anywhere.
7. Prove the handoff from the CLI's `backgrounded · <short-id> · <name>` block. The
   test asserts the handoff was accepted, carries a state id, and — explicitly —
   that verification did **not** claim transcript growth.
8. Reach `RESUMED`, persisted, with one session row and the same project path; the
   attempt records the handoff state id, label, instant and copy flag, which is what
   tells a restarting ccresu the work is the supervisor's.
9. Queue and deliver exactly one Telegram success message.

This scenario proves the *handoff*, because that is where its proof now lies: its
provider returns an empty listing on purpose (`tests/support`), so the session is
"not observed" and the published state decides nothing here. The mapping that
reads the published state — one `claude agents --json --all` per pass, keyed by
`sessionId`, total over every documented value plus absent and unknown — is proven
where the listing is scripted: the `internal/recovery` acceptance tests. Those
also cover the other branch, where a `blocked` publication with a handoff
outstanding is read as a landed continuation — `RESUMED`, with the published
`waitingFor` recorded verbatim — instead of a park that sent no continuation. (At
the time of this report it parked the session in a needs-input state and raised a
needs-input milestone; 0.9.0 removed both, because a blocked conversation is one
waiting on a human, not a failed resume.)

Plus two tighter scenarios: a restart in the middle of the wait
(`TestScenarioSurvivesRestartMidWait`) and `ErrLimitNotCleared` — the limit is
still in force, so the session goes back to waiting instead of resuming
(`TestScenarioRefusesWhenTheLimitIsStillInForce`). The attempt-budget path to
Rediscovery after a spent budget is covered by the `internal/recovery` unit tests.

## Known gaps

Tracked as open goals, not hidden:

- `internal/recovery` is at 95.8%. The statements still uncovered are **not** all
  unreachable, and calling them defensive was wrong: the `LimitInForceError`
  message variants, the verification notes for a copied or unstamped handoff
  (`verify.go`), the `state.move` and store-error returns in `manager.go`, and
  `Reset` on a nil source are each reachable by a test that scripts them. They are
  an open goal — coverage debt, not written off as unreachable.
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
