#!/usr/bin/env bash
#
# Proves the recovery end to end, with the real ccresu binary and a stub
# `claude` on PATH -- not a Go-level fake.
#
# The contract under test is the resume:
#
#	claude --resume <session-id> "<continuation message>"
#
# One invocation, one verdict. The command runs the whole turn in the session's
# own directory and returns when the turn is over, and its **exit status is the
# only thing ccresu judges the recovery on**: exit 0 means the conversation
# continued, and any other status is a failed attempt carrying its exit code and
# its stderr. There is no print mode, no background flag, no session label, no
# `claude stop` and no second resume. A harness that expected an acceptance block
# would be describing a mechanism the binary no longer has, which is exactly the
# drift this script exists to prevent.
#
# The transcript is measured once, after the exit, as a diagnostic. Session C
# exists to prove that: its turn finishes without the transcript changing shape
# and the session is still RESUMED, because growth is reported and never required.
#
# Three sessions are set up in one scratch HOME:
#
#   A  continues -> the stub exits 0 and appends the turn to the transcript, so
#                   the session ends RESUMED and the attempt records the growth
#                   the measurement found afterwards.
#   B  refused   -> the resume exits 1 with an authentication error, so the
#                   session ends DISCOVERED. An auth refusal classifies as
#                   permanent: repeating it on this schedule cannot help, so no
#                   retry instant is stored (`ccresu inspect` shows `at: none`)
#                   and the next discovery pass re-reads the transcript and
#                   decides afresh. It is rediscovered rather than abandoned
#                   because a failed resume is not a verdict on the conversation.
#   C  quiet     -> the resume exits 0 and writes nothing to the transcript, so
#                   the session still ends RESUMED and the measurement records
#                   that it did not grow.
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
	# The version floor is 2.1.257. Reporting the host's real version would make
	# this suite depend on whatever is installed.
	printf '2.1.257 (Claude Code)\n'
	exit 0
	;;
agents)
	# What discovery reads. Every seeded session is reported once, in the shape
	# `claude agents --json` prints. Nothing is published, because there is no
	# supervisor to publish anything: the listing only says which sessions exist
	# and where they run.
	printf '['
	sep=''
	while IFS="$(printf '\t')" read -r id cwd; do
		[ -n "$id" ] || continue
		printf '%s' "$sep"
		sep=','
		printf '{"id":"%s","sessionId":"%s","cwd":"%s","kind":"interactive","status":"idle"}' \
			"$(printf '%s' "$id" | cut -c1-8)" "$id" "$cwd"
	done <"$state/sessions"
	printf ']\n'
	exit 0
	;;
stop)
	# A stop is never part of a recovery. Recording it means a regression that
	# reintroduced one shows up in the call log instead of passing quietly.
	printf 'stopped %s\n' "${2:-}"
	exit 0
	;;
esac

# The resume: `--resume <id> <message>`. The message is positional and last, so
# the session id is the only other argument there is to find.
id=""
previous=""
for a in "$@"; do
	if [ "$previous" = "--resume" ]; then id=$a; fi
	previous=$a
done

if [ -f "$state/refuse-$id" ]; then
	# Octal escapes, not \xNN: this stub runs under /bin/sh, and POSIX printf
	# defines \ooo but not \xNN.
	printf 'Invalid API key \302\267 Please run /login\n' >&2
	exit 1
fi
if [ -f "$state/quiet-$id" ]; then
	# Exit 0 and write nothing: a turn that ran without the transcript ccresu
	# watches changing shape. The verdict is still the exit status.
	printf 'continued the goal\n'
	exit 0
fi

# A normal turn: it appends to the transcript and exits 0. The growth below is
# what the diagnostic records afterwards; it is not what makes the recovery
# succeed.
transcript=$(cat "$state/transcript-path-$id")
printf '{"type":"assistant","sessionId":"%s","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"continued the goal"}]}}\n' \
	"$id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$transcript"
printf 'continued the goal\n'
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

# The sessions the stub's `agents` listing reports, and the two shapes a resume
# can come back in other than a plain success.
printf '%s\t%s\n' \
	"$session_a" "$project" \
	"$session_b" "$project" \
	"$session_c" "$project" >"$state/sessions"
: >"$state/refuse-$session_b"
: >"$state/quiet-$session_c"

"$ccresu" config init >/dev/null

# ---------------------------------------------------------------- pass one
# The backticks are prose quoting a command name, not a command substitution:
# SC2016 would have this string double-quoted, which turns the literal
# `ccresu recover` in the banner into an execution.
# shellcheck disable=SC2016
printf '\n== pass 1: discover, opt in, then `ccresu recover` each session\n'
# The first pass only discovers and detects: recovery is switched off for this
# single invocation so the daemon records the usage limit and parks the sessions
# instead of resuming them itself. `ccresu recover` is then the thing that runs
# the resume.
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
resume_count() { # resume_count <session-id>
	awk -F'\037' -v id="$1" '$2=="--resume" && $3==id' "$state/calls.log" | wc -l | tr -d ' '
}
resume_a=$(resume_count "$session_a")
resume_b=$(resume_count "$session_b")
resume_c=$(resume_count "$session_c")

