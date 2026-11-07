# 0001. Library first, binary as a thin wrapper

- **Status:** Accepted
- **Date:** 2026-11-07
- **Source:** plan, "Two ways to run it"

## Context

Our own service embeds the proxy with its own auth, hooks and metric sink.
Other teams want a URL. Options: (a) a service with YAML config, extended by
plugins; (b) a Go library, with the service as one consumer of it.

## Decision

The library (`github.com/ethosagent/tollgate`) is the product. `cmd/tollgated`
builds `Options` from flags/config and uses only the public API. Custom
extensions mean building your own binary from the library: no `.so` loading.

## Consequences

- Extensions are plain Go packages, versioned and unit-tested with the host.
- Everything the binary does, an embedder can do.
- No runtime plugin loading; adding an extension to the binary means a rebuild.
- Enforced by archtest D5 (`cmd/**` and `ext/**` cannot import `internal/`).
