#!/usr/bin/env bash
#
# Behavioural tests for scripts/install.sh.
#
# The installer is the one component that runs as a plain shell script on an
# unknown machine, so the only honest way to keep its promises is to execute it:
# this suite serves a fake GitHub release mirror over HTTP, packages a real
# ccresu binary built with the version it claims, and then drives the installer
# exactly as a user would.
#
# What is proved, one scenario at a time:
#
#   1. a pinned install downloads from /releases/download/v<ver>/, verifies the
#      archive against checksums.txt and lands a binary that reports the pinned
#      version (with the shipped example config validating afterwards);
#   2. the download URLs use the pinned-tag shape — never
#      /releases/latest/download/<tag>/..., which GitHub does not serve;
#   3. re-running the same version is a no-op: no re-download, no rewrite;
#   4. an upgrade replaces the binary, and --force re-downloads on demand;
#   5. a tampered checksum aborts with nothing installed;
#   6. a checksums.txt without an entry for the asset aborts with nothing
#      installed;
#   7. --dry-run writes nothing and downloads nothing;
#   8. argument errors are usage errors, not partial installs;
#   9. an explicitly exported XDG_CONFIG_HOME/XDG_DATA_HOME is honoured instead
#      of the ~/.config and ~/.local/share defaults;
#  10. piped into bash from outside a checkout — the documented one-liner — it
#      still installs, and --systemd fetches the unit from the release tag
#      instead of failing because there is no repository beside it.
#
# The installer resolves its directories from XDG_* when they are set, so every
# invocation below runs with those variables scrubbed: the suite asserts paths
# relative to an isolated HOME, and a runner that exports XDG_CONFIG_HOME (the
# GitHub-hosted images do) would otherwise send the config somewhere the
# assertions never look. Scenario 9 opts back in deliberately.
#
# Usage: bash tests/scripts/install_test.sh
# Requires: a POSIX userland (tar, sha256sum), curl, python3 (the mirror) and Go
# (the archive must contain a real binary). CI runs it in the scripts job.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
readonly ROOT
readonly INSTALL_SCRIPT="$ROOT/scripts/install.sh"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/ccresu-install-test.XXXXXX")"
readonly WORK
readonly MIRROR="$WORK/mirror"
readonly MIRROR_LOG="$WORK/mirror.log"
readonly PORT_FILE="$WORK/mirror.port"

SERVER_PID=""
TESTS=0

cleanup() {
	if [ -n "$SERVER_PID" ]; then
		kill "$SERVER_PID" 2>/dev/null || true
		wait "$SERVER_PID" 2>/dev/null || true
	fi
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# Assertions
# ---------------------------------------------------------------------------

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	printf '(work directory kept at %s)\n' "$WORK" >&2
	exit 1
}

pass() {
	TESTS=$((TESTS + 1))
	printf 'ok %d - %s\n' "$TESTS" "$*"
}

assert_contains() {
	# assert_contains HAYSTACK NEEDLE MESSAGE
	local haystack="$1" needle="$2" message="$3"
	case "$haystack" in
	*"$needle"*) ;;
	*) fail "$message (missing: $needle)"$'\n'"--- output ---"$'\n'"$haystack" ;;
	esac
}

assert_not_contains() {
	local haystack="$1" needle="$2" message="$3"
	case "$haystack" in
	*"$needle"*) fail "$message (unexpected: $needle)"$'\n'"--- output ---"$'\n'"$haystack" ;;
	esac
}

assert_exists() {
	[ -e "$1" ] || fail "$2 (missing: $1)"
}

assert_absent() {
	[ ! -e "$1" ] || fail "$2 (unexpected: $1)"
}

assert_equal() {
	[ "$1" = "$2" ] || fail "$3 (got '$1', want '$2')"
}

# ---------------------------------------------------------------------------
# Platform and fixtures
# ---------------------------------------------------------------------------

detect_os() {
	case "$(uname -s)" in
	Linux) printf 'linux' ;;
	Darwin) printf 'darwin' ;;
	*) fail "this suite runs on linux or darwin, not $(uname -s)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) printf 'amd64' ;;
	arm64 | aarch64) printf 'arm64' ;;
	*) fail "this suite needs an amd64 or arm64 host, not $(uname -m)" ;;
	esac
}