# The exact vector, field by field: --resume <id> <message>. Three arguments,
# nothing else: the message is positional and last, so there is no separator and
# no flag between the id and the prompt.
first_resume=$(awk -F'\037' -v id="$session_a" '$2=="--resume" && $3==id' "$state/calls.log" | sed -n '1p')
printf '%s' "$first_resume" | awk -F'\037' '{ for (i=2; i<=NF; i++) print $i }' >"$work/resume.argv"
{
	printf '%s\n' "--resume" "$session_a" "$message"
} >"$work/resume.want"
argv_diff=$(diff -u "$work/resume.want" "$work/resume.argv" || true)
check 'the resume vector is --resume <id> "<message>" and nothing else' "$argv_diff" ""

# No resume may carry a fourth argument, and no call may carry a flag the
# contract retired: no background flag, no session label, no print mode.
resume_shapes=$(awk -F'\037' '$2=="--resume" && NF!=4' "$state/calls.log" | wc -l | tr -d ' ')
check "every resume was exactly three arguments" "$resume_shapes" "0"
retired_calls=$(awk -F'\037' '{ for (i=2; i<=NF; i++) if ($i=="--bg" || $i=="--name" || $i=="-p" || $i=="--print" || $i=="--output-format" || $i=="--max-turns" || $i=="--max-budget-usd" || $i=="--fallback-model" || $i=="--permission-mode") { print; break } }' \
	"$state/calls.log" | wc -l | tr -d ' ')
check "no call carried a retired flag" "$retired_calls" "0"
stop_calls=$(awk -F'\037' '$2=="stop"' "$state/calls.log" | wc -l | tr -d ' ')
check "no stop was issued" "$stop_calls" "0"
daemon_calls=$(awk -F'\037' '$2=="daemon"' "$state/calls.log" | wc -l | tr -d ' ')
check "no supervisor was asked for" "$daemon_calls" "0"

# ----------------------------------------------------------------- the verdict
check "recover A exits 0" "$recover_a_code" "0"
check "session A ends RESUMED" "$(state_field "$inspect_a")" "RESUMED"
contains "recover A is proven by the exit status" "$recover_a" \
	"proven: the resume finished its turn and the transcript grew"
check "the resume ran exactly once for A" "$resume_a" "1"

check_nonzero "recover B is refused" "$recover_b_code"
# B's refusal was a real attempt, not a skipped one: exactly one resume was sent
# for it, and the failure was not retried into a second.
check "the refused resume was attempted exactly once for B" "$resume_b" "1"
check "session B is rediscovered" "$(state_field "$inspect_b")" "DISCOVERED"
contains "the refusal records the exit status" "$recover_b" "failed (exit 1)"
contains "the refusal keeps the stub's stderr" "$recover_b" "Invalid API key"

# C is the whole point of the diagnostic being a diagnostic: its turn ran and the
# transcript did not change shape, and the session is still RESUMED.
check "recover C exits 0" "$recover_c_code" "0"
check "session C ends RESUMED without the transcript growing" "$(state_field "$inspect_c")" "RESUMED"
contains "C's verdict is the exit status" "$recover_c" "proven: the resume finished its turn"
lacks "C's verification does not claim growth" "$recover_c" "the transcript grew"
check "the resume ran exactly once for C" "$resume_c" "1"

# ------------------------------------------------------------- the audit row
history_a=$("$ccresu" history "$session_a" 2>&1)
contains "history records the resume as the success" "$history_a" "RESUMED"
attempt=$("$ccresu" --json history "$session_a" 2>&1)
contains "the audit row keeps the resume command" "$attempt" "--resume $session_a"
contains "the audit row records the growth the measurement found" "$attempt" '"TranscriptGrew": true'
# Nothing from the retired handoff may survive in the row an operator reads.
lacks "the audit row carries no handoff state id" "$attempt" '"HandoffStateID"'
lacks "the audit row carries no handoff label" "$attempt" '"HandoffName"'
lacks "the audit row carries no supervisor acceptance" "$attempt" "background supervisor"

attempt_c=$("$ccresu" --json history "$session_c" 2>&1)
contains "C's audit row says the transcript did not grow" "$attempt_c" '"TranscriptGrew": false'

printf '\n== the stub claude call log\n'
sed 's/^CALL //' "$state/calls.log" | tr '\037' ' '

# ------------------------------------------------------- the removed commands
# The retired commands must stay retired: a script that still calls them has to
# fail loudly rather than silently doing nothing.
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
printf 'PASSED: one resume per session, and its exit status is the verdict\n'
