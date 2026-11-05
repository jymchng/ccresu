#!/usr/bin/env bash
#
# Proves the background-session fix end to end, with the real ccresu binary and
# a stub `claude` on PATH -- not a Go-level fake.
#
# The stub is the whole point: it keeps a "background session" state file, makes
# the first `claude -p --resume <id>` fail with the CLI's real refusal text,
# makes `claude stop <id>` clear that state, and makes the second, identical
# resume print a valid JSON result and grow the transcript. Nobody tells ccresu
# what to do; it has to notice the refusal, stop the session, resume again and
# then *prove* the conversation continued.
#
# Two sessions are set up in one scratch HOME:
#
#   A  backgrounded   -> must end RESUMED, with the stop call visible
#   B  poisoned       -> its resume always fails with an authentication error,
#                        so it must end PERMANENTLY_FAILED, which is then the
#                        fixture for `ccresu reset`.
#
# Usage: tests/scripts/background-resume-e2e.sh [path-to-ccresu]
# With no argument the binary is built into the scratch directory.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

export HOME="$work/home"
mkdir -p "$HOME"
stub_dir="$work/bin"
state="$work/stub"
mkdir -p "$stub_dir" "$state"
export STUB_STATE="$state"

if [ $# -ge 1 ]; then
	ccresu=$1
else
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
contains() { # contains <description> <haystack> <needle>
	case "$2" in
	*"$3"*) printf 'ok    %s\n' "$1" ;;
	*)
		printf 'FAIL  %s\n      %s does not contain %s\n' "$1" "$2" "$3"
		fail=1
		;;
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

case "${1:-}" in
--version)
	printf '2.1.223 (Claude Code)\n'
	exit 0
	;;
agents)
	cat "$state/agents.json"
	exit 0
	;;
stop)
	id=${2:-}
	rm -f "$state/background-$id"
	printf 'stopped %s\n' "$id"
	exit 0
	;;
esac

# `-p --resume <id> <message> --output-format json`
id=""
previous=""
for a in "$@"; do
	if [ "$previous" = "--resume" ]; then id=$a; fi
	previous=$a
done

if [ -f "$state/background-$id" ]; then
	# The CLI's refusal, verbatim.
	printf 'Error: That session is running in the background (837eaaea). Run `claude attach 837eaaea` to open it, or `claude stop 837eaaea` first to resume it here. Add --fork-session to branch off a copy instead.\n' >&2
	exit 1
fi
if [ -f "$state/poison-$id" ]; then
	printf 'Invalid API key · Please run /login\n' >&2
	exit 1
fi

# A real resume: the transcript grows, and the CLI reports the same session id.
transcript=$(cat "$state/transcript-path-$id")
printf '{"type":"assistant","sessionId":"%s","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"continued the goal"}]}}\n' \
	"$id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$transcript"
printf '{"type":"result","subtype":"success","session_id":"%s","result":"continued the goal","is_error":false,"num_turns":2,"duration_ms":1500,"total_cost_usd":0.01,"model":"claude-sonnet-4"}\n' "$id"
exit 0
STUB
chmod +x "$stub_dir/claude"
export PATH="$stub_dir:$PATH"

# --------------------------------------------------------------- scratch state
project="$work/project"
mkdir -p "$project"
slug=$(printf '%s' "$project" | sed 's/[^A-Za-z0-9]/-/g')
projects="$HOME/.claude/projects/$slug"
mkdir -p "$projects"

session_a=aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa
session_b=bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb
# An explicit, unambiguous instant that has already passed, so the schedule is
# due in the very next pass. A bare clock time would be read as the *next*
# occurrence of that time and the run would sit in WAITING_FOR_RESET.
reset_at=$(date -d '2 hours ago' '+%Y-%m-%d %H:%M')
# printf interprets the escapes in its format string, so the transcript carries a
# real UTF-8 middle dot rather than a literal \xc2\xb7 that would invalidate the JSON.
banner=$(printf "You've hit your 5-hour limit \xc2\xb7 resets %s" "$reset_at")

seed_transcript() { # seed_transcript <id>
	local id=$1 path="$projects/$1.jsonl"
	printf '{"type":"user","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"user","content":"start the migration"}}\n' \
		"$id" "$project" "$(date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ)" >"$path"
	printf '{"type":"assistant","sessionId":"%s","cwd":"%s","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}\n' \
		"$id" "$project" "$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)" "$banner" >>"$path"
	printf '%s' "$path" >"$state/transcript-path-$id"
	printf '%s\n' "$path"
}
seed_transcript "$session_a" >/dev/null
seed_transcript "$session_b" >/dev/null

# A keeps its background agent; B is poisoned so its resume can never succeed.
: >"$state/background-$session_a"
: >"$state/poison-$session_b"

cat >"$state/agents.json" <<JSON
[
  {"id":"agent-a","session_id":"$session_a","cwd":"$project","kind":"interactive","status":"idle"},
  {"id":"agent-b","session_id":"$session_b","cwd":"$project","kind":"interactive","status":"idle"}
]
JSON

configure() {
	"$ccresu" config init >/dev/null
	# The verifier polls until the transcript grows; the stub grows it at once.
	CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" config validate >/dev/null
}

