# 0016. Thin root package; the implementation lives in `internal/core`

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** maintainer request (thin-root branch): a tidy repo root, as in warden

## Context

The root package `github.com/ethosagent/tollgate` held both the public API
and the whole proxy pipeline: 41 `.go` files, 12 of them non-test, about
5.6K lines. A reader looking for the API had to skip the engine, and the
root directory was a wall of files. The import path and the public API must
not change: it is a library (ADR 0001) and every extension, `testkit` and the
binary build against it.

Options: (a) leave it; (b) move the implementation into `internal/core` and
alias every public type from the root (`type Options = core.Options`): no
copying, but `go doc` on an alias shows no fields or field docs, so the
documentation of `Options`, `Limits` and `HeaderNames` would move out of
sight; (c) move the implementation into `internal/core`, keep `Options`,
`Limits`, `HeaderNames` and `Proxy` *defined* in the root with their docs,
and hand core a copy of the configuration.

## Decision

(c). The root keeps only the public surface: `doc.go`, `tollgate.go` (`Proxy`
wrapping `*core.Proxy`; `New`, `Use`, `Handler`, `Start`, `Close`,
`AdminHandler` delegate), `options.go` (the documented option types, their
defaults and `Validate`, which delegate) and `model.go` (the model aliases,
ADR 0003). Everything else is in `internal/core`, an ordinary internal
package (layer "internal", D3). `core.Limits` and `core.HeaderNames` repeat
the root structs' fields, so the root converts them with a plain type
conversion; `core.Options` mirrors `Options` field for field.

The rule for what the root defines rather than aliases: the configuration a
host writes (`Options`, `Limits`, `HeaderNames`) and `Proxy`, because hosts
read their field and method docs in `go doc`. Model types an extension
receives (`Call`, `Tenant`, `Policy`, ...) stay aliases of `internal/model`
(ADR 0003); their docs are read in that package.

## Consequences

- The root is five files; the engine has the boundaries of any internal package.
- Adding an `Options`/`Limits`/`HeaderNames` field means adding it in both
  places. The `Limits(…)`/`HeaderNames(…)` conversions do not compile if the
  field lists differ, and `TestOptions_MirrorCore` fails if `Options` and
  `core.Options` differ.
- `ErrClosed` is `core.ErrClosed` re-exported: same value, same message.
- Engine tests live in `internal/core` and, under D3, may not import the root
  or `testkit`; they replay `internal/fixtures` through a small local fake
  upstream. Root tests cover the public contract only.
- Enforced by archtest D3/D4 (core is classified "internal"; a bad-edge case
  checks it may not import the root), the compile-time conversions and
  `TestOptions_MirrorCore`.