OS="$(detect_os)"
readonly OS
ARCH="$(detect_arch)"
readonly ARCH

need_tool() {
	command -v "$1" >/dev/null 2>&1 || fail "the test suite needs '$1' on PATH"
}

for tool in curl python3 tar sha256sum go; do
	need_tool "$tool"
done

asset_name() { printf 'ccresu_%s_%s_%s.tar.gz' "$1" "$OS" "$ARCH"; }

# build_binary VERSION - compiles a real ccresu reporting VERSION, once per run.
build_binary() {
	local version="$1"
	local out="$WORK/build/$version/ccresu"
	if [ ! -x "$out" ]; then
		mkdir -p "$WORK/build/$version"
		(cd "$ROOT" && go build -trimpath \
			-ldflags "-X main.version=$version -X main.commit=installtest -X main.buildDate=2026-01-01" \
			-o "$out" ./cmd/ccresu) || fail "go build for version $version failed"
	fi
	printf '%s' "$out"
}

# publish VERSION - packages that binary the way release.yml does and writes a
# correct checksums.txt next to it, all under the pinned tag directory.
publish() {
	local version="$1" dir asset binary staging sums
	dir="$MIRROR/jymchng/ccresu/releases/download/v$version"
	asset="$(asset_name "$version")"
	mkdir -p "$dir"

	binary="$(build_binary "$version")"
	staging="$WORK/staging/$version"
	rm -rf "$staging"
	mkdir -p "$staging/configs"
	cp "$binary" "$staging/ccresu"
	cp "$ROOT/configs/config.example.yaml" "$staging/configs/config.example.yaml"
	# The release archive holds exactly these two entries (see release.yml).
	tar -czf "$dir/$asset" -C "$staging" ccresu configs

	sums="$(sha256sum "$dir/$asset" | awk '{print $1}')"
	printf '%s  %s\n' "$sums" "$asset" >"$dir/checksums.txt"
	printf '%s' "$dir"
}

# publish_with_checksums VERSION CHECKSUMS_BODY - publishes the archive but
# writes CHECKSUMS_BODY verbatim, so a test can corrupt the manifest.
publish_with_checksums() {
	local version="$1" body="$2" dir asset
	dir="$(publish "$version")"
	asset="$(asset_name "$version")"
	printf '%s\n' "$body" >"$dir/checksums.txt"
	printf '%s' "$dir"
}

# publish_tag_files VERSION - serve the files a piped installer has to read from
# the release tag rather than from a checkout. The release archive holds only the
# binary and configs/ (see release.yml), so the systemd unit has to come from
# here.
publish_tag_files() {
	local version="$1" dir
	dir="$MIRROR/jymchng/ccresu/raw/v$version/deployments/systemd"
	mkdir -p "$dir"
	cp "$ROOT/deployments/systemd/ccresu.service" "$dir/ccresu.service"
}

# start_mirror - serves $MIRROR on a loopback port and records every request path.
start_mirror() {
	local script="$WORK/mirror.py"
	cat >"$script" <<'PY'
import http.server
import os
import sys

root, log_path, port_path = sys.argv[1], sys.argv[2], sys.argv[3]
os.chdir(root)


class Handler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, fmt, *args):
        # Only release traffic is recorded: the readiness probe below asks for
        # "/" and must not look like a download.
        if self.path.startswith("/jymchng/"):
            with open(log_path, "a", encoding="utf-8") as handle:
                handle.write(self.path + "\n")


server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
with open(port_path, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_address[1]))
server.serve_forever()
PY
	rm -f "$MIRROR_LOG" "$PORT_FILE"
	: >"$MIRROR_LOG"
	python3 "$script" "$MIRROR" "$MIRROR_LOG" "$PORT_FILE" &
	SERVER_PID=$!

	local deadline=0 port="" code=""
	while [ "$deadline" -lt 200 ]; do
		if [ -s "$PORT_FILE" ]; then
			port="$(cat "$PORT_FILE")"
			code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/" || true)"
			if [ -n "$code" ] && [ "$code" != "000" ]; then
				BASE_URL="http://127.0.0.1:$port"
				readonly BASE_URL
				return 0
			fi
		fi
		sleep 0.05
		deadline=$((deadline + 1))
	done
	fail "the fake release mirror did not start"
}

