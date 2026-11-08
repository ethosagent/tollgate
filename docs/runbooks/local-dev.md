# Local development

## Set up

```sh
go version                    # >= 1.26 (go.mod); CI also tests the latest stable
scripts/install-hooks.sh      # pre-commit: fmt+vet+docs; pre-push: full gate
scripts/run-checks.sh         # first run builds pinned tools from tools/go.mod (~1 min)
```

Dev tools are pinned in `tools/go.mod` and run with
`GOWORK=off go tool -modfile=tools/go.mod <tool>` (`-modfile` is refused in
workspace mode; the gates use `gotool` in `scripts/lib.sh`). Nothing to install
globally. Bump them with `cd tools && go get -tool <module>@<version> && go mod tidy`.

## Nested modules and go.work

`cmd/tollgated` and `ext/metrics/postgres` are their own modules. The committed
`go.work` (ADR 0017) makes every `go` command inside them build against this
checkout's root code, not the root release their `go.mod` requires.

- Add a nested module: `go work use ./ext/<area>/<name>` (archtest
  `TestWorkspace` fails until you do).
- Build as a consumer would (published root, no workspace): `GOWORK=off go -C cmd/tollgated build ./...`.
- `go mod tidy` ignores `go.work`, so it needs the required root tag on the proxy.
- Releasing: [`release.md`](release.md).

## Run

```sh
go -C cmd/tollgated run . serve -listen :8080
curl -s localhost:8080/healthz
curl -s -X POST localhost:8080/openai/v1/chat/completions -d '{}'   # 501 until the pipeline lands
```

With a config file (see `configs/tollgated.example.yaml`; secrets via `${VAR}`):

```sh
export TOLLGATE_KEY_TEAM_A=... TOLLGATE_KEY_TEAM_B=...
go -C cmd/tollgated run . config check -config ../../configs/tollgated.example.yaml -json
go -C cmd/tollgated run . serve -config ../../configs/tollgated.example.yaml
```

## Debug a red gate

1. `scripts/run-checks.sh` prints the last 40 lines of each failing gate.
2. Full output: `.logs/<gate>.log`. Coverage profile: `.logs/coverage.out`.
3. Re-run one gate: `scripts/run-checks.sh lint`. Machine-readable: `scripts/run-checks.sh --json`.
4. One command, quietly: `scripts/quiet.sh go test -run TestValidate .`

## Known environment issue (macOS)

If linking fails with `tapi error: malformed file ... unknown architecture`,
the Command Line Tools SDK is newer than the linker. Pure-Go tools still work
(the lint and vuln gates set `CGO_ENABLED=0`). Fix the host with
`sudo rm -rf /Library/Developer/CommandLineTools && xcode-select --install`.
