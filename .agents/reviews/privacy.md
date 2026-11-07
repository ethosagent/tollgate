# Privacy (`PRIV-*`)

Guards non-negotiables 4 and 5 in `CLAUDE.md`.

## Already covered by CI

- Lint bans `log.Print*` and `fmt.Print*` (all logs go through `slog`).

## Checks

**PRIV-1 Logs carry metadata, not content.** No request/response body,
message text, reasoning, tool arguments or raw SSE bytes in any log line, at
any level, whatever the privacy-mode setting of the caller.

**PRIV-2 Credentials are never logged or stored.** `Authorization`,
`x-api-key`, the tenant-key header and any header that looks like a secret are
redacted before a header set reaches a logger, error, metric or capture.

**PRIV-3 Upstream credentials pass through untouched.** The core never reads,
rewrites, caches or persists them; only the transport sends them.

**PRIV-4 tollgate's own headers are stripped before forwarding.** The headers
in `HeaderNames` and hop-by-hop headers never reach the upstream.

**PRIV-5 Errors are provider-shaped and leak nothing.** Error bodies sent to
clients contain no stack, no internal address, no other tenant's data.

**PRIV-6 Tenant isolation.** Session lookup, admission and metrics are keyed
by tenant; no code path can return one tenant's session or data to another.

**PRIV-7 Capture is opt-in and capped.** Any capture of bodies is per tenant,
explicitly enabled (`Policy.Capture`), size-capped and sent only to a sink the
host provides.

**PRIV-8 Metric labels are bounded.** No user-controlled free text (model
names are allowed; prompts, ids from bodies are not) becomes a metric label.
