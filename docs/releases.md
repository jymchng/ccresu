# Releases

A release is one git tag. Everything else — the six archives, the checksum file,
the notes — is produced by `.github/workflows/release.yml` from that tag.

## Versioning

`vMAJOR.MINOR.PATCH`, for example `v0.1.0`. The version baked into the binary is
the tag without its leading `v`.

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Tags are annotated, as every published tag is; the re-cut command below relies on
that too.

The workflow refuses a tag whose characters are not `[0-9A-Za-z.+-]`, so a typo
cannot become a release name. There is no pre-release channel and no nightly
build: a tag on the repository is the only thing that publishes.

## Where the tag goes

On a commit that is already on `main`: the **squash commit** of the pull request.
Changes reach `main` by squash merge, so each PR lands as a single commit there,
and `main` carries no merge commits at all:

```bash
git rev-list --merges main | wc -l   # 0
gh pr view 3 --json mergeCommit      # 15fceba…, the commit v0.10.0 points at
```

`gh pr view` is how to find that commit. Do not match on the `(#N)` suffix squash
merges add by default: the subject is whatever was typed when merging, and only
PR #3 carries `(#3)` — PRs #1 and #2 kept their own subjects.

Tag that commit, never the branch head: the head of the PR branch is a commit
that never reaches `main`, so a release cut from it would ship a tree no `main`
commit contains, and the version the binary reports would not be reachable from
the branch anyone installs from.

Nothing enforces the convention — the repository permits squash, merge-commit and
rebase merges alike — so keep it, and check you kept it with the binary itself.
Each build stamps the commit it was compiled from (`main.commit` is the runner's
`GITHUB_SHA`, which on a tag push is the tagged commit), so `ccresu --version`
has to agree with the tag:

```bash
git rev-parse v0.10.0^{commit}   # 15fceba57e1137c6149af60362e1257496fbc0d8
ccresu --version                 # commit:        15fceba57e1137c6149af60362e1257496fbc0d8
```

Ask for `^{commit}`: for an annotated tag `git rev-parse v0.10.0` prints the tag
object instead, which never matches the binary's `commit:` line.

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
- **The website republishes itself.** A published release triggers
  `.github/workflows/deploy-website.yml`, which builds the site with
  `CCRESU_RELEASE_TAG` set to that tag, so the install commands on the landing
  page and the installation page name the release you just published. You do not
  re-run anything by hand.

## The website follows the release, not the merge

The order is: merge the version bump, then tag it. Between those two moments the
repository says one thing and GitHub says another — `web/package.json` declares
the new version, and `releases/latest` still answers with the previous one — so
the deploy that the **merge** starts builds a site that names the old release.
That is how 0.10.0 shipped a page telling readers to install 0.9.2 until someone
happened to look at the live site; the deploy was green, because nothing was
checking.

Two mechanisms make that visible instead of silent, and neither needs a decision
from you:

| Trigger | Version it names | What it means |
| --- | --- | --- |
| push to `main` | `releases/latest` | If the commit declares a version that is not published yet, the **version guard** fails the run and nothing is published. |
| `release: published` | the tag that fired it | The site is rebuilt from the tagged commit and names that release. This is the run that puts the new version live. |

So on a release you should expect **two** "Deploy website" runs: a red one on the
merge commit, and a green one on the tag a few minutes later. The red one is not
a broken site — it is the guard refusing to advertise a release that did not
exist yet, and `scripts/check-site-version.mjs` is what fails it. The site keeps
serving the last published version in the meantime, so the gap costs nothing but
a red status; it closes as soon as the tag is pushed.

To confirm what is actually deployed:

```bash
curl -sS https://jymchng.github.io/ccresu/ | grep -o 'ccresu_[0-9.]*_linux_amd64'
ccresu --version    # the binary reports its own version and commit
```

A re-cut re-publishes the release, which fires the deploy again for the same tag,
so the site is rebuilt without anyone remembering to re-run it.

## Re-cutting a version

A version that shipped with a defect can be re-cut instead of followed by a patch
release. Move the tag and push it: the pipeline replaces the release.

```bash
git tag -f -a v0.7.0 -m "v0.7.0" <commit>
git push --force origin v0.7.0
```

The publish step deletes any release already published for that tag before it
creates the new one, so the archives and `checksums.txt` are the ones built from
the new commit and the download URLs do not change. `0.7.0` was re-cut this way:
it shipped without the sessions-browser fix, and the fix belongs in the release
an operator is told to install rather than in a patch they have to know about.

What a re-cut cannot do is recall anything already downloaded. That is why it is
for a version that has not spread — otherwise the honest answer is a patch
version, which leaves the old archives where they are.

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
