# Local development

## Set up

```sh
go version                    # >= 1.26 (go.mod); CI also tests the latest stable
scripts/install-hooks.sh      # pre-commit: fmt+vet+docs; pre-push: full gate
scripts/run-checks.sh         # first run builds pinned tools from tools/go.mod (~1 min)
```

Dev tools are pinned in `tools/go.mod` and run with
`go tool -modfile=tools/go.mod <tool>`. Nothing to install globally. Bump them
with `cd tools && go get -tool <module>@<version> && go mod tidy`.

## Run

```sh
go run ./cmd/tollgated serve -listen :8080
curl -s localhost:8080/healthz
curl -s -X POST localhost:8080/openai/v1/chat/completions -d '{}'   # 501 until the pipeline lands
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
