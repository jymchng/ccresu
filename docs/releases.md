# Releases

A release is one git tag. Everything else — the six archives, the checksum file,
the notes — is produced by `.github/workflows/release.yml` from that tag.

## Versioning

`vMAJOR.MINOR.PATCH`, for example `v0.1.0`. The version baked into the binary is
the tag without its leading `v`.

```bash
git tag v0.1.0
git push origin v0.1.0
```

The workflow refuses a tag whose characters are not `[0-9A-Za-z.+-]`, so a typo
cannot become a release name. There is no pre-release channel and no nightly
build: a tag on the repository is the only thing that publishes.

## What a release contains

Six archives, one per supported target:

```text
ccresu_<version>_linux_amd64.tar.gz
ccresu_<version>_linux_arm64.tar.gz
ccresu_<version>_darwin_amd64.tar.gz
ccresu_<version>_darwin_arm64.tar.gz
ccresu_<version>_windows_amd64.zip
ccresu_<version>_windows_arm64.zip
checksums.txt
```

Each archive holds the binary at the top level (`ccresu`, or `ccresu.exe` on
Windows) plus `configs/config.example.yaml` — the installer copies that into
`~/.config/ccresu/config.yaml` when you have no configuration yet.

`checksums.txt` lists a sha256 for every archive, using **bare file names** with
no directory prefix:

```text
3f1c...  ccresu_0.1.0_darwin_amd64.tar.gz
...

```

Three things depend on that exact shape, and all three are tested or checked in
CI: `scripts/install.sh` matches the asset name in this file, `web/lib/site.ts`
links to it, and `web/tests/install-commands.test.tsx` asserts the names.

## The pipeline

```text
gate  →  build (3 OS × 2 arch, parallel)  →  publish
```

1. **gate** — `go vet`, `go test`, `make cover-check`. Nothing is built if the
   suite or the 80% coverage gate fails.
2. **build** — one job per target on one Ubuntu runner, because pure-Go SQLite
   means `CGO_ENABLED=0` and true cross-compilation. Each build sets
   `-trimpath`, `-buildvcs=false` and
   `-ldflags "-s -w -X main.version=… -X main.commit=… -X main.buildDate=…"`,
   then stages the binary with the example config and archives it.
3. **smoke test** — the linux/amd64 binary must print its version, answer
   `--help`, and report the tag's version.
4. **publish** — collects the six artifacts, fails if the count is not exactly
   six, writes `checksums.txt` with `sha256sum`, and creates the GitHub release
   with `gh release create --verify-tag --generate-notes`.

Permissions are least-privilege: the workflow default is `contents: read`, and
only the `publish` job holds `contents: write`. Checkout uses
`persist-credentials: false` in every job.

## Verification order, and why it matters

The installer verifies the archive against `checksums.txt` **before** anything is
extracted or executed. A corrupted download, a truncated archive, or a
`checksums.txt` that does not mention your asset is a fatal error, not a warning.
That ordering is the reason a release can be downloaded over a hostile network
without handing it code execution.

```bash
./scripts/install.sh --dry-run --version 0.1.0   # prints URLs and actions only
./scripts/install.sh --version 0.1.0             # pinned install
./scripts/install.sh                             # newest release
```

Download URLs are always the pinned-tag form,
`https://github.com/jymchng/ccresu/releases/download/v<version>/<asset>`. The
`releases/latest/download/<tag>/<asset>` form is a 404, which is why
`CCRESU_BASE_URL` exists for mirrors and local stand-ins:

```bash
CCRESU_BASE_URL=http://127.0.0.1:8000 ./scripts/install.sh --version 0.1.0
```

## After publishing

- **The release is the contract.** If the asset names or the checksums format
  change, `scripts/install.sh`, `web/lib/site.ts` and the website's
  install-commands test must change in the same commit.
- **Upgrades are re-runs.** `install.sh` is idempotent: the same version is a
  no-op, a newer one replaces the binary in place. See [upgrade.md](upgrade.md).
- **Nothing migrates by hand.** The database schema is applied on open, so an
  upgraded binary adopts an older database automatically. A downgrade cannot read
  a newer schema; back up `~/.local/share/ccresu/ccresu.db` first.
- **The service restarts you.** `systemctl --user restart ccresu.service` after
  an upgrade; an in-flight wait is a row in the database, so nothing is lost.

## Building a release by hand

There is no hidden step; the workflow is the recipe:

```bash
VERSION=0.1.0
COMMIT=$(git rev-parse HEAD)
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE" \
  -o staging/ccresu ./cmd/ccresu

mkdir -p staging/configs
cp configs/config.example.yaml staging/configs/

tar -czf ccresu_${VERSION}_linux_amd64.tar.gz -C staging ccresu configs
sha256sum -- ccresu_${VERSION}_linux_amd64.tar.gz
```

`./ccresu version` reports the injected values:

```console
$ ccresu version
ccresu 0.1.0
commit:        <sha>
built:         2026-01-01T00:00:00Z
go:            go1.26.x
platform:      linux/amd64
```
