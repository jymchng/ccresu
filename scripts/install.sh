#!/usr/bin/env bash
#
# ccresu installer.
#
# Downloads a released ccresu binary for this OS/architecture, verifies it
# against the release checksums *before* anything is extracted or executed,
# installs it into ~/.local/bin and prepares the config and data directories.
#
# ccresu resumes Claude Code sessions that belong to you, so it is installed
# per user and refuses to run as root.
#
# Usage:
#   ./scripts/install.sh                 # latest release, system-matched
#   ./scripts/install.sh --version 0.1.0 # pin a release
#   ./scripts/install.sh --dry-run       # show what would happen, change nothing
#   ./scripts/install.sh --systemd       # also install the user systemd unit
#
# It runs the same whether it is executed from a checkout or piped from the
# repository, which is the documented one-liner:
#
#   curl -fsSL https://raw.githubusercontent.com/jymchng/ccresu/main/scripts/install.sh | bash
#
# Everything the script needs comes from the release it resolves, so there is no
# checkout to rely on: the example configuration ships inside the release
# archive and the systemd unit is fetched from the same tag.
#
# Re-running the script is the supported upgrade path; see docs/upgrade.md.
set -euo pipefail

readonly PROGRAM="ccresu"
readonly REPO="jymchng/ccresu"
readonly DEFAULT_BASE_URL="https://github.com"

log() { printf '%s\n' "$*"; }
warn() { printf '%s\n' "warning: $*" >&2; }
die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Install or upgrade ccresu from GitHub releases.

Usage: install.sh [OPTIONS]

Options:
  --version VER   Install a specific release (e.g. 0.1.0 or v0.1.0).
                  Default: the newest published release.
  --prefix DIR    Directory to install the binary into.
                  Default: $HOME/.local/bin
  --systemd       Also install and enable the user-level systemd unit.
  --dry-run       Print the actions without downloading or changing anything.
  --force         Reinstall even when the same version is already present.
  -h, --help      Show this help.

Environment:
  CCRESU_VERSION      Same as --version.
  CCRESU_PREFIX       Same as --prefix.
  CCRESU_BASE_URL     Base URL for downloads (default https://github.com).
                      Useful for a mirror or an air-gapped test.
  CCRESU_ALLOW_ROOT   Set to 1 to install as root anyway (not recommended).
EOF
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

version="${CCRESU_VERSION:-}"
prefix="${CCRESU_PREFIX:-$HOME/.local/bin}"
base_url="${CCRESU_BASE_URL:-$DEFAULT_BASE_URL}"
dry_run=0
with_systemd=0
force=0

while [ "$#" -gt 0 ]; do
	case "$1" in
	--version)
		[ "$#" -ge 2 ] || die "--version needs a value"
		version="$2"
		shift 2
		;;
	--version=*)
		version="${1#*=}"
		shift
		;;
	--prefix)
		[ "$#" -ge 2 ] || die "--prefix needs a value"
		prefix="$2"
		shift 2
		;;
	--prefix=*)
		prefix="${1#*=}"
		shift
		;;
	--systemd)
		with_systemd=1
		shift
		;;
	--dry-run)
		dry_run=1
		shift
		;;
	--force)
		force=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown argument: $1 (try --help)"
		;;
	esac
done

