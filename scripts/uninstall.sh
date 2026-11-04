#!/usr/bin/env bash
#
# ccresu uninstaller.
#
# Removes the installed binary and, when asked, the user systemd unit.
# Your configuration and recovery history are kept unless --purge is given,
# because the database holds the record of what ccresu did on your behalf.
#
# Usage:
#   ./scripts/uninstall.sh            # remove the binary and stop the service
#   ./scripts/uninstall.sh --purge    # also delete config, data and state
#   ./scripts/uninstall.sh --dry-run  # show what would be removed
set -euo pipefail

readonly PROGRAM="ccresu"

log() { printf '%s\n' "$*"; }
warn() { printf '%s\n' "warning: $*" >&2; }
die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Remove an installed ccresu.

Usage: uninstall.sh [OPTIONS]

Options:
  --prefix DIR    Where the binary was installed.
                  Default: $CCRESU_PREFIX, then $HOME/.local/bin
  --purge         Also delete the configuration file, database and state.
  --keep-service  Leave the systemd user unit installed (do not disable it).
  --dry-run       Print the actions without changing anything.
  -h, --help      Show this help.

Environment:
  CCRESU_PREFIX   Same as --prefix.
  CCRESU_ALLOW_ROOT  Set to 1 to run as root anyway (not recommended).
EOF
}

prefix="${CCRESU_PREFIX:-$HOME/.local/bin}"
dry_run=0
purge=0
keep_service=0

while [ "$#" -gt 0 ]; do
	case "$1" in
	--prefix)
		[ "$#" -ge 2 ] || die "--prefix needs a value"
		prefix="$2"
		shift 2
		;;
	--prefix=*)
		prefix="${1#*=}"
		shift
		;;
	--purge)
		purge=1
		shift
		;;
	--keep-service)
		keep_service=1
		shift
		;;
	--dry-run)
		dry_run=1
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

[ -n "${HOME:-}" ] || die "HOME is not set"
case "$prefix" in
/*) ;;
*) die "--prefix must be an absolute path, got: $prefix" ;;
esac

if [ "$(id -u)" -eq 0 ] && [ "${CCRESU_ALLOW_ROOT:-0}" != "1" ]; then
	die "refusing to run as root; run this as the user ccresu was installed for"
fi

dry_suffix=""
if [ "$dry_run" -eq 1 ]; then
	dry_suffix=" (dry-run)"
fi

# run CMD... executes the command, or prints it when --dry-run is active.
run() {
	if [ "$dry_run" -eq 1 ]; then
		printf '  would run: %s\n' "$*"
		return 0
	fi
	"$@"
}

log "ccresu uninstaller"
if [ "$dry_run" -eq 1 ]; then
	log "  mode:      dry-run (nothing will be changed)"
fi

# ---------------------------------------------------------------------------
# Stop and remove the user service
# ---------------------------------------------------------------------------

unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
unit_file="$unit_dir/$PROGRAM.service"

log ""
log "Service"
if [ ! -f "$unit_file" ]; then
	log "  no user unit installed"
elif [ "$keep_service" -eq 1 ]; then
	log "  keeping $unit_file (--keep-service)"
elif ! command -v systemctl >/dev/null 2>&1; then
	warn "systemctl not found; removing $unit_file without stopping it"
	run rm -f "$unit_file"
	log "  removed $unit_file$dry_suffix"
else
	run systemctl --user disable --now "$PROGRAM.service"
	run rm -f "$unit_file"
	run systemctl --user daemon-reload
	log "  stopped and removed $unit_file$dry_suffix"
fi

# ---------------------------------------------------------------------------
# Remove the binary
# ---------------------------------------------------------------------------

target="$prefix/$PROGRAM"
log ""
log "Binary"
if [ -e "$target" ]; then
	run rm -f "$target"
	log "  removed $target$dry_suffix"
else
	log "  no binary at $target"
fi

# ---------------------------------------------------------------------------
# Configuration and state
# ---------------------------------------------------------------------------

config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/$PROGRAM"
data_dir="${XDG_DATA_HOME:-$HOME/.local/share}/$PROGRAM"
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/$PROGRAM"

log ""
log "Configuration and data"
if [ "$purge" -eq 1 ]; then
	for dir in "$config_dir" "$data_dir" "$state_dir"; do
		if [ -d "$dir" ]; then
			run rm -rf "$dir"
			log "  purged $dir$dry_suffix"
		fi
	done
else
	log "  kept (use --purge to delete):"
	log "    $config_dir"
	log "    $data_dir"
	log "    $state_dir"
fi

log ""
if [ "$dry_run" -eq 1 ]; then
	log "Dry run complete; nothing was changed."
else
	log "Done."
fi
