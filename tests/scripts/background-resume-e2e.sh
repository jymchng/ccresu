#!/usr/bin/env bash
#
# Proves the recovery handoff end to end, with the real ccresu binary and a stub
# `claude` on PATH -- not a Go-level fake.
#
# The contract under test is the handoff:
#
#	claude --bg --resume <session-id> --name ccresu-<first-8-hex> <message>
#
# One invocation, one answer. The supervisor prints a
# `backgrounded . <short-id> . <name>` block, ccresu records it, and the command
# returns. There is no print mode, no `--output-format json`, no second resume
# and no `claude stop`: recovery hands the conversation over and the supervisor
# owns it from that moment. A harness that stopped the job and resumed again
# would be describing a mechanism the binary no longer has, which is exactly the
# drift this script exists to prevent.
#
# The stub is the whole point. It keeps a "published state" per session and
# answers `claude agents --json --all` from it, so the real published-state read
# -- not a mock -- is what ccresu decides on. It records every call with its argv
# so the figure below can show the exact vector that was sent.
#
# Three sessions are set up in one scratch HOME:
#
#   A  handed-off   -> the stub prints the block and publishes `working`; the
#                      session must end RESUMED, with exactly one handoff and no
#                      stop, and the audit row must carry the state id and label.
#   B  refused      -> its handoff always fails with an authentication error, so
#                      it ends DISCOVERED: a failed resume is not a verdict on
#                      the conversation.
#   C  silent       -> its handoff exits 0 but prints no `backgrounded` block, so
#                      it ends DISCOVERED with `claude.ErrHandoffNotAcknowledged`
#                      and is not retried.
#
# Usage: tests/scripts/background-resume-e2e.sh
# The binary is built into the scratch directory from this checkout.
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

ccresu="$work/ccresu"
(cd "$repo_root" && go build -o "$ccresu" ./cmd/ccresu)

# The message is set explicitly so the figure shows a readable argv. It is a
# real configuration value, not a harness-only flag: the binary is still the
# thing that decides how to send it.
message="continue the existing goal"
export CCRESU_CLAUDE__CONTINUATION_MESSAGE="$message"

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
		printf 'FAIL  %s\n      %s still contains %s\n' "$1" "$2" "$3"
		fail=1
		;;
	*) printf 'ok    %s\n' "$1" ;;
	esac
}

# ---------------------------------------------------------------- stub claude
# Every call is recorded with its argv, one call per line, fields separated by
# \x1f, so an invocation can be matched field by field instead of with a fragile
# regular expression.
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
	# The version floor moved to 2.1.257 when print mode was retired (PRD 1), so
	# the stub must report a CLI that can hand off. Reporting the host's real
	# version would make this suite depend on whatever is installed.
	printf '2.1.257 (Claude Code)\n'
	exit 0
	;;
daemon)
	# `daemon status` answers exit 1 when no supervisor is running, which is the
	# documented "there is none yet" answer and the normal state before a first
	# handoff. It is not a failure, and ccresu must not treat it as one.
	printf 'no supervisor is running\n' >&2
	exit 1
	;;
agents)
	# The listing is built from the per-session published-state files, so a state
	# appears only once the stub has accepted a handoff for that session.
	printf '['
	sep=''
	while IFS="$(printf '\t')" read -r id cwd; do
		[ -n "$id" ] || continue
		printf '%s' "$sep"
		sep=','
		if [ -f "$state/state-$id" ]; then
			published=$(cat "$state/state-$id")
			printf '{"id":"%s","sessionId":"%s","cwd":"%s","kind":"background","status":"busy","state":"%s"}' \
				"$(printf '%s' "$id" | cut -c1-8)" "$id" "$cwd" "$published"
		else
			printf '{"id":"%s","sessionId":"%s","cwd":"%s","kind":"interactive","status":"idle"}' \
				"$(printf '%s' "$id" | cut -c1-8)" "$id" "$cwd"
		fi
	done <"$state/sessions"
	printf ']\n'
	exit 0
	;;