[ -n "$prefix" ] || die "--prefix must not be empty"
case "$prefix" in
/*) ;;
*) die "--prefix must be an absolute path, got: $prefix" ;;
esac

# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------

if [ "$(id -u)" -eq 0 ] && [ "${CCRESU_ALLOW_ROOT:-0}" != "1" ]; then
	die "refusing to install as root; run this as the user who runs Claude Code (override with CCRESU_ALLOW_ROOT=1)"
fi

[ -n "${HOME:-}" ] || die "HOME is not set"

# run CMD... executes the command, or prints it when --dry-run is active.
run() {
	if [ "$dry_run" -eq 1 ]; then
		printf '  would run: %s\n' "$*"
		return 0
	fi
	"$@"
}

need_command() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

fetch_stdout() {
	# fetch_stdout URL - print the body to stdout.
	local url="$1"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --proto '=https,=http' "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O - "$url"
	else
		die "neither curl nor wget is installed; cannot download"
	fi
}

fetch_file() {
	# fetch_file URL DEST
	local url="$1" dest="$2" status=0
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --proto '=https,=http' -o "$dest" "$url" || status=$?
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$dest" "$url" || status=$?
	else
		die "neither curl nor wget is installed; cannot download"
	fi
	if [ "$status" -ne 0 ]; then
		die "download failed for $url (tool status $status); the release or asset may not exist"
	fi
}

# try_fetch_repo_file PATH DEST - fetch PATH from the repository at the resolved
# release tag, reporting failure instead of exiting.
#
# A piped installer has no checkout next to it, so a file that lives in the
# repository but not in the release archive can only be read at the tag. The tag
# is the right coordinate because it is the release the user is installing, not
# whatever main happens to hold later.
try_fetch_repo_file() {
	local path="$1" dest="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --proto '=https,=http' -o "$dest" "$base_url/$REPO/raw/v$version/$path" 2>/dev/null
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$dest" "$base_url/$REPO/raw/v$version/$path" 2>/dev/null
	else
		return 1
	fi
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "neither sha256sum nor shasum is installed; cannot verify the download"
	fi
}

# ---------------------------------------------------------------------------
# Platform detection
# ---------------------------------------------------------------------------

detect_os() {
	case "$(uname -s)" in
	Linux) printf 'linux' ;;
	Darwin) printf 'darwin' ;;
	*)
		die "unsupported operating system: $(uname -s). Releases cover linux and darwin (windows: download the .zip from https://github.com/$REPO/releases)"
		;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) printf 'amd64' ;;
	arm64 | aarch64) printf 'arm64' ;;
	*)
		die "unsupported architecture: $(uname -m). Releases cover amd64 and arm64"
		;;
	esac
}

os="$(detect_os)"
arch="$(detect_arch)"

# ---------------------------------------------------------------------------
# Version resolution
# ---------------------------------------------------------------------------

normalise_version() {
	# Accept "0.1.0" or "v0.1.0" and print the bare version.
	local raw="$1"
	raw="${raw#v}"
	[ -n "$raw" ] || die "empty version"
	case "$raw" in
	*[!0-9A-Za-z.+-]*) die "suspicious version string: $raw" ;;
	esac
	printf '%s' "$raw"
}

resolve_latest_version() {
	# GitHub answers /releases/latest with a redirect to /releases/tag/<tag>.
	# base_url is honoured so a mirror (or the test suite) can answer instead.
	local effective tag
	if command -v curl >/dev/null 2>&1; then
		effective="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$base_url/$REPO/releases/latest" 2>/dev/null || true)"
	else
		effective="$(wget --spider -S --max-redirect=0 "$base_url/$REPO/releases/latest" 2>&1 | awk '/^ *Location:/{print $2}' | tail -n 1 || true)"
	fi
	tag="${effective##*/}"
	if [ -z "$tag" ] || [ "$tag" = "latest" ]; then
		die "could not determine the latest release; pass --version VER"
	fi
	normalise_version "$tag"
}

if [ -n "$version" ]; then
	version="$(normalise_version "$version")"
	pinned=1
else
	pinned=0
	if [ "$dry_run" -eq 1 ]; then
		if ! version="$(resolve_latest_version 2>/dev/null)"; then
			version="LATEST"
		fi
	else
		version="$(resolve_latest_version)"
	fi
fi

asset="${PROGRAM}_${version}_${os}_${arch}.tar.gz"
checksums_asset="checksums.txt"
# Always download from the tag resolved above, never from
# /releases/latest/download: the asset name embeds the version, so pulling it
# from the tag keeps the bytes and the reported version in lockstep and relies
# on one redirect fewer. (GitHub also does not serve
# /releases/latest/download/<tag>/<asset> at all - that shape is a 404.)
download_base="$base_url/$REPO/releases/download/v$version"
asset_url="$download_base/$asset"
checksums_url="$download_base/$checksums_asset"

