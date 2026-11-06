#!/usr/bin/env bash
#
# Proves the dispatched-resume loop end to end, with the real ccresu binary and a
# stub `claude` on PATH -- not a Go-level fake.
#
# A resume is no longer a print-mode invocation that answers with JSON. It is
# `claude --resume <id> --bg <message>`, which acknowledges with
# `backgrounded · <job-id>` and continues the conversation under a session id of
# its own. The stub models exactly that: it prints the acknowledgement, registers
# the job in `claude agents --all --json`, reports it `working` for the first read
# and `done` afterwards, and grows the job's own transcript -- a different file
# from the one that was resumed. Nobody tells ccresu what to do; it has to follow
# the job and *prove* the conversation continued.
#
# The stop fallback is still proven. When the session is attached to a background
# agent the CLI refuses the dispatch ("That session is running in the background
# (837eaaea)"), and ccresu stops that job -- by its eight-hex name, never the
# session UUID -- and repeats the identical dispatch. The stub resolves the
# argument the way the real CLI does: only an eight-hex job name it holds, and
# anything else answered with `No job matching` and exit 1.
#
# Two sessions are set up in one scratch HOME:
#
#   A  resumable   -> DISPATCHED then VERIFIED, and a second limit in the
#                     continuation's own transcript drives the second cycle at
#                     the continuation's id, not the abandoned one.
#   B  poisoned    -> its resume always fails with an authentication error, so it
#                     ends DISCOVERED: a failed resume returns the session to
#                     discovery instead of a terminal failure state.
#
# Usage: tests/scripts/background-resume-e2e.sh [path-to-ccresu]
# With no path the binary is built into the scratch directory.
set -euo pipefail

ccresu=""
for arg in "$@"; do
	ccresu=$arg
done

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

export HOME="$work/home"
mkdir -p "$HOME"
stub_dir="$work/bin"
state="$work/stub"
project="$work/project"
mkdir -p "$stub_dir" "$state" "$project"
slug=$(printf '%s' "$project" | sed 's/[^A-Za-z0-9]/-/g')
projects="$HOME/.claude/projects/$slug"
mkdir -p "$projects"
export STUB_STATE="$state" STUB_PROJECTS="$projects" STUB_PROJECT="$project"

if [ -z "$ccresu" ]; then
	ccresu="$work/ccresu"
	(cd "$repo_root" && go build -o "$ccresu" ./cmd/ccresu)
fi

fail=0
check() { # check <description> <actual> <expected>
	if [ "$2" = "$3" ]; then
		printf 'ok    %s\n' "$1"
	else
		printf 'FAIL  %s\n      got:  %s\n      want: %s\n' "$1" "$2" "$3"
		fail=1
	fi
}
check_nonzero() { # check_nonzero <description> <exit-code>
	if [ "$2" -ne 0 ]; then
		printf 'ok    %s (exit %s)\n' "$1" "$2"
	else
		printf 'FAIL  %s\n      exit code was 0, expected a refusal\n' "$1"
		fail=1
	fi
}
contains() { # contains <description> <haystack> <needle>
	case "$2" in
	*"$3"*) printf 'ok    %s\n' "$1" ;;
	*)
		printf 'FAIL  %s\n      %s does not contain %s\n' "$1" "$2" "$3"
		fail=1
		;;
	esac
}
lacks() { # lacks <description> <haystack> <needle>
	case "$2" in
	*"$3"*)
		printf 'FAIL  %s\n      %s contains %s\n' "$1" "$2" "$3"
		fail=1
		;;
	*) printf 'ok    %s\n' "$1" ;;
	esac
}

# ---------------------------------------------------------------- stub claude
# Every call is recorded with its argv, one call per line, fields separated by
# \x1f, so two invocations can be compared byte for byte.
cat >"$stub_dir/claude" <<'STUB'
#!/bin/sh
set -u
state="$STUB_STATE"
{
	printf 'CALL'
	for a in "$@"; do printf '\037%s' "$a"; done
	printf '\n'
} >>"$state/calls.log"

# The retired print-mode flags must never be passed again. Failing here is the
# point: a silent fallback would let the argv drift without any test noticing.
case " $* " in
*" -p "*|*" --print "*|*" --output-format "*)
	printf 'stub: ccresu passed a retired headless flag: %s\n' "$*" >&2
	exit 9
	;;
esac

case "${1:-}" in
--version)
	printf '2.1.223 (Claude Code)\n'
	exit 0
	;;
