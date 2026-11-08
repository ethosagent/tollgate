# Release

Three modules release with their own tags (ADR 0010, 0011, 0017):

| Module | Tag | What a tag publishes |
|---|---|---|
| root `github.com/ethosagent/tollgate` | `vX.Y.Z` | the library |
| `cmd/tollgated` | `cmd/tollgated/vX.Y.Z` | `go install …/cmd/tollgated@vX.Y.Z` and, via `release.yml`, the binaries |
| `ext/metrics/postgres` | `ext/metrics/postgres/vX.Y.Z` | the Postgres sink library |

Rules the order follows:

- A nested module requires a **published** root tag and has **no** `replace`.
  `go install` refuses a module with a replace; library users ignore it.
- Local dev and CI build nested modules against the checkout through the
  committed `go.work`, so the bump never changes what the gate tests.
- `scripts/check-release.sh <tag>` is the guard. Run it before every tag;
  `release.yml` runs it again. A tag that is pushed is final: the Go proxy
  caches it. Never move or delete a pushed tag; release a new patch.

Use `GOTOOLCHAIN=go1.27.3` (or newer) for every command if your local Go has
known stdlib vulnerabilities; the vuln gate fails otherwise. Your git config
may force annotated tags, so the commands use `git tag -a`.

## v0.1.0, the first release

### 0. Land the release tooling

Merge the `ci-release` branch (go.work, ADR 0017, `release.yml`, this runbook)
into `main` with a green CI. The nested modules still carry the bootstrap
`replace` and `require … v0.0.0`: no root tag exists yet.

### 1. Tag the root

```sh
git switch main && git pull --ff-only
scripts/run-checks.sh                       # ✓ all gates passed
scripts/check-release.sh v0.1.0             # ✓ v0.1.0 is releasable
git tag -a v0.1.0 -m "tollgate v0.1.0"
git push origin v0.1.0
# the proxy fetches the tag on first request; this also proves it is visible
(cd "$(mktemp -d)" && go list -m github.com/ethosagent/tollgate@v0.1.0)
```

### 2. Bump the nested modules (one commit, via PR)

```sh
git switch -c release/nested-v0.1.0
for m in cmd/tollgated ext/metrics/postgres; do
  (cd "$m" &&
    go mod edit -dropreplace=github.com/ethosagent/tollgate \
      -require=github.com/ethosagent/tollgate@v0.1.0 &&
    go mod tidy)                            # tidy ignores go.work: records v0.1.0 in go.sum
done
git diff                                    # go.mod: require v0.1.0, replace + its comment gone; go.sum: +2 lines each
scripts/check-release.sh cmd/tollgated/v0.1.0
scripts/check-release.sh ext/metrics/postgres/v0.1.0
scripts/run-checks.sh
git commit -am "build: require tollgate v0.1.0 in nested modules; drop replace"
git push -u origin release/nested-v0.1.0     # open a PR, wait for green CI, merge
```

Both guards must print `✓ … is releasable`. If one says the module "does not
build with GOWORK=off", it uses root code that is newer than `v0.1.0`: tag a
new root patch first (step 1), then bump to it.

### 3. Tag the nested modules

On the merged bump commit:

```sh
git switch main && git pull --ff-only
scripts/check-release.sh ext/metrics/postgres/v0.1.0
scripts/check-release.sh cmd/tollgated/v0.1.0
git tag -a ext/metrics/postgres/v0.1.0 -m "ext/metrics/postgres v0.1.0"
git tag -a cmd/tollgated/v0.1.0 -m "tollgated v0.1.0"
git push origin ext/metrics/postgres/v0.1.0 cmd/tollgated/v0.1.0
```

`cmd/tollgated` does not import `ext/metrics/postgres`, so the two nested tags
are independent. If a nested module ever requires another, tag the required
one first and bump the other to it, as with the root.

### 4. Watch the release and check it

```sh
gh run watch "$(gh run list --workflow release.yml --limit 1 --json databaseId -q '.[0].databaseId')"
gh release view cmd/tollgated/v0.1.0        # 6 archives + SHA256SUMS
tmp="$(mktemp -d)" && cd "$tmp"
GOWORK=off GOBIN="$tmp" go install github.com/ethosagent/tollgate/cmd/tollgated@v0.1.0
"$tmp/tollgated" version                    # v0.1.0
gh release download cmd/tollgated/v0.1.0 -p 'tollgated_v0.1.0_linux_amd64.tar.gz' -p SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS    # macOS: shasum -a 256 --ignore-missing -c SHA256SUMS
```

## What `release.yml` does

On a push of `cmd/tollgated/v*`:

1. **verify**: `scripts/check-release.sh "$GITHUB_REF_NAME"`, then the full
   gate (`scripts/run-checks.sh`, with the Postgres service).
2. **build**: matrix `linux | darwin | windows` × `amd64 | arm64`, with
   `GOWORK=off CGO_ENABLED=0 go -C cmd/tollgated build -trimpath
   -ldflags "-s -w -X main.version=vX.Y.Z"`. The version is the tag without
   `cmd/tollgated/`. Archives: `tollgated_vX.Y.Z_<os>_<arch>.tar.gz`, `.zip`
   for windows; each holds the binary and `README.md`.
3. **publish**: `SHA256SUMS` over the archives, then `gh release create` with
   generated notes (`contents: write` on this job only). A version with a
   `-` suffix (`v0.2.0-rc.1`) is marked as a prerelease.

Reproduce one target locally:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off \
  go -C cmd/tollgated build -trimpath -ldflags "-s -w -X main.version=v0.1.0" -o ../../dist/tollgated .
file dist/tollgated                         # ELF 64-bit LSB executable, ARM aarch64, statically linked
```

## Later releases

- **Root only** (`vX.Y.Z`): step 1. Nested modules keep their require until
  they release.
- **A nested module**: its require must be the newest root tag reachable from
  `HEAD` (the guard's default; pass a version as the second argument to check
  another). Bump with `go get github.com/ethosagent/tollgate@vX.Y.Z && go mod
  tidy` in the module, merge, then tag. Dependabot also opens this bump.
- **A bad release**: never retag. Add a `retract` directive for it to that
  module's `go.mod`, and release the next patch.
