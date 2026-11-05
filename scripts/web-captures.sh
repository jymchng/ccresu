#!/usr/bin/env bash
#
# Regenerates every command-output figure the website shows, from the real
# binary -- not from memory, not from a mock.
#
# The site is a marketing and documentation site for a tool whose whole promise
# is "this is what actually happens". A hand-typed terminal mockup is therefore
# a lie waiting to happen: it keeps looking plausible after the CLI's wording
# changes. This script removes the copying step. It:
#
#   1. builds ./cmd/ccresu into a scratch directory,
#   2. gives it a scratch HOME, a scratch project and a stub `claude` so
#      discovery, detection and the database are exercised offline,
#   3. runs the commands whose output the pages show, and captures stdout,
#   4. runs the real end-to-end proof (`background-resume-e2e.sh`) and captures
#      its log,
#   5. writes the results to web/lib/captures/index.ts (imported by the site)
#      and to web/tests/fixtures/captures/<id>.txt (the parity fixture the
#      Vitest suite compares the rendered figure against).
#
# Two kinds of value in the output are environment-dependent and are scrubbed
# so that regenerating on another machine does not produce a fake diff:
#
#   * scratch paths become `~` or `<project>`,
#   * wall-clock instants become `<timestamp>`.
#
# Everything else is the binary's own text, byte for byte.
#
# Usage:
#   scripts/web-captures.sh              # everything, including the e2e log
#   scripts/web-captures.sh --no-e2e     # skip the ~2 minute end-to-end run
#   scripts/web-captures.sh --check      # verify the committed captures are
#                                        # current; exit 1 if they are not
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out_ts="$repo_root/web/lib/captures/index.ts"
out_fixtures="$repo_root/web/tests/fixtures/captures"

with_e2e=1
check=0
for arg in "$@"; do
	case "$arg" in
	--no-e2e) with_e2e=0 ;;
	--check) check=1 ;;
	*)
		printf 'unknown argument: %s\n' "$arg" >&2
		exit 2
		;;
	esac
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# A fixed clock and timezone keep the scrubbed output identical across hosts.
export TZ=UTC
export HOME="$work/home"
mkdir -p "$HOME" "$work/bin" "$work/stub" "$work/captures"

ccresu="$work/ccresu"
(cd "$repo_root" && go build -o "$ccresu" ./cmd/ccresu)

# ------------------------------------------------------------------ stub claude
# Discovery talks to `claude agents --json`; stubbing it keeps the capture
# offline and repeatable while still exercising the real parser, the real
# transcript read and the real database writes.
cat >"$work/bin/claude" <<'STUB'
#!/bin/sh
case "${1:-}" in
--version)
	printf '2.1.223 (Claude Code)\n'
	;;
agents)
	cat "$STUB_AGENTS"
	;;
*)
	printf 'claude: unsupported invocation in the capture harness: %s\n' "$*" >&2
	exit 1
	;;
esac
STUB
chmod +x "$work/bin/claude"
export PATH="$work/bin:$PATH"
export STUB_AGENTS="$work/stub/agents.json"

# ------------------------------------------------------------- scratch session
project="$work/project"
mkdir -p "$project"
slug=$(printf '%s' "$project" | sed 's/[^A-Za-z0-9]/-/g')
projects="$HOME/.claude/projects/$slug"
mkdir -p "$projects"

session=4d1f9c2a-0000-4000-8000-1a2b3c4d5e6f
transcript="$projects/$session.jsonl"
# The transcript is written "now" so the session counts as live; the instant is
# only ever visible in the `LAST SEEN` column, which is scrubbed below. The reset
# time in the banner is fixed text, so the schedule and the history reason it
# produces are stable from run to run.
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
banner=$(printf "You've hit your 5-hour limit \xc2\xb7 resets 2020-01-01 12:00")
printf '{"type":"user","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"user","content":"start the migration"}}\n' \
	"$session" "$project" "$now" >"$transcript"
printf '{"type":"assistant","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}\n' \
	"$session" "$project" "$now" "$banner" >>"$transcript"

cat >"$STUB_AGENTS" <<JSON
[
  {"id":"agent-a","session_id":"$session","cwd":"$project","kind":"interactive","status":"idle"}
]
JSON

