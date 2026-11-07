# 0003. Neutral model in `internal/model`, aliased from the root

- **Status:** Accepted
- **Date:** 2026-11-07
- **Source:** scaffolding; plan "Package layout" put types in the root package

## Context

The engine packages (`internal/chain`, `normalise`, `session`, `metrics`) need
`Call`, `Event`, `Metric` and the extension interfaces. The root package
imports the engine to build `Handler()`. If the types lived in the root, the
engine would have to import the root: an import cycle.

Options: (a) all engine code in the root package: no cycle, but no enforced
boundaries and a very large package; (b) a public `model` sub-package that
extension authors also import; (c) `internal/model` plus type aliases in the root.

## Decision

(c). Types and interfaces are declared in `internal/model`; `model.go` in the
root re-exports every exported name as an alias. Extension authors import only
`github.com/ethosagent/tollgate`.

## Consequences

- One import for extension authors; engine boundaries stay enforced.
- Godoc for an aliased type points at `internal/model`. Keep doc comments there.
- A new exported model name must be aliased; `TestAliases_CoverModel` fails if not.
- Enforced by archtest D2/D3 and `TestAliases_CoverModel`.