stop)
	# A stop is never part of a recovery. Recording it here means a regression
	# that reintroduced one shows up in the call log instead of passing quietly.
	printf 'stopped %s\n' "${2:-}"
	exit 0
	;;
esac

# The handoff: `--bg --resume <id> --name <label> <message>`.
id=""
previous=""
for a in "$@"; do
	if [ "$previous" = "--resume" ]; then id=$a; fi
	previous=$a
done

if [ -f "$state/poison-$id" ]; then
	# Octal escapes, not \xNN: this stub runs under /bin/sh, and POSIX printf
	# defines \ooo but not \xNN.
	printf 'Invalid API key \302\267 Please run /login\n' >&2
	exit 1
fi
if [ -f "$state/silent-$id" ]; then
	# Exit 0, but no block: the supervisor said nothing about the session it was
	# given, which is a refusal ccresu names rather than retries.
	printf 'Starting background service\342\200\246\n'
	exit 0
fi

# Accepted. The supervisor owns the conversation from here: it publishes a state
# for it and appends the turn it is running to the transcript, which is why the
# growth below is a diagnostic and not the proof.
short=$(printf '%s' "$id" | cut -c1-8)
printf '%s' 'working' >"$state/state-$id"
transcript=$(cat "$state/transcript-path-$id")
printf '{"type":"assistant","sessionId":"%s","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"continued the goal"}]}}\n' \
	"$id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$transcript"
printf 'Starting background service\342\200\246\n'
printf 'backgrounded \302\267 %s \302\267 ccresu-%s\n' "$short" "$short"
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
session_c=cccccccc-3333-4333-8333-cccccccccccc
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
}
for id in "$session_a" "$session_b" "$session_c"; do
	seed_transcript "$id"
done

# The sessions the stub's `agents` listing reports, and the two refusals.
printf '%s\t%s\n' \
	"$session_a" "$project" \
	"$session_b" "$project" \
	"$session_c" "$project" >"$state/sessions"
: >"$state/poison-$session_b"
: >"$state/silent-$session_c"

"$ccresu" config init >/dev/null

# ---------------------------------------------------------------- pass one
printf '\n== pass 1: discover, opt in, then `ccresu recover` each session\n'
# The first pass only discovers and detects: recovery is switched off for this
# single invocation so the daemon records the usage limit and parks the sessions
# instead of handing them off itself. `ccresu recover` is then the thing that
# issues the handoff.
CCRESU_RECOVERY__ENABLED=false "$ccresu" daemon --once || true
for id in "$session_a" "$session_b" "$session_c"; do
	"$ccresu" enable "$id" >/dev/null
done
CCRESU_RECOVERY__ENABLED=false "$ccresu" daemon --once || true

set +e
recover_a=$("$ccresu" recover "$session_a" 2>&1)
recover_a_code=$?
recover_b=$("$ccresu" recover "$session_b" 2>&1)
recover_b_code=$?
recover_c=$("$ccresu" recover "$session_c" 2>&1)
recover_c_code=$?
set -e
printf -- '--- recover %s\n%s\n' "$session_a" "$recover_a"
printf -- '--- recover %s\n%s\n' "$session_b" "$recover_b"
printf -- '--- recover %s\n%s\n' "$session_c" "$recover_c"

inspect_a=$("$ccresu" inspect "$session_a" 2>&1)
inspect_b=$("$ccresu" inspect "$session_b" 2>&1)
inspect_c=$("$ccresu" inspect "$session_c" 2>&1)
state_field() { printf '%s' "$1" | sed -n 's/^state:[[:space:]]*\(.*\)$/\1/p' | head -1; }

# ------------------------------------------------------------ the call log
# Each line is CALL<US>arg<US>arg...; awk splits on that separator so an
# invocation is matched field by field.
handoff_count() { # handoff_count <session-id>
	awk -F'\037' -v id="$1" '$2=="--bg" && $3=="--resume" && $4==id' "$state/calls.log" | wc -l | tr -d ' '
}
handoff_a=$(handoff_count "$session_a")
handoff_b=$(handoff_count "$session_b")
handoff_c=$(handoff_count "$session_c")

