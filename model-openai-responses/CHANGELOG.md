# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

- Send local user and tool images as base64 data URLs in complete and streaming
  requests, without Files API uploads, file-ID caches, or upload diagnostic files.

- Derive a stable Session cache key for `prompt_cache_key` and the Responses
  `session-id` / `thread-id` headers so compatible gateways can route related
  requests consistently across rounds, turns and runtime restarts.
- Report provider `input_tokens` and `cached_input_tokens` in successful
  Complete/Stream diagnostics, preserving missing versus explicit zero counts.

- Preserve ordered Responses output items, encrypted reasoning, assistant phase,
  and message boundaries for subsequent stateless requests. Request encrypted
  reasoning explicitly. Scope replay to the provider, requested model and endpoint;
  edits to message fields fall back to ordinary encoding. Malformed matching
  state fails before dispatch. Complete and Stream use the same replay path.

Requires published SDK v0.2.18 for `model.Message.Replay`; validate with
`GOWORK=off` against that exact dependency.

### Changed

- Mark truncated SSE, transient provider error codes and explicit HTTP/2 transport failures retryable; diagnostics report whether stream reset is required.

### Added

- Accept `max` and `ultra` as explicit reasoning efforts in provider defaults
  and per-model overrides, and offer both in the configuration form.
- Add per-attempt structured diagnostics for Responses requests: correlation,
  HTTP/TLS negotiation and connection reuse, request IDs, stream progress/timing,
  and failure origin with typed HTTP/2/network details. Omit content and arbitrary
  error strings, and redact configured secrets from bounded metadata.
- Publish configured models and their supported reasoning-effort values through
  `model.ProviderEntry`, and map the request-selected value to
  `reasoning.effort` on Responses API requests.
- Allow individual models to override the provider-level reasoning efforts;
  an empty override disables explicit reasoning effort for that model.

Requires published SDK v0.2.20 for `model.ReasoningEffortMax` and
`model.ReasoningEffortUltra`; validate with `GOWORK=off` against that exact
dependency.

### Changed

- Replace static provider exports with a live `model.ProviderSource`; saved
  provider configuration applies to subsequent calls without restarting,
  while existing calls retain their original provider instances.
- Expose provider invocation callbacks directly in `model.ProviderEntry`,
  removing the old provider interfaces and streaming type assertions.

### Fixed

- Accept image-only and mixed text/image tool outputs in Complete and Stream
  requests as native `function_call_output` content arrays, preserving content
  order, call IDs, image validation, and the existing text-only wire format.
- Ignore SSE `keepalive` heartbeats before, during, and after a terminal response
  instead of aborting the stream with a protocol error.
- Persist reasoning-effort selections submitted by a host as a MultiChoice
  value so `model-runtime` can expose the configured choices.
- Map the leading SDK system message to the Responses API `instructions`
  field instead of including it in conversation `input`.

### Added

- Add an independent OpenAI Responses API model provider with complete and
  typed SSE streaming support.
- Map SDK messages, images, function calls, function outputs, tools, usage,
  incomplete responses, and transient reasoning summaries.
- Keep invocation stateless with `store:false`; provider continuation state is
  replayed through durable SDK messages across tool rounds.
- Expose provider configuration through `/model-openai-responses config` with
  secret-safe API key and header updates.
