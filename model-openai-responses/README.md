# model-openai-responses

`model-openai-responses` adapts the OpenAI Responses API to Ingot's
`model.Provider` and `model.StreamingProvider` contracts.

The plugin sends requests to `POST <base_url>/responses`. It supports:

- system, user, assistant, and tool history;
- text and user image input;
- function definitions, calls, and text function outputs;
- complete and typed SSE streaming responses;
- output text, refusals, function calls, usage, incomplete responses, and
  transient reasoning text or summaries;
- multiple named providers with model allowlists, custom headers, response
  limits, and secret-safe interactive configuration.

Requests explicitly set `store` to `false`, so the provider remains stateless
and Ingot session history stays authoritative. SDK `model.Message` does not
currently retain opaque Responses API reasoning output items. Models that
require those items to be replayed with later function outputs need a future
typed SDK contract; this plugin does not hide that limitation with mutable
provider-side conversation state.

`model.Request.Stop` and `model.Message.Name` have no lossless mapping in this
contract and are rejected instead of being ignored. Audio, video, file input,
and non-text function output are also rejected with
`content.ErrUnsupportedContent`.