agents)
	# The listing is built from what is on disk: the seeded interactive
	# sessions plus every background job the stub has created. A job reports
	# `working` for its first read and `done` for every read after that, which
	# is what a dispatched turn looks like from the outside.
	printf '['
	separator=""
	if [ -f "$state/seed-agents" ]; then
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			printf '%s%s' "$separator" "$line"
			separator=","
		done <"$state/seed-agents"
	fi
	for descriptor in "$state"/job-*; do
		[ -f "$descriptor" ] || continue
		job=${descriptor##*/job-}
		session=$(sed -n '1p' "$descriptor")
		name=$(sed -n '2p' "$descriptor")
		job_state=$(sed -n '3p' "$descriptor")
		reads_file="$state/reads-$job"
		reads=0
		[ -f "$reads_file" ] && reads=$(cat "$reads_file")
		reads=$((reads + 1))
		printf '%s' "$reads" >"$reads_file"
		if [ "$job_state" = working ] && [ "$reads" -gt 1 ]; then
			job_state=done
		fi
		printf '%s{"id":"%s","session_id":"%s","cwd":"%s","kind":"background","status":"busy","state":"%s","name":"%s"}' \
			"$separator" "$job" "$session" "$STUB_PROJECT" "$job_state" "$name"
		separator=","
	done
	printf ']\n'
	exit 0
	;;
stop)
	id=${2:-}
	# The real CLI resolves an eight-hex job name and answers anything else --
	# a session UUID included -- with exit 1 and this line.
	case "$id" in
	[a-f0-9][a-f0-9][a-f0-9][a-f0-9][a-f0-9][a-f0-9][a-f0-9][a-f0-9]) ;;
	*)
		printf "No job matching '%s'. Run 'claude agents' to list running sessions.\n" "$id" >&2
		exit 1
		;;
	esac
	removed=0
	for marker in "$state"/background-*; do
		[ -f "$marker" ] || continue
		if [ "$(cat "$marker")" = "$id" ]; then
			rm -f "$marker"
			removed=1
		fi
	done
	if [ "$removed" -ne 1 ]; then
		printf "No job matching '%s'. Run 'claude agents' to list running sessions.\n" "$id" >&2
		exit 1
	fi
	printf 'stopped %s\n' "$id"
	exit 0
	;;
esac

# `--resume <id> --bg <message>`
id=""
previous=""
for a in "$@"; do
	if [ "$previous" = "--resume" ]; then id=$a; fi
	previous=$a
done
if [ -z "$id" ]; then
	printf 'stub: no --resume id in %s\n' "$*" >&2
	exit 2
fi

if [ -f "$state/background-$id" ]; then
	# The CLI's refusal, verbatim except for the name: the session is attached
	# to a background agent, whose job name is what `claude stop` accepts.
	job=$(cat "$state/background-$id")
	printf 'Error: That session is running in the background (%s). Run `claude attach %s` to open it, or `claude stop %s` first to resume it here. Add --fork-session to branch off a copy instead.\n' \
		"$job" "$job" "$job" >&2
	exit 1
fi
if [ -f "$state/poison-$id" ]; then
	printf 'Invalid API key · Please run /login\n' >&2
	exit 1
fi

# A real dispatch: the job continues the conversation under an id of its own, in
# a transcript of its own, and the acknowledgement names the job.
count=0
[ -f "$state/dispatches" ] && count=$(cat "$state/dispatches")
count=$((count + 1))
printf '%s' "$count" >"$state/dispatches"
continuation=$(printf 'dddddddd-1111-4111-8111-%012d' "$count")
job=$(printf 'c0ffee%02d' "$count")
transcript="$STUB_PROJECTS/$continuation.jsonl"
printf '{"type":"user","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"user","content":"continue"}}\n' \
	"$continuation" "$STUB_PROJECT" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$transcript"
printf '{"type":"assistant","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"continued the goal"}]}}\n' \
	"$continuation" "$STUB_PROJECT" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$transcript"
printf '%s\n%s\n%s\n' "$continuation" "nightly-audit" "working" >"$state/job-$job"
printf '%s' "$transcript" >"$state/transcript-$continuation"
printf 'backgrounded \302\267 %s\n' "$job"
exit 0
STUB
chmod +x "$stub_dir/claude"
export PATH="$stub_dir:$PATH"

# --------------------------------------------------------------- scratch state
session_a=aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa
session_b=bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb
# An explicit, unambiguous instant that has already passed, so the schedule is
# due in the very next pass. A bare clock time would be read as the *next*
# occurrence of that time and the run would sit in WAITING_FOR_RESET.
reset_at=$(date -d '2 hours ago' '+%Y-%m-%d %H:%M')
banner=$(printf "You've hit your 5-hour limit \xc2\xb7 resets %s" "$reset_at")

