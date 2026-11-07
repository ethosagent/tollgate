---
name: add-extension
description: Add a built-in or example extension under ext/ (Authenticator, Admission, Hook, MetricSink, Finalizer). Use when new behaviour can live outside the core.
---

# Add an extension

Rules: `CLAUDE.md` (dependency law D5, non-negotiables). Lenses: `STR-*`,
`PRIV-*`, `HOT-*`, `TEST-*` in `.agents/reviews/`.

## Steps

1. **Pick the seam.** `ARCHITECTURE.md` §5. If no seam fits, stop: a new seam
   is an ADR (`write-adr`), not an extension.
2. **Create `ext/<area>/<name>/`** with `doc.go` stating what it does, which
   seam(s) it implements and its config. Import only `github.com/ethosagent/tollgate`
   and the standard library (archtest D5). A third-party import needs an ADR and
   an archtest allowlist entry first.
3. **Test first.** `<name>_test.go` with table-driven cases: happy path,
   each error path, and the default/off behaviour. Use `httptest` or `testkit`.
4. **Implement.** A constructor `New(cfg Config) (*X, error)` that validates
   once (HOT-2). Hooks embed `tollgate.NoopHook` and override `Name()` plus the
   methods they need. Add a compile-time check: `var _ tollgate.Hook = (*X)(nil)`.
5. **Respect the guarantees.**
   - `Before` returns fast; anything slow takes `ctx` and honours its deadline.
   - `OnEvent` only observes; never block; tolerate missed events (ADR 0004).
   - Read per-tenant settings from `Call.Tenant.Policy` / `Attrs`, not globals.
   - Log only metadata through the logger you are given (PRIV-1, PRIV-2).
6. **Document wiring** in `doc.go` with a short example:
   `p.Use(name.New(cfg))` or the `Options` field it fills.
7. Run `scripts/run-checks.sh`. Open the PR with the template; tier is `normal`
   unless you touched a critical path.

## Example shape

```go
// Package budget rejects calls once a tenant's spend cap is reached.
package budget

type Hook struct {
	tollgate.NoopHook
	caps map[string]int64
}

func (h *Hook) Name() string { return "budget" }

func (h *Hook) Before(ctx context.Context, c *tollgate.Call) (*tollgate.Response, error) {
	// decide from c.Tenant; return a provider-shaped 429 Response to short-circuit
}
```
