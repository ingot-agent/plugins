# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Add an independent OpenAI Responses API model provider with complete and
  typed SSE streaming support.
- Map SDK messages, images, function calls, function outputs, tools, usage,
  incomplete responses, and transient reasoning summaries.
- Keep invocation stateless with `store:false`; document the current SDK limit
  around replaying opaque reasoning output items across tool rounds.
- Expose provider configuration through `/model-openai-responses config` with
  secret-safe API key and header updates.
