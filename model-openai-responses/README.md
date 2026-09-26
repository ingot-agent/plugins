# model-openai-responses

`model.openai-responses` adapts the OpenAI Responses HTTP API to Ingot's live
`model.ProviderSource` contract. It sends `POST <base_url>/responses`. See the
[plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory/module suffix is `model-openai-responses`; [the manifest](ingot.plugin.toml)
names `model.openai-responses`, declares component `default` in `.`, and supports
Ingot `>=0.3.0 <0.4.0`.

`New(ctx, deps)` requires `HTTP httpx.Client`, `Assets asset.Resolver`, and
`State state.Scope`. Exports are `Source model.ProviderSource` and
`Operations []operation.Operation`. Each named source entry provides complete
and streaming callbacks. [model-runtime](../model-runtime/README.md) snapshots
these entries when it resolves or invokes a request.

## Provider configuration

Providers live in this plugin's own `config.toml` beneath its runtime-assigned
state scope, not in the desired build recipe `plugins.toml`.

```toml
[[providers]]
name = "responses"
base_url = "https://api.openai.com/v1"
api_key = "replace-with-your-key"
models = ["your-model-id"]
max_response_bytes = 16777216
max_error_body_bytes = 65536
max_asset_bytes = 20971520
asset_concurrency = 4
```

The key and model above are placeholders; the adapter does not verify model
availability during configuration or fetch a remote model catalog.

| Field within `[[providers]]` | Default | Contract |
| --- | --- | --- |
| `name` | required | Unique name of at most 64 bytes matching `[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*` |
| `base_url` | required | Absolute HTTP/HTTPS URL without userinfo, query, or fragment |
| `api_key` | empty | Bearer token; no Authorization header when empty |
| `organization` | empty | Optional `OpenAI-Organization` header |
| `project` | empty | Optional `OpenAI-Project` header |
| `models` | empty list | Exact model allowlist; empty permits any nonempty model name |
| `default_headers` | empty table | Additional HTTP headers |
| `max_response_bytes` | 16 MiB | Complete body or total SSE stream bound |
| `max_error_body_bytes` | 64 KiB | Maximum retained non-success response body |
| `max_asset_bytes` | 20 MiB | Per-image inline/asset byte limit |
| `asset_concurrency` | 4 | Concurrent local asset reads per provider |

Zero numeric values select defaults and negative values fail. Trailing slashes
are trimmed before `/responses` is appended; `base_url` must not already contain
that endpoint suffix. Model entries must be nonempty UTF-8 and unique.

Custom header names/values must be valid and cannot duplicate another header
case-insensitively. `Authorization`, `Content-Type`, `Accept`, `User-Agent`,
`OpenAI-Organization`, and `OpenAI-Project` are owned by the plugin and cannot be
overridden. Use `[providers.default_headers]` after the relevant provider table
for additional headers. Keys are literal strings stored in the state file; there
is no environment interpolation or credential-store integration.

Missing configuration or an empty provider list is a valid empty source. Unknown
TOML fields, malformed files, and invalid provider records fail startup. The asset
resolver remains required even for text-only use.

## Live operation

Operation `config`, group `model-openai-responses`
(`/model-openai-responses config`), accepts `{}` with no extra properties. Its
structured interaction edits the complete provider list and requires at least
one provider when saving. A row selects an existing `source` or a new provider.
API-key actions are `keep`, `replace`, and `clear`; old keys are never supplied as
form defaults. Header values have the same action model: remove the header row
to delete it, or use `clear` to retain an empty value. Replacement fields are
marked sensitive. A renamed provider can retain the source provider's secrets.

Saving validates the full candidate and checks for concurrent edits, persists it,
then publishes new entries. The result is
`{"providers":N,"restart_required":false}`. Existing requests and old entry
handles continue using their captured configuration. A failed save does not
publish a candidate. Direct file changes are loaded at reconstruction.

## Request and response mapping

- The first system message maps to `instructions`; remaining system, user, and
  assistant messages map to input messages. Function calls and text tool outputs
  map to `function_call` and `function_call_output` items using the SDK call ID.
- Tool definitions map to function tools. SDK `MaxTokens` maps to
  `max_output_tokens`; temperature must be in `[0,2]`, and token limits positive.
- Requests explicitly set `store: false`. Ingot session history supplies the
  conversation; the plugin does not use provider-side mutable conversation state.
- User images accept HTTP/HTTPS URIs or inline/asset `image/*` content. Remote URIs
  are passed through, while local bytes become base64 data URLs after size checks.
- Completed response text and refusals become assistant text. Function calls are
  accumulated into the final SDK message. Missing usage stays unreported; supplied
  input/output/total counts must all be present and consistent.
- `incomplete` is a supported terminal response: `max_output_tokens` maps to finish
  reason `length`, `content_filter` remains `content_filter`, and other nonempty
  reasons are preserved. Failed/cancelled responses return `ProviderResponseError`.

Typed SSE streaming handles content, refusals, function-call argument fragments,
and reasoning text/summary deltas, then checks them against the terminal response.
A terminal event is required; unsupported event/output item types return protocol
errors. Reasoning events are transient and do not become canonical message content.

## Explicit limitations

`model.Message.Name` must be empty. `model.Request.Stop` must be nil: even a
non-nil empty slice is rejected with `ErrInvalidRequest`. Audio, video, file input,
images outside user messages, and non-text function output return
`content.ErrUnsupportedContent`. Local file URIs are not accepted.

The SDK does not retain opaque Responses reasoning output items for later replay.
Models requiring those items alongside subsequent function outputs therefore need
additional typed SDK support. Built-in Responses tools, arbitrary response-format
options, and provider-specific reasoning controls are not represented by this
adapter's request mapping.

The current [context compactor](../context-compact/README.md) explicitly creates a
non-nil empty `Stop` slice in summarizer requests. Configure a Chat Completions
provider for compaction; the Responses adapter currently rejects that request
shape unless another component explicitly transforms it.

HTTP errors retain bounded bodies and request IDs. The configured API key is
redacted in the explicitly sanitized error paths; this is not universal redaction
of custom-header secrets or provider content. There are no automatic retries or
plugin-specific overall request deadlines. Cancellation follows the caller
context and closes open response/asset bodies.

## Source and checks

See [responses.go](responses.go), [protocol.go](protocol.go), [stream.go](stream.go),
[source.go](source.go), and [setup.go](setup.go). Run `go test ./...` in this module.
[responses_test.go](responses_test.go), [source_internal_test.go](source_internal_test.go),
and [setup_internal_test.go](setup_internal_test.go) verify mapping, SSE behavior,
unsupported inputs, live snapshots, and secret-preserving setup using local test
endpoints. See [CONTRIBUTING](../CONTRIBUTING.md) for workspace instructions.

## Transient failures

The adapter marks dispatch/response-read network interruptions and HTTP 408,
429, 500, 502, 503, and 504 as retryable for callers that support retries.
It parses `Retry-After` (seconds or HTTP-date) as a bounded delay hint. Invalid
requests, decoding errors, and cancellation are not marked. In streaming mode,
the caller must additionally check that no events were delivered before replay.
A retry may still incur duplicate provider charges.