"$ccresu" config init >/dev/null 2>&1
# Pass one discovers. The session is then enabled by hand, because detection only
# runs for sessions recovery is allowed to touch, and pass two is what turns the
# banner in the transcript into a persisted USAGE_LIMIT_DETECTED and a schedule.
# Recovery itself stays off for both passes: this capture is the *waiting* state,
# not a resume.
CCRESU_RECOVERY__ENABLED=false "$ccresu" daemon --once >/dev/null 2>&1 || true
"$ccresu" enable "$session" >/dev/null 2>&1
CCRESU_RECOVERY__ENABLED=false "$ccresu" daemon --once >/dev/null 2>&1 || true

# -------------------------------------------------------------------- scrubbing
# Two values are machine-local and are rewritten so that regenerating on another
# host produces no fake diff: the scratch paths, and the wall-clock columns. The
# wall-clock placeholder is padded back to the original width so the tables keep
# their column alignment. The reset instant in `history` is *not* scrubbed: it
# comes from the transcript's own banner text and is part of the story.
# The project path appears inside a table column, so the replacement is padded
# back to the original width; otherwise the columns to its right would shift.
project_token="<project>$(printf '%*s' $(( ${#project} - 9 )) '')"

scrub() {
	sed -u \
		-e "s|$HOME|~|g" \
		-e "s|$work/project|$project_token|g" \
		-e "s|$work|<scratch>|g" \
		-e 's|[0-9]\{4\}-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9] [+-][0-9][0-9]|(captured-at)            |g' \
		-e 's/[[:space:]]*$//'
}

capture() { # capture <id> <command...>
	local id=$1
	shift
	"$@" >"$work/captures/$id.raw" 2>&1 || true
	scrub <"$work/captures/$id.raw" >"$work/captures/$id.txt"
	printf 'captured %-10s %s\n' "$id" "$*"
}

capture help "$ccresu" --help
capture status "$ccresu" status
capture sessions "$ccresu" sessions --all
capture history "$ccresu" history "$session"

if [ "$with_e2e" -eq 1 ]; then
	# The real proof, including the stub's call log: it is the only honest way to
	# show that the resume, the stop and the second resume really happened.
	# Two kinds of line are dropped so the figure is the report and not the
	# plumbing: module-download chatter from the build, and the daemon's JSON
	# log lines, which repeat what the report already states.
	(cd "$repo_root" && tests/scripts/background-resume-e2e.sh) \
		>"$work/captures/e2e.raw" 2>&1 || true
	grep -v -e '^go: ' -e '^{"time":' "$work/captures/e2e.raw" \
		| scrub >"$work/captures/e2e.txt"
	printf 'captured %-10s %s\n' e2e "tests/scripts/background-resume-e2e.sh"
fi

# --------------------------------------------------------------------- emission
if [ "$check" -eq 1 ]; then
	# Compare against the committed files without touching them.
	drift=0
	ids="help status sessions history"
	if [ "$with_e2e" -eq 1 ]; then
		ids="$ids e2e"
	fi
	for id in $ids; do
		fixture="$out_fixtures/$id.txt"
		if [ ! -f "$fixture" ]; then
			printf 'MISSING %s\n' "${fixture#"$repo_root"/}"
			drift=1
			continue
		fi
		if ! diff -u "$fixture" "$work/captures/$id.txt" >/dev/null; then
			printf 'STALE   %s\n' "${fixture#"$repo_root"/}"
			diff -u "$fixture" "$work/captures/$id.txt" | head -20
			drift=1
		fi
	done
	if [ "$drift" -ne 0 ]; then
		printf '\nThe committed captures no longer match the binary. Run: make web-captures\n' >&2
		exit 1
	fi
	printf 'captures are current\n'
	exit 0
fi

mkdir -p "$out_fixtures" "$(dirname "$out_ts")"
for id in help status sessions history e2e; do
	if [ ! -s "$work/captures/$id.txt" ] && [ ! -f "$work/captures/$id.txt" ]; then
		continue
	fi
	cp "$work/captures/$id.txt" "$out_fixtures/$id.txt"
done

python3 - "$out_ts" "$work/captures" "$out_fixtures" <<'PY'
import json
import os
import subprocess
import sys
from datetime import datetime, timezone

out_ts, captured_dir, fixtures_dir = sys.argv[1], sys.argv[2], sys.argv[3]

