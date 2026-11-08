# Orientation

A ten-minute tour. Rules are in [`../ARCHITECTURE.md`](../ARCHITECTURE.md); this file only shows
where things are and how a request moves.

## The repo in one picture

```
tollgate.go, options.go, model.go   public API only (Proxy wrapper, New, Options, aliases)
internal/core/                      the pipeline behind the API: Proxy, record, hooks, admin
internal/model/                     neutral types + extension interfaces (stdlib only)
internal/{forward,chain,normalise,resolve,session,metrics}/   the engine parts core wires
ext/<area>/<name>/                  extensions, public API only
testkit/                            fake upstream + plugin harness for extension authors
cmd/tollgated/                      the binary: flags -> Options -> serve
internal/archtest/                  the dependency-law gate
scripts/  .githooks/  .github/      the gate (same script locally, in hooks, in CI)
.agents/  .claude/                  agent skills, review rubrics, risk tiers, hooks, path rules
```

Every package starts with a `doc.go` that says what it does and what it may import; read that
before the code (archtest rule P1 guarantees it exists).

## How one streaming call flows

1. `Proxy.Handler()` receives `POST /openai/v1/chat/completions`; the pipeline that runs
   the steps below is `internal/core` (`proxy.go`, `hooks.go`, `record.go`).
2. **Auth** (`Options.Auth`) turns the request into a `Tenant`, or 401/403.
3. **Admission** (`Options.Admission`) admits it now, queues it, or 429s.
4. **Resolve** (`internal/resolve`) picks the `Provider` from the path prefix; the rest path
   is `/v1/chat/completions`.
5. **Normalise** (`internal/normalise`) reads the capped body once into a `Call`.
6. **Session** (`internal/session`) attributes the call to a `Session` (header or predicted).
7. **Hook.Before** chain (`internal/chain`) may mutate the call or short-circuit it.
8. **Transport** (`internal/forward`) sends it upstream with the client's own credentials.
9. **Tee** (`internal/forward`): each chunk is written and flushed to the client, then a copy
   is parsed into `Event`s for OnEvent hooks on another goroutine.
10. **Finalizer** (only if registered) decides on the terminal event.
11. **Hook.After** and one `Metric` to the bounded queue (`internal/metrics`) → `MetricSink`.

A `stream:false` call replaces 9–10 with a capped buffer and `Hook.OnResponse`.

## Where to look when…

| You want to… | Start at |
|---|---|
| Know what is allowed | `CLAUDE.md`, then `ARCHITECTURE.md` |
| Know why something is the way it is | `docs/decisions/` |
| Add an extension, provider or option | `.agents/skills/` |
| Review a change | `.agents/reviews/` (the PR's tier picks the lenses) |
| Fix a red gate | `.logs/<gate>.log`, then `docs/runbooks/local-dev.md` |
