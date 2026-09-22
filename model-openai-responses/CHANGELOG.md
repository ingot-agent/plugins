# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Publish configured models and their supported reasoning-effort values through
  `model.ProviderEntry`, and map the request-selected value to
  `reasoning.effort` on Responses API requests.
- Allow individual models to override the provider-level reasoning efforts;
  an empty override disables explicit reasoning effort for that model.

### Changed

- Replace static provider exports with a live `model.ProviderSource`; saved
  provider configuration applies to subsequent calls without restarting,
  while existing calls retain their original provider instances.
- Expose provider invocation callbacks directly in `model.ProviderEntry`,
  removing the old provider interfaces and streaming type assertions.

### Fixed

- Persist reasoning-effort selections submitted by a host as a MultiChoice
  value so `model-runtime` can expose the configured choices.
- Map the leading SDK system message to the Responses API `instructions`
  field instead of including it in conversation `input`.

### Added

- Add an independent OpenAI Responses API model provider with complete and
  typed SSE streaming support.
- Map SDK messages, images, function calls, function outputs, tools, usage,
  incomplete responses, and transient reasoning summaries.
- Keep invocation stateless with `store:false`; document the current SDK limit
  around replaying opaque reasoning output items across tool rounds.
- Expose provider configuration through `/model-openai-responses config` with
  secret-safe API key and header updates.