log "ccresu installer"
log "  platform:  $os/$arch"
if [ "$pinned" -eq 1 ]; then
	log "  release:   v$version (pinned)"
else
	log "  release:   v$version (latest)"
fi
log "  asset:     $asset"
log "  prefix:    $prefix"
[ "$dry_run" -eq 1 ] && log "  mode:      dry-run (nothing will be changed)"

# ---------------------------------------------------------------------------
# Already-installed check
# ---------------------------------------------------------------------------

target="$prefix/$PROGRAM"
if [ -x "$target" ] && [ "$force" -eq 0 ] && [ "$dry_run" -eq 0 ]; then
	if current="$("$target" version 2>/dev/null | head -n 1)"; then
		case "$current" in
		*" $version")
			log ""
			log "$target is already at v$version; nothing to do (use --force to reinstall)"
			exit 0
			;;
		esac
	fi
fi

# ---------------------------------------------------------------------------
# Download and verify
# ---------------------------------------------------------------------------

workdir="$(mktemp -d "${TMPDIR:-/tmp}/ccresu-install.XXXXXX")"
cleanup() { rm -rf "$workdir"; }
trap cleanup EXIT INT TERM

log ""
log "Downloading v$version"
log "  $asset_url"
log "  $checksums_url"
if [ "$dry_run" -eq 1 ]; then
	log "  would run: curl/wget into $workdir"
else
	fetch_file "$checksums_url" "$workdir/$checksums_asset"
	fetch_file "$asset_url" "$workdir/$asset"

	expected="$(awk -v want="$asset" '$2 == want || $2 == "*" want {print $1; exit}' "$workdir/$checksums_asset")"
	[ -n "$expected" ] || die "no checksum entry for $asset in $checksums_asset; refusing to install an unverified binary"
	actual="$(sha256_of "$workdir/$asset")"
	if [ "$expected" != "$actual" ]; then
		die "checksum mismatch for $asset (expected $expected, got $actual); the download was rejected"
	fi
	log "  checksum:  ok ($expected)"
fi

# ---------------------------------------------------------------------------
# Extract
# ---------------------------------------------------------------------------

staging="$workdir/staging"
if [ "$dry_run" -eq 1 ]; then
	log "  would run: tar -xzf $asset into $staging"
else
	need_command tar
	mkdir -p "$staging"
	tar -xzf "$workdir/$asset" -C "$staging"
	binary="$(find "$staging" -type f -name "$PROGRAM" -perm -u+x | head -n 1)"
	if [ -z "$binary" ]; then
		binary="$(find "$staging" -type f -name "$PROGRAM" | head -n 1)"
	fi
	[ -n "$binary" ] || die "the archive $asset does not contain a $PROGRAM binary"
fi

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------

log ""
log "Installing"
run mkdir -p "$prefix"
if [ "$dry_run" -eq 1 ]; then
	log "  would run: install -m 0755 <extracted $PROGRAM> $target"
else
	install -m 0755 "$binary" "$target"
fi

config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/$PROGRAM"
data_dir="${XDG_DATA_HOME:-$HOME/.local/share}/$PROGRAM"
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/$PROGRAM"
config_file="$config_dir/config.yaml"

for dir in "$config_dir" "$data_dir" "$state_dir"; do
	run mkdir -p "$dir"
	run chmod 0700 "$dir"
done

# Example configuration: prefer the copy shipped with the release archive or
# next to this script (a git checkout), fall back to a minimal starter file.
script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
example=""
for candidate in "$workdir/staging/configs/config.example.yaml" "$script_dir/../configs/config.example.yaml"; do
	if [ -f "$candidate" ]; then
		example="$candidate"
		break
	fi
done

if [ -f "$config_file" ]; then
	log "  config:    $config_file (kept)"
else
	if [ "$dry_run" -eq 1 ]; then
		log "  would install the example config to $config_file"
	else
		if [ -n "$example" ]; then
			install -m 0600 "$example" "$config_file"
		else
			cat >"$config_file" <<'YAML'
