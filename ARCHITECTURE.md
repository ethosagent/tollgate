# tollgate architecture

The constitution. It changes only by the amendment procedure in §8. Cap: 300
lines (`scripts/check-docs.sh`).

## 1. Axioms

1. **The library is the product.** `cmd/tollgated` is a convenience wrapper
   built only on the public API. If the binary needs something the public API
   cannot give, the public API is wrong.
2. **The core is small and dumb.** Resolve upstream, identify session,
   normalise, forward-and-tee, emit metric. Everything else is an extension.
3. **Client-first.** The client's stream is never slower, later or different
   because of an extension, unless that extension is explicitly allowed to
   change it (Before, OnResponse, Finalizer).
4. **Isolation over correctness of extensions.** A panicking, slow or wrong
   extension is skipped; the call proceeds as a pass-through.
5. **No hidden state.** No globals, no `init()` registration, no plugin
   loading. Everything arrives through `Options`.
6. **Stdlib-only core.** The root package and `internal/` import no
   third-party module (ADR 0002). Extensions may, with an ADR.

## 2. Vocabulary

| Term | Meaning |
|---|---|
| Tenant | Who is calling; resolved by the Authenticator; partitions everything. Opaque `ID` + `Attrs` + optional `Policy`. |
| Session | One multi-call task (an agent run). Header-supplied or predicted. |
| Call | One LLM request, normalised. `Body` mutable, `Meta` shared scratch space. |
| Event | One parsed streaming chunk: text, reasoning, tool-call delta, usage, finish, error. |
| Provider | Upstream API family + `WireFormat` + base URL. |
| Extension | Anything implementing one or more interfaces in §5. |

Use these words everywhere. Do not add product-specific terms to the library.

## 3. Layers and dependency law

```
cmd/tollgated ─┐
ext/**        ─┼──► tollgate (root: public API) ──► internal/* ──► internal/model
testkit       ─┘                                       (engine)      (neutral model)
```

| Layer | Packages | May import | Rule |
|---|---|---|---|
| model | `internal/model` | stdlib | D2 |
| internal | `internal/*` (engine) | stdlib, `internal/*` | D3 |
| root | `tollgate` | stdlib, `internal/*` | D4 |
| ext, cmd | `ext/**`, `cmd/**` | stdlib, root, `ext/**`, `testkit` (tests), allowlisted third-party | D5 |
| testkit | `testkit/**` | stdlib, root | D6 |
| archtest | `internal/archtest` | stdlib | D6 |

D1: a package outside every layer fails the gate. D7: any third-party import
must be on the archtest allowlist for its layer, added with an ADR. Test
files: `_test.go` files in any layer may also import `testkit` for fixtures
and the fake upstream (archtest checks non-test imports only); root tests may
import `internal/*`. Production code never imports `testkit`.
Enforcement: `internal/archtest` (stage `arch`) and `scripts/check-mod.sh`.

Why the model lives in `internal/model` and is aliased from the root: the
engine packages need the types, and the root package imports the engine, so
the types cannot live in the root without a cycle (ADR 0003).

## 4. Call lifecycle

```
request ─► Auth ─► Admission ─► ProviderResolver ─► limits/normalise ─► Session ─► Hook.Before… ─► Transport
   ├─ stream:true  ─► tee ─► Hook.OnEvent… ─► Finalizer.Decide ─► release terminal ─► Hook.After… ─► MetricSink
   └─ stream:false ─► buffer JSON ─► Hook.OnResponse… ─► write ─► Hook.After… ─► MetricSink
                                                             (later) Hook.OnSessionClosed
```

- The tee (`internal/forward`) writes and flushes each upstream chunk to the
  client **before** handing a copy to the parser. Parsed events go to a
  bounded channel consumed by OnEvent hooks in another goroutine.
- Admission fires right after Auth so a rejected tenant costs nothing.
- Finalizers hold only the terminal event (`[DONE]` / `message_stop`).

## 5. Seams (extension points)

| # | Interface | Default | Fires |
|---|---|---|---|
| 1 | `Authenticator` | `NoAuth` | first; error → 401/403, provider-shaped |
| 2 | `ProviderResolver` | path prefix (`internal/resolve`); the core then applies the upstream override and checks the target | after Admission |
| 3 | `SessionStrategy` | header-or-predict (`internal/session`) | after normalise |
| 4 | `Hook` | none (embed `NoopHook`) | Before / OnEvent / OnResponse / After / OnSessionClosed |
| 5 | `MetricSink` | `DiscardSink` | batched, off the request path |
| 6 | `Options.Transport` | pooled HTTP/2 transport (`internal/forward`) | every upstream call |
| 7 | `Admission` | `Unlimited` | right after Auth; 429 + `Retry-After` |
| 8 | `Finalizer` | none (zero overhead) | before the terminal stream event |
| 9 | `Hook.OnResponse` | pass-through | `stream:false` only |