# The exact vector, field by field: --bg --resume <id> --name <label> <message>.
first_handoff=$(awk -F'\037' -v id="$session_a" '$2=="--bg" && $3=="--resume" && $4==id' "$state/calls.log" | sed -n '1p')
printf '%s' "$first_handoff" | awk -F'\037' '{ for (i=2; i<=NF; i++) print $i }' >"$work/handoff.argv"
{
	printf '%s\n' "--bg" "--resume" "$session_a" "--name" "ccresu-aaaaaaaa" "$message"
} >"$work/handoff.want"
argv_diff=$(diff -u "$work/handoff.want" "$work/handoff.argv" || true)
check "the handoff vector is --bg --resume <id> --name <label> <message>" "$argv_diff" ""

# Nothing print-mode shaped, anywhere: not on the handoff, not on any call.
print_mode_calls=$(awk -F'\037' '{ for (i=2; i<=NF; i++) if ($i=="-p" || $i=="--print" || $i=="--output-format" || $i=="--json-schema" || $i=="--max-turns" || $i=="--max-budget-usd") { print; break } }' \
	"$state/calls.log" | wc -l | tr -d ' ')
check "no call carried a print-mode flag" "$print_mode_calls" "0"
stop_calls=$(awk -F'\037' '$2=="stop"' "$state/calls.log" | wc -l | tr -d ' ')
check "no stop was issued" "$stop_calls" "0"

check "recover A exits 0" "$recover_a_code" "0"
check "session A ends RESUMED" "$(state_field "$inspect_a")" "RESUMED"
contains "recover A is proven by the supervisor's acceptance" "$recover_a" \
	"proven: the continuation was handed to the background supervisor"
check "the handoff ran exactly once for A" "$handoff_a" "1"
check_nonzero "recover B is refused" "$recover_b_code"
check "session B is rediscovered" "$(state_field "$inspect_b")" "DISCOVERED"
check_nonzero "recover C is refused" "$recover_c_code"
check "session C is rediscovered" "$(state_field "$inspect_c")" "DISCOVERED"
check "the silent handoff ran exactly once for C" "$handoff_c" "1"

# ------------------------------------------------------------- the audit row
history_a=$("$ccresu" history "$session_a" 2>&1)
contains "history records the handoff as the success" "$history_a" "RESUMED"
lacks "history does not credit transcript growth for the handoff" "$history_a" "transcript grew"

attempt=$("$ccresu" --json history "$session_a" 2>&1)
contains "the audit row keeps the handoff command" "$attempt" "--bg --resume"
contains "the audit row keeps the supervisor's state id" "$attempt" '"HandoffStateID": "aaaaaaaa"'
contains "the audit row keeps the handoff label" "$attempt" '"HandoffName": "ccresu-aaaaaaaa"'
contains "the audit row keeps the session id" "$attempt" '"SessionIDVerified": true'
# A handoff is not proven by growth: the transcript is not measured for one at
# all, so the row says so rather than claiming evidence it does not have.
contains "the audit row does not claim growth proved the handoff" "$attempt" '"TranscriptGrew": false'
contains "the reason names the acceptance, not the growth" "$attempt" \
	"the supervisor accepted the continuation as aaaaaaaa (ccresu-aaaaaaaa)"

# The refusal C produced is the named sentinel, not a generic failure.
error_c=$("$ccresu" --json history "$session_c" 2>&1)
contains "C records the missing block verbatim" "$error_c" "printed no backgrounded block"

printf '\n== the stub claude call log\n'
sed 's/^CALL //' "$state/calls.log" | tr '\037' ' '

# ------------------------------------------------------- the removed commands
# PERMANENTLY_FAILED no longer exists, so nothing needs `reset` or
# `sessions clear` to get out of it. Both are gone; a script that still calls
# them must fail loudly rather than silently doing nothing.
printf '\n== pass 2: the retired commands are gone\n'
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
printf 'PASSED: the session was handed to the supervisor once, and the block it printed is the proof\n'
