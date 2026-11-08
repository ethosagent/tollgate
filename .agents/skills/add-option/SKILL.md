---
name: add-option
description: Add a field to Options, Limits or HeaderNames with a default, validation, docs and tests. Use when a host needs to configure new core behaviour.
---

# Add an option

Rules: `CLAUDE.md`. Lenses: `API-*`, `TEST-*`.

## Steps

1. **Ask whether it belongs in the core.** Per-tenant behaviour is data on
   `Tenant.Policy`, not a global option. Extension config belongs to the
   extension's own `Config`.
2. **Test first** in `internal/core/options_test.go`:
   - the zero value takes the default (`TestLimits_PartialOverrideKeepsOtherDefaults` style);
   - invalid values fail `Validate` with a message naming the field;
   - for `Limits`, add the row to `TestDefaultLimits_MatchDocumentedValues`.
3. **Add the field** in the root `options.go` with a doc comment giving the
   unit and default, and the same field, same place, in its mirror
   `internal/core/options.go` (ADR 0016). For `Options` also copy it in
   `Options.core`; `TestOptions_MirrorCore` and `TestOptions_CoreCopiesEveryField`
   fail otherwise. `Limits`/`HeaderNames` mismatches do not compile.
4. **Default** it in `withDefaults` (and `DefaultLimits` / `DefaultHeaderNames`)
   in `internal/core/options.go`. The zero `Options{}` must keep working (API-2).
5. **Validate** it in `internal/core/options_validate.go`; return all problems,
   never stop at the first.
6. **Document** it in `ARCHITECTURE.md` §6 (protected: say so in the PR) and,
   if the binary exposes it, as a `cmd/tollgated` flag.
7. Run `scripts/run-checks.sh`.
