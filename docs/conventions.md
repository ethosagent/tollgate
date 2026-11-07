# Conventions

Rules a linter cannot fully decide. What a linter does decide lives in
`.golangci.yml` and `internal/archtest`.

## Size

- Source file: aim for ≤ 300 lines (the gate warns); it fails above 800. Test files ≤ 500.
- Function: ≤ 50 lines (`funlen`). Cognitive complexity ≤ 20 (`gocognit`).
- One file, one idea. Name the file after the idea (`tee.go`, not `utils.go`).

## Errors

- Wrap with context: `fmt.Errorf("resolve provider %q: %w", name, err)`.
- Compare with `errors.Is` / `errors.As`, never by string.
- Sentinel errors are `var ErrX = errors.New("tollgate: ...")` in the package that returns them.
- Validation returns **all** problems at once with `errors.Join` (see `Options.Validate`).
- The library never panics on bad input. Panics in extensions are recovered by `internal/chain`.

## Logging

- Only through the injected `*slog.Logger` (`Options.Logger`). No `log`, no `fmt.Print`.
- Every line about a call carries `tenant`, `session`, `call`, `provider`, `model`.
- Never log: credentials, any request or response body, reasoning, tool arguments,
  raw SSE bytes. Privacy mode (default on) is the floor, not a setting to work around.
- Errors at the boundary where they are handled, once. Do not log and return.

## Context and concurrency

- Every blocking call takes a `context.Context` as its first parameter.
- A goroutine has an owner that stops it: a context, a closed channel, or `Close`.
- Channels between the request path and background work are bounded. On full:
  drop and count; never block the client.
- `go test -race` must stay clean. Shared state is behind a mutex or atomics;
  document which.

## Public API

- Everything exported from the root package is semver-stable once v1 ships.
- Add a capability as a new optional interface; never add a method to an
  existing interface.
- Exported identifiers have a doc comment that starts with the name.
- Zero values are useful: a zero `Options{}` must work.

## Tests

- Test first. A bug fix starts with a test that fails for the bug.
- Table-driven, `t.Run` per case, names that read as behaviour
  (`TestValidate/duplicate_header_names`).
- No network, no sleeps for synchronisation (poll with a deadline instead), no
  real keys. Upstreams are `httptest` servers or `testkit.FakeUpstream`.
- Assert behaviour at the boundary (HTTP status, body shape, emitted metric),
  not private state, unless the private state is the contract.
- Coverage floor: 75 % total (`scripts/check-test.sh`). Raise it, never lower it.

## Naming

- Use the vocabulary in `ARCHITECTURE.md` §2. Spelling follows the plan
  (`normalise`); either UK or US spelling passes the linter.
- Packages are short nouns (`chain`, `session`), no `util`, `common`, `helpers`.
- Extensions: `ext/<area>/<name>` (e.g. `ext/auth/statickeys`).
