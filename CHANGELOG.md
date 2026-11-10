# Changelog

All notable changes to tollgate. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/). Tagging is described in
[`docs/runbooks/release.md`](docs/runbooks/release.md).

## [Unreleased]

## [0.2.0] - not yet tagged

### Added

- `Usage.CacheWriteInputTokens`: Anthropic `cache_creation_input_tokens`
  (streamed and buffered), which `input_tokens` excludes. 0 on the OpenAI
  wires. Not yet exported by the Prometheus registry or the stdout and
  Postgres sinks ([ADR 0019](docs/decisions/0019-host-facing-usage-and-dispatch-signals.md)).
- `Metric.Dispatched`: true when the upstream request was attempted, false for
  a refusal before it (auth, admission, resolver, target, body, Before
  short-circuit). `Finish` is unchanged.
- `Options.Transport` documents that a custom transport must set
  `DisableCompression` to keep tollgate transparent; the default already does.
- **OpenAI Responses wire.** `POST /openai/v1/responses` (and
  `/openrouter/v1/responses`), streamed and not, is now normalised into the
  neutral model instead of being read as chat: `instructions`, `input` items,
  `function_call` / `function_call_output`, reasoning summaries, text and
  tool-call events, usage (cached and reasoning tokens) and the finish reason.
  This is the wire Codex and OpenCode speak. The wire is chosen per request
  from the upstream path, so every resolver gets it, including custom ones
  ([ADR 0018](docs/decisions/0018-openai-responses-wire.md)).
- Public API: `WireOpenAIResponses` and the `Call.Meta` keys
  `MetaPreviousResponseID`, `MetaReasoningEffort`, `MetaReasoningSummary`.
- `testkit` fixtures `openai/responses`, `openai/responses-stream-{text,tools,reasoning,error}`
  and requests `openai/request-responses-{stream,tools}`; `testkit.Run` drives
  hooks and Finalizers through them.

- Custom tool calls (Codex apply_patch) and local shell calls are normalised
  into tool calls and `RoleTool` messages, in requests and streams.

### Changed

- A Finalizer on a Responses stream sees the terminal region at
  `response.completed|incomplete|failed`, and a recovery stream is spliced by
  dropping its `response.created`/`in_progress` and shifting `output_index`.
- Responses finish reasons are provider-native (`completed`, or the
  `incomplete_details` reason such as `max_output_tokens`; `failed` for
  `response.failed`), not chat's
  `stop` / `tool_calls`. A turn that ends in a tool call reports `completed`;
  read `StreamSummary.ToolCalls`.

### Known limitations

- Built-in tool items (web search, computer use, tool search, MCP), refusals
  and non-text parts are forwarded byte for byte but not normalised;
  `/v1/responses/compact` stays unparsed.
- `sequence_number` is not rewritten in a spliced recovery stream.