# id -> (label, command, description)
manifest = [
    ("help", "The command surface", "ccresu --help",
     "The whole command surface in one screen: fourteen commands, the global flags and the exit-code contract."),
    ("status", "Where ccresu is right now", "ccresu status",
     "One session, parked after the usage-limit banner was detected: a real database, reported by the real binary."),
    ("sessions", "Every session it knows about", "ccresu sessions --all",
     "Every discovered session, including the ones recovery is not allowed to touch."),
    ("history", "The audit trail for one session", "ccresu history <session-id>",
     "The audit trail: discovery, detection, the wait and the reset instant, each recorded as it happened."),
    ("e2e", "The end-to-end proof", "make e2e",
     "The end-to-end proof, verbatim: the stub claude CLI refuses the resume, is stopped, is resumed again, and the call log shows the two byte-identical resumes around one stop."),
]


def read(capture_id):
    path = os.path.join(captured_dir, capture_id + ".txt")
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as handle:
        return handle.read()


def git(*args):
    try:
        return subprocess.run(
            ["git", *args], cwd=os.path.dirname(os.path.dirname(os.path.abspath(out_ts))),
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    except Exception:
        return "unknown"


entries = []
for capture_id, label, command, description in manifest:
    output = read(capture_id)
    if output is None:
        continue
    entries.append((capture_id, label, command, description, output))

if not entries:
    sys.exit("web-captures: nothing was captured")

lines = [
    "/**",
    " * GENERATED FILE -- do not edit by hand.",
    " *",
    " * Regenerate with `make web-captures` (scripts/web-captures.sh). The script",
    " * builds ./cmd/ccresu, runs it against a scratch HOME and a stub `claude`,",
    " * and writes the output here verbatim. Scratch paths and wall-clock values",
    " * are scrubbed so a regeneration on another machine produces no fake diff;",
    " * nothing else is rewritten.",
    " *",
    " * These strings are what the documentation pages render, and",
    " * web/tests/captures.test.tsx fails if a rendered figure disagrees with",
    " * them or if these disagree with the fixtures beside it.",
    " */",
    "",
    "export type Capture = {",
    "  /** Stable id used by the documentation content to reference this figure. */",
    "  readonly id: string;",
    "  /** Human label shown above the figure. */",
    "  readonly label: string;",
    "  /** The command that produced the output, for the caption. */",
    "  readonly command: string;",
    "  /** One sentence saying what the reader is looking at. */",
    "  readonly description: string;",
    "  /** The binary's own output, verbatim. */",
    "  readonly output: string;",
    "};",
    "",
    "/** Where these captures came from; regenerated with the file itself. */",
    "export const captureSource = {",
    '  generator: "scripts/web-captures.sh",',
    f'  generated: "{datetime.now(timezone.utc).strftime("%Y-%m-%d")}",',
    f'  commit: "{git("rev-parse", "--short", "HEAD")}",',
    '  binary: "ccresu (built from this commit)",',
    "} as const;",
    "",
    "export const captures = {",
]
for capture_id, label, command, description, output in entries:
    lines.append(f"  {capture_id}: {{")
    lines.append(f"    id: {json.dumps(capture_id)},")
    lines.append(f"    label: {json.dumps(label)},")
    lines.append(f"    command: {json.dumps(command)},")
    lines.append(f"    description: {json.dumps(description)},")
    lines.append(f"    output: {json.dumps(output)},")
    lines.append("  },")
lines.append("} as const satisfies Record<string, Capture>;")
lines.append("")
lines.append("/** Every capture, in the order the pages introduce them. */")
lines.append("export const captureList: readonly Capture[] = [")
for capture_id, *_ in entries:
    lines.append(f"  captures.{capture_id},")
lines.append("];")
lines.append("")
lines.append("/** Look one up by id, for content that references a figure by name. */")
lines.append("export function getCapture(id: string): Capture | undefined {")
lines.append("  return captureList.find((capture) => capture.id === id);")
lines.append("}")
lines.append("")

with open(out_ts, "w", encoding="utf-8") as handle:
    handle.write("\n".join(lines))

print(f"wrote {out_ts} ({len(entries)} captures)")
PY

printf '\nfixtures written to %s\n' "${out_fixtures#"$repo_root"/}"