Stability: after v1.0 these interfaces are frozen. New capability arrives as a
new optional interface that the core type-asserts, so old plugins compile.

## 6. Limits (defaults in `DefaultLimits`, test: `TestDefaultLimits_MatchDocumentedValues`)

| Limit | Default |
|---|---|
| Max request body (pre-parse) | 16 MiB |
| Upstream connect / first byte / idle stream | 10 s / 60 s / 120 s |
| Max call duration | 15 min |
| Hook.Before deadline | 50 ms each, 200 ms chain |
| Finalizer budget / recovery ceiling | 2 per finalizer / 10 per call |
| Event buffer / metric queue | 1024 / 10 000; overflow drops + counts |
| Session TTL / max live sessions | 5 min / 100 000 (LRU) |
| Transport pool per host | 100 conns, 20 idle, 30 s idle expiry, HTTP/2 |

Header names (`HeaderNames`) are options, not constants. Defaults:
`x-proxy-key`, `x-session-id`, `x-upstream-url`. tollgate strips its own
headers, hop-by-hop headers and any header an extension declares through
`HeaderConsumer` (ADR 0009) before forwarding.

- **Upstream target (ADR 0007).** Whatever resolver ran, `forward.Target`
  checks the final URL: the resolver's base, or the override header only when
  `Policy.AllowedUpstreams` matches (exact host/host:port, IP or CIDR, no DNS);
  http(s), a host, no userinfo.
- **Transparency.** The default transport never compresses (no added
  `Accept-Encoding`, no decoding); the inbound escaped path (`%2F`) and query
  reach the upstream byte for byte.

## 7. Failure behaviour

| Failure | Behaviour | `Metric.Finish` |
|---|---|---|
| Auth error | 401/403, provider-shaped body | `rejected` |
| Admission reject | 429 + `Retry-After` | `rejected` |
| Upstream 4xx/5xx | relayed as-is | `upstream_error` |
| Upstream drop mid-stream | partial bytes stay sent; provider-shaped error event; close | `upstream_error` |
| Client disconnect (incl. during an admission wait) | upstream request cancelled via context; nothing written | `client_cancel` |
| Deadline hit | provider-shaped timeout error | `timeout` |
| Auth/Admission/resolver panic | recovered; provider-shaped 500; logged with seam, tenant, call id | `rejected` |
| Hook panic/timeout | logged with tenant + call id; hook skipped; call proceeds | unchanged |
| Sink down | metrics dropped and counted; `/readyz` fails after N consecutive failures | unchanged |

A stream that times out or whose upstream drops after the first byte ends
with a provider-shaped SSE error event and a clean close (`Finish` timeout /
upstream_error); non-SSE bodies are still aborted; response headers are sent
with the first body byte. The event-stream check is made once per response
from its media type. The OnEvent frame queue is bounded in frames and bytes
(`EventBuffer` frames, `EventBuffer` × 4 KiB); drops are counted and reported
to the consumer as gaps, and the queue's end carries the call's `Finish`.

Extension errors map to a status through one contract (ADR 0008): a
`StatusError` with a 4xx/5xx `HTTPStatus()` wins, else the seam default (Auth
401, Admission 429, resolver 404); `RetryAfterError` sets `Retry-After`.
The body message is fixed per status; no extension or transport error text
reaches the client. Core errors: override not allowed 403, bad override 400,
body too large 413, bad resolver target 502, upstream unreachable 502, timed
out 504, extension panic 500.

Endpoints served by the core: `ANY /{provider}/*`, `GET /healthz`,
`GET /readyz`; later `GET /statusz` (host-only) and `GET /metrics` (optional).

## 8. Amendment procedure

1. Write an ADR in `docs/decisions/` (skill: `write-adr`) that states the
   change, the alternatives and what enforces the new rule.
2. Update this file and `internal/archtest` (or the lint config) in the same
   PR, so the rule and its enforcement never disagree.
3. A human maintainer approves: this file and the gates are protected paths.