seed_transcript() { # seed_transcript <id>
	local id=$1 path="$projects/$1.jsonl"
	printf '{"type":"user","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"user","content":"start the migration"}}\n' \
		"$id" "$project" "$(date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ)" >"$path"
	printf '{"type":"assistant","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}\n' \
		"$id" "$project" "$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)" "$banner" >>"$path"
}
seed_transcript "$session_a"
seed_transcript "$session_b"

# A is attached to a background agent under its own eight-hex name, so the first
# dispatch is refused and ccresu has to stop that job and dispatch again. B is
# poisoned, so its dispatch can never succeed.
job_a=$(printf '%s' "$session_a" | cut -c1-8)
printf '%s' "$job_a" >"$state/background-$session_a"
: >"$state/poison-$session_b"

cat >"$state/seed-agents" <<JSON
{"id":"agent-a","session_id":"$session_a","cwd":"$project","kind":"interactive","status":"idle"}
{"id":"agent-b","session_id":"$session_b","cwd":"$project","kind":"interactive","status":"idle"}
JSON

"$ccresu" config init >/dev/null

recovery_env() { # recovery_env <timeout> <command...>
	local timeout=$1
	shift
	CCRESU_RECOVERY__VERIFICATION_TIMEOUT="$timeout" \
		CCRESU_RECOVERY__HEALTH_CHECK_INTERVAL=1s \
		"$@"
}

# ---------------------------------------------------------------- pass one
printf '\n== pass 1: discover, opt in, and let the daemon plan the wait\n'
CCRESU_RECOVERY__ENABLED=false "$ccresu" daemon --once || true
"$ccresu" enable "$session_a" >/dev/null
"$ccresu" enable "$session_b" >/dev/null
# Recovery stays off for this pass: the limits are recorded and the waits are
# planned, which leaves both sessions at RESET_TIME_REACHED for `ccresu recover`.
CCRESU_RECOVERY__ENABLED=false "$ccresu" daemon --once || true

set +e
recover_a=$(recovery_env 20s "$ccresu" recover "$session_a" 2>&1)
recover_a_code=$?
recover_b=$(recovery_env 20s "$ccresu" recover "$session_b" 2>&1)
recover_b_code=$?
set -e
printf -- '--- recover %s\n%s\n' "$session_a" "$recover_a"
printf -- '--- recover %s\n%s\n' "$session_b" "$recover_b"

inspect_a=$(recovery_env 20s "$ccresu" inspect "$session_a" 2>&1)
inspect_b=$(recovery_env 20s "$ccresu" inspect "$session_b" 2>&1)
field() { printf '%s' "$1" | sed -n "s/^$2:[[:space:]]*\(.*\)$/\1/p" | head -1; }

continuation_one=$(field "$inspect_a" continuation)
job_one=$(field "$inspect_a" job)

# ------------------------------------------------------------ the call log
# Each line is CALL<US>arg<US>arg...; awk splits on that separator so a call can
# be matched field by field instead of by a fragile regular expression.
dispatch_calls=$(awk -F'\037' -v id="$session_a" '$2=="--resume" && $3==id && $4=="--bg"' \
	"$state/calls.log" | wc -l | tr -d ' ')
stop_calls=$(awk -F'\037' -v job="$job_a" '$2=="stop" && $3==job' "$state/calls.log" | wc -l | tr -d ' ')
stop_uuid_calls=$(awk -F'\037' -v id="$session_a" '$2=="stop" && $3==id' "$state/calls.log" | wc -l | tr -d ' ')
stop_arg=$(awk -F'\037' '$2=="stop" { print $3; exit }' "$state/calls.log")

check "recover A exits 0" "$recover_a_code" "0"
check "session A ends RESUMED" "$(field "$inspect_a" state)" "RESUMED"
contains "recover A proves the continuation grew" "$recover_a" "proven"
check "A records the job the dispatch created" "$job_one" "c0ffee01"
check "A records the continuation the job reported" "$continuation_one" \
	"dddddddd-1111-4111-8111-000000000001"
check_nonzero "recover B is refused" "$recover_b_code"
check "session B is rediscovered" "$(field "$inspect_b" state)" "DISCOVERED"

check "the dispatch ran twice for A (refused, then stopped and repeated)" "$dispatch_calls" "2"
check "one stop call cleared the background agent" "$stop_calls" "1"
check "the stop named the eight-hex job, not the session" "$stop_arg" "$job_a"
check "the session UUID was never passed to claude stop" "$stop_uuid_calls" "0"

