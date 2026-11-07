# API stability (`API-*`)

The library is the product (ADR 0001). Every exported name is a promise.

## Already covered by CI

- archtest D5/D6 (extensions and testkit use the public API only).
- `TestAliases_CoverModel` (every model name is re-exported from the root).

## Checks

**API-1 Frozen interfaces.** After v1, no method is added to or removed from
an exported interface. New capability = new optional interface, type-asserted.

**API-2 Useful zero values.** A new `Options`/`Limits` field works when left
zero (default applied in `withDefaults`, documented in `ARCHITECTURE.md` §6).

**API-3 Neutral vocabulary.** No product, customer or vendor term in exported
names or docs. Use `ARCHITECTURE.md` §2.

**API-4 Minimal surface.** Do not export a type, function or field that only
the core uses. Unexported until an extension needs it.

**API-5 Documented.** Every exported identifier has a doc comment that starts
with its name and states units, defaults and concurrency safety.

**API-6 Breaking changes are explicit.** A change that breaks a caller is
called out in the PR and needs a major version (after v1) or an ADR (before).

**API-7 Extensions can be tested without the network.** A new seam comes with
`testkit` support in the same or the next PR.