# run_install HOME_DIR ARGS... - runs the installer with an isolated HOME and
# records its output and exit code in INSTALL_OUTPUT / INSTALL_STATUS.
#
# XDG_* is scrubbed so the ambient environment cannot redirect the paths the
# caller is about to assert on. `env -u` is used rather than unsetting in this
# shell so the suite itself keeps its own environment intact.
run_install() {
	local home="$1"
	shift
	mkdir -p "$home"
	if INSTALL_OUTPUT="$(env -u XDG_CONFIG_HOME -u XDG_DATA_HOME -u XDG_STATE_HOME -u XDG_CACHE_HOME \
		-u CCRESU_PREFIX HOME="$home" CCRESU_ALLOW_ROOT=1 CCRESU_BASE_URL="$BASE_URL" \
		bash "$INSTALL_SCRIPT" "$@" 2>&1)"; then
		INSTALL_STATUS=0
	else
		INSTALL_STATUS=$?
	fi
}

# run_install_with_xdg HOME_DIR CONFIG_HOME DATA_HOME ARGS... - the same, but
# with the XDG base directories exported, so the test can prove the installer
# obeys them (scenario 9).
run_install_with_xdg() {
	local home="$1" config_home="$2" data_home="$3"
	shift 3
	mkdir -p "$home"
	if INSTALL_OUTPUT="$(env -u XDG_STATE_HOME -u XDG_CACHE_HOME -u CCRESU_PREFIX \
		HOME="$home" XDG_CONFIG_HOME="$config_home" XDG_DATA_HOME="$data_home" \
		CCRESU_ALLOW_ROOT=1 CCRESU_BASE_URL="$BASE_URL" \
		bash "$INSTALL_SCRIPT" "$@" 2>&1)"; then
		INSTALL_STATUS=0
	else
		INSTALL_STATUS=$?
	fi
}

# run_install_piped HOME_DIR ARGS... - runs the installer the way the README
# tells users to: the script arrives on stdin (`bash -s -- ...`) from a working
# directory that is not a checkout, so `$0` is "bash" and nothing in the
# repository is reachable by a relative path.
#
# A fake systemctl is prepended to PATH: the suite must not touch the runner's
# user services, and the assertion is that the installer *tried* to install and
# enable the unit.
run_install_piped() {
	local home="$1"
	shift
	mkdir -p "$home" "$WORK/fakebin" "$WORK/nocheckout"
	cat >"$WORK/fakebin/systemctl" <<'SH'
#!/bin/sh
printf '%s\n' "$*" >>"$SYSTEMCTL_LOG"
SH
	chmod +x "$WORK/fakebin/systemctl"
	: >"$WORK/systemctl.log"
	if INSTALL_OUTPUT="$(cd "$WORK/nocheckout" && env -u XDG_CONFIG_HOME -u XDG_DATA_HOME -u XDG_STATE_HOME -u XDG_CACHE_HOME \
		-u CCRESU_PREFIX -u CCRESU_VERSION HOME="$home" CCRESU_ALLOW_ROOT=1 CCRESU_BASE_URL="$BASE_URL" \
		SYSTEMCTL_LOG="$WORK/systemctl.log" PATH="$WORK/fakebin:$PATH" \
		bash -s -- "$@" <"$INSTALL_SCRIPT" 2>&1)"; then
		INSTALL_STATUS=0
	else
		INSTALL_STATUS=$?
	fi
}

# request_count NEEDLE - how many mirror requests contain NEEDLE.
request_count() {
	local count
	count="$(grep -cF -- "$1" "$MIRROR_LOG" 2>/dev/null || true)"
	printf '%s' "${count:-0}"
}

# sha_of PATH - the file's sha256, for "was it replaced?" assertions.
sha_of() { sha256sum "$1" | awk '{print $1}'; }

