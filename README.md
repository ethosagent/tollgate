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

## Testing your extension

`testkit.Run` drives your plugin (an `Authenticator`, `Hook`, `Finalizer` or
`MetricSink`) through a real `tollgate.New` and a fake upstream on loopback,
one call per recorded provider response, and hands back every call the plugin
received, in order, with copies of its arguments. No network, no keys.

```go
import (
	"context"
	"testing"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// auditHook is the extension under test: it tags every call in Before.
type auditHook struct{ tollgate.NoopHook }

func (auditHook) Name() string { return "audit" }

func (auditHook) Before(_ context.Context, c *tollgate.Call) (*tollgate.Response, error) {
	c.Meta = map[string]any{"audited": true}
	return nil, nil
}

func TestAuditHook(t *testing.T) {
	rec := testkit.Run(t, auditHook{}, "openai/chat-stream-text", "anthropic/messages")

	before := rec.Of(testkit.StageBefore) // what Before was given, per call
	if len(before) != 2 || before[1].Call.Model != "claude-sonnet-4-5" {
		t.Fatalf("Before saw %d calls: %+v", len(before), before)
	}
	if n := len(rec.Of(testkit.StageAfter)); n != 0 { // only the stages it implements
		t.Fatalf("After ran %d times; auditHook only implements Before", n)
	}
	if rec.Replies[0].Status != 200 { // what the client got
		t.Fatalf("client status = %d", rec.Replies[0].Status)
	}
}
```

`testkit.RunWith` takes a `Config` for several plugins at once (hooks and
finalizers in order; one `Authenticator` and one `MetricSink` at most), request
headers (a tenant key for an `Authenticator`), and upstream faults (latency,
a cut stream, a status). A hook is offered only the stages it implements, as
in production. Credential
headers are used but never recorded. `testkit.FixtureNames()` lists the
fixtures. A worked example is `ext/auth/statickeys/testkit_test.go`.

## Measuring overhead

```sh
go run ./cmd/tollgate-bench -sessions 200 -calls 4 -fixture openai/chat-stream-text -chunk-delay 20ms
go run ./cmd/tollgate-bench -sessions 50 -calls 4 -json
```

Each session makes pairs of calls on warm connections, one straight at the
fake upstream and one through an in-process tollgate. The fake stamps when it
received each request and wrote each chunk, so the added latency (p50/p99, to
first byte and per chunk) excludes the fake's own delays and is valid with
`-chunk-delay`. Memory per open stream is read in a separate pass with every
stream held open. Also printed: end-to-end times and the drop counters from
`/statusz`. One process over loopback: compare runs on the same machine.

## Develop

```sh
scripts/install-hooks.sh   # once
scripts/run-checks.sh      # the gate CI runs
```

Read [`CLAUDE.md`](CLAUDE.md) (the map; `AGENTS.md` is its twin),
[`ARCHITECTURE.md`](ARCHITECTURE.md) (the rules) and [`docs/`](docs/README.md).