# ccresu configuration. Every field is documented in
# https://github.com/jymchng/ccresu/blob/main/docs/configuration.md
# Any field can be overridden with a CCRESU_ environment variable, e.g.
#   CCRESU_TELEGRAM__BOT_TOKEN=123:abc
app:
  log_level: info
  log_format: json

recovery:
  enabled: true
  max_attempts: 10

telegram:
  enabled: false
  bot_token: ""
  chat_id: ""

server:
  enabled: true
  host: 127.0.0.1
  port: 8787

storage:
  driver: sqlite
  path: ~/.local/share/ccresu/ccresu.db
YAML
			chmod 0600 "$config_file"
		fi
		log "  config:    $config_file (new)"
	fi
fi
log "  data:      $data_dir"
log "  state:     $state_dir"

# ---------------------------------------------------------------------------
# Optional systemd unit
# ---------------------------------------------------------------------------

unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
unit_file="$unit_dir/$PROGRAM.service"
unit_template="$script_dir/../deployments/systemd/$PROGRAM.service"

if [ "$with_systemd" -eq 1 ]; then
	log ""
	log "Systemd (user unit)"
	if [ ! -f "$unit_template" ] && [ "$dry_run" -eq 0 ]; then
		# Piped from the repository: the unit lives in the repo but not in the
		# release archive, so read it at the tag being installed.
		if try_fetch_repo_file "deployments/systemd/$PROGRAM.service" "$workdir/$PROGRAM.service"; then
			unit_template="$workdir/$PROGRAM.service"
		fi
	fi
	if [ ! -f "$unit_template" ]; then
		warn "unit template not found at $unit_template; skipping systemd setup"
	elif ! command -v systemctl >/dev/null 2>&1; then
		warn "systemctl not found; skipping systemd setup"
	else
		run mkdir -p "$unit_dir"
		if [ "$dry_run" -eq 1 ]; then
			log "  would install $unit_file (ExecStart=$target daemon)"
			log "  would run: systemctl --user daemon-reload && systemctl --user enable --now $PROGRAM.service"
		else
			# Keep the shipped template's hardening, but point ExecStart at the
			# prefix actually used.
			sed "s|%h/.local/bin/$PROGRAM|$target|" "$unit_template" >"$unit_file"
			chmod 0644 "$unit_file"
			log "  unit:      $unit_file"
			# Starting the unit is best effort: a container or a login session
			# without a user manager must not turn a successful install into a
			# failed one.
			if systemctl --user daemon-reload && systemctl --user enable --now "$PROGRAM.service"; then
				log "  service:   enabled and started"
				if ! loginctl show-user "$USER" 2>/dev/null | grep -q '^Linger=yes'; then
					warn "the user manager stops at logout; enable lingering with: sudo loginctl enable-linger $USER"
				fi
			else
				warn "the unit is installed at $unit_file but could not be started here"
				warn "start it from your session with: systemctl --user enable --now $PROGRAM.service"
			fi
		fi
	fi
fi

# ---------------------------------------------------------------------------
# Post-install verification
# ---------------------------------------------------------------------------

log ""
if [ "$dry_run" -eq 1 ]; then
	log "Would verify: $target version && $target config validate $config_file"
	log ""
	log "Dry run complete; nothing was changed."
	exit 0
fi

log "Verifying"
installed_version="$("$target" version | head -n 1)" || die "the installed binary cannot run: $target version failed"
log "  binary:    $installed_version"
"$target" config validate "$config_file" >/dev/null || die "the installed configuration is invalid: $config_file"
log "  config:    valid"

case ":$PATH:" in
*":$prefix:"*) ;;
*)
	log ""
	warn "$prefix is not on your PATH; add it with:"
	log "  echo 'export PATH=\"$prefix:\$PATH\"' >> ~/.profile"
	;;
esac

log ""
log "Done. Next steps:"
log "  1. edit $config_file"
log "  2. $PROGRAM doctor"
if [ "$with_systemd" -eq 1 ]; then
	log "  3. systemctl --user status $PROGRAM.service"
else
	log "  3. $PROGRAM daemon   # or rerun with --systemd for a user service"
fi
