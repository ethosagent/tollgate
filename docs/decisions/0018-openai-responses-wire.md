# 0018. OpenAI Responses wire, chosen per request from the path

- **Status:** Proposed
- **Date:** 2026-11-09
- **Source:** Meterwise plan 08 (OpenAI Responses API wire)

## Context

Codex and OpenCode talk to `POST /openai/v1/responses`. Until now that path
reached the chat normaliser: the model and stream flag were read, but no
messages, tool calls, reasoning or usage, so monitors, Finalizers and metrics
were blind to the traffic. The Responses API differs from chat in shape
(`input` items, `instructions`, typed SSE events, usage on a closing
`response.completed`), so it needs its own normaliser.

A wire format is a property of `model.Provider`, and one provider (`openai`)
serves both APIs. Options: (a) a second provider prefix such as
`/openai-responses`, which breaks the client's base URL; (b) let each
resolver return the wire per path, which every custom resolver must then
copy; (c) the core refines the wire after any resolver answers, from the
upstream path.

## Decision

(c). `resolve.WireForRest` maps an `openai.chat` provider on exactly
`/v1/responses` to the new `WireOpenAIResponses`; the core applies it to the
answer of every resolver, and the default resolver and `WireForPath` (early
error shape) apply it too. Other wires, other paths and `/v1/responses/{id}`
sub-resources are untouched, so a resolver that declares pass-through is never
overridden.

The wire maps `response.output_text.delta` to text, `response.reasoning_*.delta`
to reasoning, `function_call` items and `response.function_call_arguments.delta`
to tool calls (numbered by `output_index`), `custom_tool_call` (Codex
apply_patch) and `local_shell_call` items to tool calls too (arguments are the
freeform input as a JSON string, or the shell action object, taken from
`response.output_item.done`; their outputs become `RoleTool` messages), and the terminal
`response.completed|incomplete|failed` to finish reason and usage. The finish
reason is provider-native: `completed`, or `incomplete_details.reason`. It is
not rewritten to chat's `stop`/`tool_calls`. `previous_response_id` and
`reasoning.effort|summary` land in `Call.Meta` under new `model.Meta*` keys.
The terminal region opens at the closing frame; a recovery splice drops the
second `response.created`/`in_progress` and shifts `output_index`.

## Consequences

- Adds public API: `WireOpenAIResponses` and three `Meta*` keys (aliased in
  `model.go`, `TestAliases_CoverModel`). No interface changes.
- Hosts that branch on finish reasons must know Responses reports `completed`
  even when the turn ends in a tool call; read `StreamSummary.ToolCalls`.
- Not mapped: built-in tool items (web search, computer use, tool search, MCP),
  refusals, and image/audio parts. They pass through byte for byte. Request and
  output item fields whose JSON type varies by item type are decoded only for the
  item types that use them, so an unmodelled item never fails the parse.
- `response.failed` reports finish reason `failed` as well as an error event;
  error codes may be strings or numbers. `/v1/responses/compact` is not normalised.
- `sequence_number` is not rewritten when a recovery is spliced.
- Enforced by `TestWireForRest`, `TestResponses_WireChosenForEveryResolver`,
  the golden fixtures `openai/responses*`, and
  `TestFinalizer_RecoverOnceThenRelease/responses`.
