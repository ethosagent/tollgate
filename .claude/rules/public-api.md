---
paths:
  - "model.go"
  - "options.go"
  - "doc.go"
  - "internal/model/**"
  - "testkit/**"
---

# Public API rules

Everything here is exported to extension authors (ADR 0001, 0003). Checklist: `.agents/reviews/api-stability.md` (API-*).

- A new exported name in `internal/model` needs an alias in `model.go` (`TestAliases_CoverModel`).
- Never add a method to an existing interface; add a new optional interface the core type-asserts.
- Zero values must work: default every new `Options`/`Limits` field in `withDefaults`, validate in `validate()`.
- Doc comment on every exported identifier, starting with its name; state units, default and concurrency safety.
- Use the vocabulary in `ARCHITECTURE.md` §2; no product or vendor terms.
- A change that breaks callers needs an ADR (before v1) or a major version (after). Say so in the PR.
- Use the `add-option` skill for options; `write-adr` for a new seam.