first_dispatch=$(awk -F'\037' -v id="$session_a" '$2=="--resume" && $3==id && $4=="--bg"' "$state/calls.log" | sed -n '1p')
second_dispatch=$(awk -F'\037' -v id="$session_a" '$2=="--resume" && $3==id && $4=="--bg"' "$state/calls.log" | sed -n '2p')
check "both dispatches used byte-identical arguments" "$first_dispatch" "$second_dispatch"

# The retired print-mode invocation must not appear anywhere in the call log.
lacks "no call passed -p" "$(cat "$state/calls.log")" " -p "
lacks "no call passed --output-format" "$(cat "$state/calls.log")" "--output-format"
check "the stub was never handed a retired flag" "$(grep -c 'retired headless flag' "$state/calls.log" 2>/dev/null || true)" "0"

# `claude stop` refuses a session UUID, which is what made the old stop always
# fail. The stub reproduces that, so the assertion above means something.
set +e
stop_refusal=$(claude stop "$session_a" 2>&1)
stop_refusal_code=$?
set -e
check_nonzero "claude stop refuses a session UUID" "$stop_refusal_code"
contains "the refusal names the argument" "$stop_refusal" "No job matching '$session_a'"

history_a=$(recovery_env 20s "$ccresu" --json history "$session_a" 2>&1)
contains "the audit row keeps the dispatched command" "$history_a" "claude --resume $session_a --bg"
contains "the audit row reports the continuation" "$history_a" '"ContinuationSessionID": "dddddddd-1111-4111-8111-000000000001"'
contains "the continuation was confirmed" "$history_a" '"SessionIDVerified": true'
contains "the transcript grew" "$history_a" '"TranscriptGrew": true'

printf '\n== the stub claude call log (A only)\n'
awk -F'\037' -v id="$session_a" -v cont="$continuation_one" '$3==id || $3==cont || $2=="stop"' \
	"$state/calls.log" | sed 's/^CALL //'

# ------------------------------------------------------------- the second cycle
# The conversation now lives in the continuation's transcript. A new limit there
# must be detected, planned and resumed -- at the continuation's id, because that
# is the session that answers now.
printf '\n== pass 2: the next limit arrives in the continuation transcript\n'
next_reset=$(date -d '3 hours ago' '+%Y-%m-%d %H:%M')
next_banner=$(printf "You've hit your 5-hour limit \xc2\xb7 resets %s" "$next_reset")
next_transcript="$projects/$continuation_one.jsonl"
printf '{"type":"assistant","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}\n' \
	"$continuation_one" "$project" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$next_banner" >>"$next_transcript"

CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s CCRESU_RECOVERY__HEALTH_CHECK_INTERVAL=1s \
	"$ccresu" daemon --once || true

inspect_a=$(recovery_env 20s "$ccresu" inspect "$session_a" 2>&1)
second_cycle_calls=$(awk -F'\037' -v cont="$continuation_one" '$2=="--resume" && $3==cont && $4=="--bg"' \
	"$state/calls.log" | wc -l | tr -d ' ')
check "the second cycle dispatched at the continuation's id" "$second_cycle_calls" "1"
check "the loop dispatched once per cycle" "$(cat "$state/dispatches")" "2"
check "session A ends the second cycle RESUMED" "$(field "$inspect_a" state)" "RESUMED"
check "A tracks the newest continuation" "$(field "$inspect_a" continuation)" \
	"dddddddd-1111-4111-8111-000000000002"
check "the abandoned original id was never resumed twice more" \
	"$(awk -F'\037' -v id="$session_a" '$2=="--resume" && $3==id' "$state/calls.log" | wc -l | tr -d ' ')" "2"

# The continuation the first cycle created was not resumed a second time: the
# second dispatch continued the conversation under yet another id.
history_a=$(recovery_env 20s "$ccresu" --json history "$session_a" 2>&1)
contains "the second attempt records its own command" "$history_a" \
	"claude --resume $continuation_one --bg"

# ------------------------------------------------------- the removed commands
# PERMANENTLY_FAILED no longer exists, so nothing needs `reset` or
# `sessions clear` to get out of it. Both are gone; a script that still calls
# them must fail loudly rather than silently doing nothing.
printf '\n== pass 3: the retired commands are gone\n'
set +e
"$ccresu" reset "$session_b" >/dev/null 2>&1
reset_code=$?
"$ccresu" sessions clear >/dev/null 2>&1
clear_code=$?
set -e
check "ccresu reset is an unknown command" "$reset_code" "2"
check "ccresu sessions clear is an unknown subcommand" "$clear_code" "2"

printf '\n'
if [ "$fail" -ne 0 ]; then
	printf 'FAILED: at least one end-to-end expectation did not hold\n'
	exit 1
fi
printf 'PASSED: the dispatched job was followed, verified, and the loop came round again\n'