# version_of BINARY - the first line of `<binary> version`.
version_of() { "$1" version 2>/dev/null | head -n 1; }

# ---------------------------------------------------------------------------
# Scenarios
# ---------------------------------------------------------------------------

main() {
	mkdir -p "$MIRROR"
	publish "0.1.0" >/dev/null
	publish "0.2.0" >/dev/null
	# A release whose manifest is wrong, and one whose manifest does not mention
	# the archive at all: both must be refused without installing anything.
	publish_with_checksums "0.3.0" \
		"0000000000000000000000000000000000000000000000000000000000000000  $(asset_name 0.3.0)" >/dev/null
	publish_with_checksums "0.4.0" \
		"1111111111111111111111111111111111111111111111111111111111111111  ccresu_9.9.9_${OS}_${ARCH}.tar.gz" >/dev/null

	start_mirror

	# --- 1. pinned install -------------------------------------------------
	local home="$WORK/home-pinned"
	local prefix="$home/.local/bin"
	run_install "$home" --version 0.1.0
	assert_equal "$INSTALL_STATUS" 0 "a pinned install must succeed"$'\n'"$INSTALL_OUTPUT"
	assert_contains "$INSTALL_OUTPUT" "checksum:  ok" "the installer must report the verified checksum"
	assert_contains "$INSTALL_OUTPUT" "config:    valid" "the installed example configuration must validate"
	assert_exists "$prefix/ccresu" "the binary was not installed"
	assert_equal "$(version_of "$prefix/ccresu")" "ccresu 0.1.0" "the installed binary reports the wrong version"
	local config_file="$home/.config/ccresu/config.yaml"
	assert_exists "$config_file" "the installer did not create a configuration file"
	assert_equal "$(stat -c '%a' "$config_file")" "600" "the configuration file must be private"
	pass "pinned install lands the expected version and a validating config"

	# --- 2. URL shape ------------------------------------------------------
	local asset
	asset="$(asset_name 0.1.0)"
	assert_equal "$(request_count "/jymchng/ccresu/releases/download/v0.1.0/$asset")" "1" \
		"the archive must be fetched from the pinned tag"
	assert_equal "$(request_count "/jymchng/ccresu/releases/download/v0.1.0/checksums.txt")" "1" \
		"the checksums must be fetched from the pinned tag"
	assert_not_contains "$(cat "$MIRROR_LOG")" "/releases/latest/download/" \
		"downloads must never use the /releases/latest/download/ shape"
	assert_not_contains "$(cat "$MIRROR_LOG")" "/download/v0.2.0/" \
		"a pinned install must not touch another release"
	assert_equal "$(wc -l <"$MIRROR_LOG" | tr -d ' ')" "2" \
		"a pinned install should need exactly two requests"
	pass "download URLs use the pinned-tag shape"

	# --- 3. same version is a no-op ---------------------------------------
	local before_sha before_requests
	before_sha="$(sha_of "$prefix/ccresu")"
	before_requests="$(request_count "$asset")"
	run_install "$home" --version 0.1.0
	assert_equal "$INSTALL_STATUS" 0 "re-running the same version must succeed"$'\n'"$INSTALL_OUTPUT"
	assert_contains "$INSTALL_OUTPUT" "already at v0.1.0" "a repeat install must report that it did nothing"
	assert_equal "$(sha_of "$prefix/ccresu")" "$before_sha" "a repeat install rewrote the binary"
	assert_equal "$(request_count "$asset")" "$before_requests" "a repeat install re-downloaded the archive"
	pass "re-running the same version is a no-op"

	# --- 4. upgrade and --force -------------------------------------------
	run_install "$home" --version 0.2.0
	# run_install only sees the last invocation; re-run for the assertions below.
	assert_equal "$INSTALL_STATUS" 0 "an upgrade must succeed"$'\n'"$INSTALL_OUTPUT"
	assert_contains "$INSTALL_OUTPUT" "release:   v0.2.0 (pinned)" "the upgrade must report the new release"
	assert_equal "$(version_of "$prefix/ccresu")" "ccresu 0.2.0" "the upgrade did not replace the binary"
	[ "$(sha_of "$prefix/ccresu")" != "$before_sha" ] || fail "the upgrade left the old binary in place"
	# The configuration is kept, never overwritten, on an upgrade.
	assert_equal "$(stat -c '%a' "$config_file")" "600" "an upgrade changed the configuration permissions"
	assert_contains "$INSTALL_OUTPUT" "config:    $config_file (kept)" "an upgrade must keep the existing configuration"

	local asset2
	asset2="$(asset_name 0.2.0)"
	before_requests="$(request_count "$asset2")"
	run_install "$home" --version 0.2.0 --force
	assert_equal "$INSTALL_STATUS" 0 "--force must succeed"$'\n'"$INSTALL_OUTPUT"
	[ "$(request_count "$asset2")" -gt "$before_requests" ] || fail "--force did not re-download the archive"
	assert_equal "$(version_of "$prefix/ccresu")" "ccresu 0.2.0" "--force corrupted the installed binary"
	pass "an upgrade replaces the binary, keeps the config and --force re-downloads"

	# --- 5. tampered checksum ---------------------------------------------
	home="$WORK/home-bad-checksum"
	prefix="$home/.local/bin"
	run_install "$home" --version 0.3.0
	[ "$INSTALL_STATUS" -ne 0 ] || fail "a tampered checksum must make the install fail"$'\n'"$INSTALL_OUTPUT"
	assert_contains "$INSTALL_OUTPUT" "checksum mismatch" "the failure must name the checksum mismatch"
	assert_absent "$prefix/ccresu" "a tampered checksum must install nothing"
	assert_absent "$home/.config/ccresu/config.yaml" "a tampered checksum must create no configuration"
	pass "a tampered checksum aborts with nothing installed"

	# --- 6. checksums.txt missing the asset -------------------------------
	home="$WORK/home-missing-sum"
	prefix="$home/.local/bin"
	run_install "$home" --version 0.4.0
	[ "$INSTALL_STATUS" -ne 0 ] || fail "a manifest without the asset must fail"$'\n'"$INSTALL_OUTPUT"
	assert_contains "$INSTALL_OUTPUT" "no checksum entry" "the failure must explain the missing entry"
	assert_absent "$prefix/ccresu" "a missing checksum entry must install nothing"
	assert_absent "$home/.config/ccresu/config.yaml" "a missing checksum entry must create no configuration"
	pass "a checksums.txt without the asset aborts with nothing installed"

	# --- 7. dry run --------------------------------------------------------
	home="$WORK/home-dry-run"
	prefix="$home/.local/bin"
	local requests_before
	requests_before="$(wc -l <"$MIRROR_LOG" | tr -d ' ')"
	run_install "$home" --version 0.1.0 --dry-run
	assert_equal "$INSTALL_STATUS" 0 "a dry run must succeed"$'\n'"$INSTALL_OUTPUT"
	assert_contains "$INSTALL_OUTPUT" "Dry run complete" "a dry run must say what it did"
	assert_contains "$INSTALL_OUTPUT" "would run:" "a dry run must describe the actions it skipped"
	assert_absent "$prefix/ccresu" "a dry run installed a binary"
	assert_absent "$home/.config/ccresu/config.yaml" "a dry run created a configuration file"
	assert_absent "$home/.local/share/ccresu" "a dry run created a data directory"
	assert_equal "$(wc -l <"$MIRROR_LOG" | tr -d ' ')" "$requests_before" "a dry run still downloaded files"
	pass "--dry-run writes nothing and downloads nothing"

	# --- 8. usage errors ---------------------------------------------------
	home="$WORK/home-usage"
	prefix="$home/.local/bin"
	run_install "$home" --not-a-flag
	[ "$INSTALL_STATUS" -ne 0 ] || fail "an unknown argument must fail"
	assert_contains "$INSTALL_OUTPUT" "unknown argument" "an unknown argument must be reported"

	run_install "$home" --version
	[ "$INSTALL_STATUS" -ne 0 ] || fail "--version without a value must fail"
	assert_contains "$INSTALL_OUTPUT" "--version needs a value" "--version without a value must be reported"

	run_install "$home" --version 0.1.0 --prefix relative/path
	[ "$INSTALL_STATUS" -ne 0 ] || fail "a relative prefix must fail"
	assert_contains "$INSTALL_OUTPUT" "absolute path" "a relative prefix must be reported"
	assert_absent "$prefix/ccresu" "a usage error installed a binary"
	pass "argument errors are reported without installing anything"

	# --- 9. XDG base directories -------------------------------------------
	# The installer resolves its config and data directories from XDG_* when they
	# are set. Pin the behaviour: an exported XDG_CONFIG_HOME / XDG_DATA_HOME wins
	# over ~/.config and ~/.local/share, and the files it creates stay private.
	home="$WORK/home-xdg"
	prefix="$home/.local/bin"
	local xdg_config="$WORK/xdg/config" xdg_data="$WORK/xdg/data"
	run_install_with_xdg "$home" "$xdg_config" "$xdg_data" --version 0.1.0
	assert_equal "$INSTALL_STATUS" 0 "an install with XDG directories exported must succeed"$'\n'"$INSTALL_OUTPUT"
	assert_exists "$xdg_config/ccresu/config.yaml" "the installer must honour XDG_CONFIG_HOME"
	assert_absent "$home/.config/ccresu/config.yaml" \
		"the installer must not fall back to ~/.config when XDG_CONFIG_HOME is set"
	assert_equal "$(stat -c '%a' "$xdg_config/ccresu/config.yaml")" "600" \
		"an XDG configuration file must be private"
	assert_contains "$INSTALL_OUTPUT" "config:    $xdg_config/ccresu/config.yaml" \
		"the installer must report the XDG configuration path"
	assert_exists "$xdg_data/ccresu" "the installer must honour XDG_DATA_HOME"
	assert_absent "$home/.local/share/ccresu" \
		"the installer must not fall back to ~/.local/share when XDG_DATA_HOME is set"
	assert_equal "$(version_of "$prefix/ccresu")" "ccresu 0.1.0" \
		"the prefix must still come from HOME, not from XDG_DATA_HOME"
	pass "XDG_CONFIG_HOME and XDG_DATA_HOME are honoured over the HOME defaults"

	# --- 10. piped install with --systemd ----------------------------------
	# The README's one-liner pipes the script into bash. Run it from a directory
	# that is not a checkout: the archive still provides the binary and the
	# example config, and the systemd unit is fetched from the tag being
	# installed.
	publish_tag_files "0.1.0"
	home="$WORK/home-piped"
	prefix="$home/.local/bin"
	local tag_unit_requests
	tag_unit_requests="$(request_count "/jymchng/ccresu/raw/v0.1.0/deployments/systemd/ccresu.service")"
	run_install_piped "$home" --version 0.1.0 --systemd
	assert_equal "$INSTALL_STATUS" 0 "a piped install must succeed"$'\n'"$INSTALL_OUTPUT"
	assert_exists "$prefix/ccresu" "a piped install did not install the binary"
	assert_equal "$(version_of "$prefix/ccresu")" "ccresu 0.1.0" \
		"a piped install installed the wrong version"
	assert_exists "$home/.config/ccresu/config.yaml" "a piped install created no configuration"
	assert_contains "$INSTALL_OUTPUT" "config:    valid" \
		"a piped install must validate the configuration it wrote"
	assert_exists "$home/.config/systemd/user/ccresu.service" \
		"a piped install with --systemd did not install the unit"
	[ "$(request_count "/jymchng/ccresu/raw/v0.1.0/deployments/systemd/ccresu.service")" -gt "$tag_unit_requests" ] \
		|| fail "the systemd unit was not fetched from the release tag"
	assert_contains "$(cat "$WORK/systemctl.log")" "--user enable --now ccresu.service" \
		"the installer must enable the unit it installed"
	pass "a piped install works without a checkout, and --systemd reads the unit from the tag"

	printf '\n%d installer scenario checks passed\n' "$TESTS"
}

main "$@"
