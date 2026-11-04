# Installing, upgrading and uninstalling ccresu

ccresu is installed **per user**, not system-wide: it resumes the Claude Code
sessions that belong to you, reads `~/.claude`, and writes into your project
directories. Installing it as root would give a root-owned process access to
your sessions and your database, so both scripts refuse to run as root.

Two paths exist: release binaries (the normal case) and `go install` (for
contributors and unusual platforms).

---

## Install from a release

```bash
curl -fsSL https://raw.githubusercontent.com/jymchng/ccresu/main/scripts/install.sh | bash
```

Or from a checkout:

```bash
./scripts/install.sh
```

The installer:

1. detects your OS and architecture (`linux`/`darwin` × `amd64`/`arm64`),
2. resolves the newest release tag — or uses the one you pin,
3. downloads `ccresu_<version>_<os>_<arch>.tar.gz` **and** `checksums.txt`,
4. verifies the sha256 of the archive and aborts if it does not match,
5. extracts and installs the binary into `~/.local/bin` (mode `0755`),
6. creates `~/.config/ccresu`, `~/.local/share/ccresu` and `~/.local/state/ccresu` (mode `0700`),
7. installs `configs/config.example.yaml` as `~/.config/ccresu/config.yaml` (mode `0600`) if you have no configuration yet (`ccresu config init` does the same from the binary alone),
8. optionally installs and starts the user-level systemd unit,
9. verifies the result by running `ccresu version` and `ccresu config validate`.

Nothing is extracted or executed before the checksum matches: a corrupted or
substituted archive is rejected outright, and a `checksums.txt` that does not
list your asset is treated as a failure rather than a warning.

### Useful flags

| Flag | Meaning |
| --- | --- |
| `--version 0.1.0` | Install a specific release instead of the newest one. |
| `--prefix DIR` | Install the binary somewhere other than `~/.local/bin`. Must be absolute. |
| `--systemd` | Also install and enable `~/.config/systemd/user/ccresu.service`. |
| `--dry-run` | Print every action (including the resolved version and download URLs) and change nothing. |
| `--force` | Reinstall even if the installed binary already reports that version. |

Environment equivalents: `CCRESU_VERSION`, `CCRESU_PREFIX`, and
`CCRESU_BASE_URL` (point the downloads at a mirror or a local stand-in;
`CCRESU_ALLOW_ROOT=1` overrides the root refusal, which you should not need).

### Check before you commit

```bash
bash install.sh --dry-run --version 0.1.0
```

The dry run performs platform detection and version resolution, prints the
exact URLs it would fetch and every file it would create, and touches nothing.

---

## Install with Go

```bash
CGO_ENABLED=0 go install github.com/jymchng/ccresu/cmd/ccresu@latest
```

`CGO_ENABLED=0` is required so the SQLite driver stays pure Go. The binary lands
in `$(go env GOPATH)/bin`, which is often not on `PATH`.

`make build` in a checkout produces `./ccresu` with version, commit and build
date injected through `-ldflags`.

---

## Upgrade

Upgrading is the same command as installing, because the installer is
idempotent:

```bash
bash install.sh            # newest release
bash install.sh --version 0.2.0   # a specific release
```

What happens to your state:

- The binary at `~/.local/bin/ccresu` is replaced atomically by `install`.
- `~/.config/ccresu/config.yaml` is **kept**. New releases gain options; the
  defaults apply to anything your file does not set. Compare against
  `configs/config.example.yaml` after a major version bump to pick up new keys.
- The database at `~/.local/share/ccresu/ccresu.db` is kept and migrated
  forward on first start. Migrations run before the daemon begins work, so do
  not downgrade: an older binary cannot read a newer schema.

If you installed the systemd unit, the installer reloads it and restarts the
service, so an in-flight wait resumes from the database rather than from
memory — schedules are persisted, which is why a restart mid-wait is safe.

Verify the upgrade:

```bash
~/.local/bin/ccresu version
~/.local/bin/ccresu config validate
~/.local/bin/ccresu doctor
```

### Rolling back

Releases are pinned by tag, so rolling back is a normal install of the previous
version:

```bash
bash install.sh --version 0.1.4 --force
```

Keep a copy of the database before rolling back across a schema migration:

```bash
cp ~/.local/share/ccresu/ccresu.db ~/.local/share/ccresu/ccresu.db.bak
```

---

## Run as a user service

```bash
bash install.sh --systemd
```

or by hand:

```bash
mkdir -p ~/.config/systemd/user
cp deployments/systemd/ccresu.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now ccresu.service
systemctl --user status ccresu.service
journalctl --user -u ccresu.service -f
```

`%h/.local/bin/ccresu` in the unit is rewritten by the installer when you
install to a different `--prefix`.

A user manager stops when your last session ends. For a watchdog that keeps
working after logout:

```bash
sudo loginctl enable-linger "$USER"
```

---

## Uninstall

```bash
bash uninstall.sh          # stop the service, remove the binary
bash uninstall.sh --purge  # also delete config, database and state
```

By default the configuration, the database and the state directory are kept:
the database is the record of what ccresu did on your behalf, and deleting it
silently would destroy your recovery history. `--purge` removes
`~/.config/ccresu`, `~/.local/share/ccresu` and `~/.local/state/ccresu`.

`--keep-service` leaves the systemd unit in place (useful when you are only
changing the binary), and `--dry-run` prints the plan without doing anything.

---

## Windows and other platforms

Releases also ship `ccresu_<version>_windows_<arch>.zip`. The shell installer
covers linux and darwin only; on Windows, unpack the zip and put
`ccresu.exe` somewhere on `PATH` yourself. Any other OS or architecture is
rejected with an explicit error instead of installing something that cannot
run.
