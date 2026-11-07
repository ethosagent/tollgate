# tollgate

An embeddable LLM proxy for Go, with extension points. Use it as a library
inside your own service, or run the `tollgated` binary.

> **Status: scaffold.** The public types, extension interfaces, options and
> health endpoints exist and are tested. The proxy pipeline returns `501` until
> it lands.

## Embed it

```go
import "github.com/ethosagent/tollgate"

p, err := tollgate.New(tollgate.Options{
	Auth:    myAuth{},   // tollgate.Authenticator
	Metrics: mySink{},   // tollgate.MetricSink
})
if err != nil {
	log.Fatal(err)
}
p.Use(myHook{}) // tollgate.Hook; embed tollgate.NoopHook
log.Fatal(http.ListenAndServe(":8080", p.Handler()))
```

## Run it

```sh
go run ./cmd/tollgated serve -listen :8080
curl -s localhost:8080/healthz          # {"status":"ok"}
```

| Endpoint | Status | Body |
|---|---|---|
| `GET /healthz` | 200 | `{"status":"ok"}` |
| `GET /readyz` | 200 | `{"status":"ready"}` |
| `ANY /{provider}/...` | 501 (for now) | `{"error":{"type":"not_implemented","message":"..."}}` |

## Develop

```sh
scripts/install-hooks.sh   # once
scripts/run-checks.sh      # the gate CI runs
```

Read [`CLAUDE.md`](CLAUDE.md) (the map; `AGENTS.md` is its twin),
[`ARCHITECTURE.md`](ARCHITECTURE.md) (the rules) and [`docs/`](docs/README.md).
