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
2. **Test first** in `options_test.go`:
   - the zero value takes the default (`TestLimits_PartialOverrideKeepsOtherDefaults` style);
   - invalid values fail `Validate` with a message naming the field;
   - for `Limits`, add the row to `TestDefaultLimits_MatchDocumentedValues`.
3. **Add the field** in `options.go` with a doc comment giving the unit and default.
4. **Default** it in `withDefaults` (and `DefaultLimits` / `DefaultHeaderNames`).
   The zero `Options{}` must keep working (API-2).
5. **Validate** it in `validate()`; return all problems, never stop at the first.
6. **Document** it in `ARCHITECTURE.md` §6 (protected: say so in the PR) and,
   if the binary exposes it, as a `cmd/tollgated` flag.
7. Run `scripts/run-checks.sh`.