# ---------------------------------------------------------------- pass one
configure
printf '\n== pass 1: discover, opt in, then one reconcile pass against the stub CLI\n'
# Discovery alone only records the sessions; a session is recovered after the
# operator has enabled it. Two passes, exactly as the tutorial describes.
CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" daemon --once || true
"$ccresu" enable "$session_a" >/dev/null
"$ccresu" enable "$session_b" >/dev/null
CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" daemon --once || true

state_a=$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" inspect "$session_a" 2>&1)
check "session A ends RESUMED" "$(printf '%s' "$state_a" | sed -n 's/^state:[[:space:]]*\(.*\)$/\1/p' | head -1)" "RESUMED"
state_b=$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" inspect "$session_b" 2>&1)
check "session B ends PERMANENTLY_FAILED" "$(printf '%s' "$state_b" | sed -n 's/^state:[[:space:]]*\(.*\)$/\1/p' | head -1)" "PERMANENTLY_FAILED"

history_a=$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" history "$session_a" 2>&1)
contains "history records the stop-then-resume success" "$history_a" "RESUMED"

# The proof that the conversation continued, as ccresu itself recorded it: the
# attempt row carries the resume command and both verification flags.
attempt=$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" --json history "$session_a" 2>&1)
contains "the audit row keeps the one resume command" "$attempt" "claude -p --resume"
contains "session id was preserved" "$attempt" '"SessionIDVerified": true'
contains "the transcript grew" "$attempt" '"TranscriptGrew": true'

# ------------------------------------------------------------ the call log
# Each line is CALL<US>arg<US>arg...; awk splits on that separator so a call can
# be matched field by field instead of by a fragile regular expression.
resume_calls=$(awk -F'\037' -v id="$session_a" '$2=="-p" && $3=="--resume" && $4==id' \
	"$state/calls.log" | wc -l | tr -d ' ')
check "the resume ran exactly twice for A" "$resume_calls" "2"
stop_calls=$(awk -F'\037' -v id="$session_a" '$2=="stop" && $3==id' "$state/calls.log" | wc -l | tr -d ' ')
check "one stop call cleared the background agent" "$stop_calls" "1"

first_resume=$(awk -F'\037' -v id="$session_a" '$2=="-p" && $3=="--resume" && $4==id' "$state/calls.log" | sed -n '1p')
second_resume=$(awk -F'\037' -v id="$session_a" '$2=="-p" && $3=="--resume" && $4==id' "$state/calls.log" | sed -n '2p')
check "both resumes used byte-identical arguments" "$first_resume" "$second_resume"

# The order has to be resume, stop, resume -- not stop first.
sequence=$(awk -F'\037' -v id="$session_a" 'index($0, id) { if ($2=="-p") print "-p"; else if ($2=="stop") print "stop" }' \
	"$state/calls.log" | tr '\n' ' ')
check "the calls run resume, stop, resume" "$(printf '%s' "$sequence" | sed 's/ *$//')" "-p stop -p"

printf '\n== the stub claude call log (A only)\n'
grep "$session_a" "$state/calls.log" | sed 's/\x1f/ /g' | sed 's/^CALL //'

# ---------------------------------------------------------------- reset flow
printf '\n== pass 2: ccresu reset on the permanently failed session\n'
reset_out=$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" reset "$session_b" 2>&1)
printf '%s\n' "$reset_out"
after_reset=$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" inspect "$session_b" 2>&1)
check "reset moves B to DISABLED" "$(printf '%s' "$after_reset" | sed -n 's/^state:[[:space:]]*\(.*\)$/\1/p' | head -1)" "DISABLED"
# The session-level error is cleared; the attempt that failed keeps its reason,
# because reset must not rewrite what happened.
header=$(printf '%s' "$after_reset" | awk 'BEGIN { RS = "" } NR == 1')
check "reset clears the session's stored error" "$(printf '%s' "$header" | grep -c '^error:' || true)" "0"
history_b=$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" --json history "$session_b" 2>&1 || true)
check "the attempt history was kept" "$(printf '%s' "$history_b" | grep -c '"AttemptNumber"' || true)" "1"
contains "the kept attempt still records why it failed" "$history_b" "Invalid API key"

# reset must refuse a state it cannot leave: B is DISABLED now.
set +e
CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" reset "$session_b" >/dev/null 2>&1
refusal=$?
set -e
check "a second reset is refused with exit 4" "$refusal" "4"

CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" enable "$session_b" >/dev/null
enabled=$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" --json sessions --all)
contains "enable after reset leaves B recoverable" "$enabled" '"ID": "'"$session_b"'"'
check "and enabled again" "$(CCRESU_RECOVERY__VERIFICATION_TIMEOUT=20s "$ccresu" inspect "$session_b" | sed -n 's/^enabled:[[:space:]]*\(.*\)$/\1/p' | head -1)" "yes"

printf '\n'
if [ "$fail" -ne 0 ]; then
	printf 'FAILED: at least one end-to-end expectation did not hold\n'
	exit 1
fi
printf 'PASSED: the background session was stopped, resumed and verified end to end\n'
