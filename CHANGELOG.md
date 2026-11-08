# Changelog

All notable changes to tollgate. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/). Tagging is described in
[`docs/runbooks/release.md`](docs/runbooks/release.md).

## [Unreleased]

### Added

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

### Changed

- A Finalizer on a Responses stream sees the terminal region at
  `response.completed|incomplete|failed`, and a recovery stream is spliced by
  dropping its `response.created`/`in_progress` and shifting `output_index`.
- Responses finish reasons are provider-native (`completed`, or the
  `incomplete_details` reason such as `max_output_tokens`), not chat's
  `stop` / `tool_calls`. A turn that ends in a tool call reports `completed`;
  read `StreamSummary.ToolCalls`.

### Known limitations

- Custom (freeform) tool calls, built-in tool items, refusals and non-text
  parts are forwarded byte for byte but not normalised.
- `sequence_number` is not rewritten in a spliced recovery stream.
