# Review rubrics

One file per **lens**, so a reviewer looks at one kind of risk at a time. Each
check has a stable ID. Rubrics hold checks that need **judgement**; anything a
script can decide belongs in `scripts/check-*.sh`, `.golangci.yml` or
`internal/archtest` instead.

| Lens | File | IDs | Use it when |
|---|---|---|---|
| Streaming | [`streaming.md`](streaming.md) | `STR-*` | Anything on the request/stream path |
| Privacy | [`privacy.md`](privacy.md) | `PRIV-*` | Logging, metrics, capture, headers, errors |
| API stability | [`api-stability.md`](api-stability.md) | `API-*` | Any exported identifier, `internal/model`, `testkit` |
| Hot path | [`hot-path.md`](hot-path.md) | `HOT-*` | Code that runs per call or per chunk |
| Test quality | [`test-quality.md`](test-quality.md) | `TEST-*` | Every PR that adds or changes behaviour |

Which lenses a PR needs comes from its risk tier (`.agents/risk.yaml`,
`.agents/pipeline.yaml`).

## Finding format

```json
{
  "lens": "streaming",
  "rule": "STR-2",
  "severity": "critical",
  "file": "internal/forward/tee.go",
  "line": 42,
  "issue": "the parser runs before Flush, so a slow parse delays the client",
  "evidence": "tee.go:42 calls parse(chunk) before w.(http.Flusher).Flush()",
  "suggestion": "write and flush first, then send the copy to the parser channel"
}
```

`evidence` points at code, a test or command output. No evidence = a question,
not a finding. Use `STR-0`, `PRIV-0`, … for a real problem the rubric lacks.

## Severity

| Severity | Meaning | Action |
|---|---|---|
| critical | Breaks a non-negotiable: client stalled, credential logged, extension can take the proxy down | Block |
| high | Data loss, wrong metric, broken acceptance criterion | Fix before merge |
| medium | Cost later: coupling, missing test for a risky branch | Fix or record why not |
| low | Naming, small cleanups | Optional |

## From review to rule

When the same rule ID is confirmed on two PRs, turn it into a deterministic
check (archtest rule, lint rule, test) and mark it **covered by CI** here.
