# model-openai-compatible

`model.openai-compatible` adapts OpenAI Chat Completions-compatible HTTP endpoints
to Ingot model capabilities. It sends `POST <base_url>/chat/completions` and
supports complete responses, SSE streaming, text, user images, and function tools.
See the [plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory/module suffix is `model-openai-compatible`; the manifest name is
`model.openai-compatible`. [The manifest](ingot.plugin.toml) declares component
`default` in `.` and Ingot `>=0.3.0 <0.4.0`.

`New(ctx, deps)` requires `HTTP httpx.Client`, `Assets asset.Resolver`, and
`State state.Scope`. Even a text-only deployment must provide the asset resolver.
Exports are `Source model.ProviderSource` and `Operations []operation.Operation`.
The source yields named `model.ProviderEntry` values, each with complete and
stream callbacks. It is consumed by [model-runtime](../model-runtime/README.md).
This is a live source, not a constructor-time `[]model.Provider` export.

## Provider configuration

Providers are stored in `config.toml` inside this plugin's assigned state scope.
They do not belong in the build recipe `plugins.toml`. The following is an example
for a local compatible endpoint; choose an endpoint and model actually available
in your deployment.

```toml
[[providers]]
name = "primary"
base_url = "http://127.0.0.1:8000/v1"
api_key = ""
models = ["your-model-id"]
max_response_bytes = 16777216
max_error_body_bytes = 65536
max_asset_bytes = 20971520
asset_concurrency = 4
```

| Field within `[[providers]]` | Default | Contract |
| --- | --- | --- |
| `name` | required | Unique provider identity; at most 64 bytes; matches `[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*` |
| `base_url` | required | Absolute HTTP/HTTPS URL without userinfo, query, or fragment; include `/v1` if the server expects it |
| `api_key` | empty | Sent as `Authorization: Bearer <key>` only when nonempty |
| `organization` | empty | Optional `OpenAI-Organization` header |
| `project` | empty | Optional `OpenAI-Project` header |
| `models` | empty list | Empty permits any nonempty requested model; otherwise an exact allowlist of unique nonempty UTF-8 names |
| `default_headers` | empty table | Additional header names and values |
| `max_response_bytes` | 16 MiB | Bound on complete response body or total SSE stream bytes |
| `max_error_body_bytes` | 64 KiB | Maximum retained non-success response body |
| `max_asset_bytes` | 20 MiB | Maximum bytes for each inline or locally resolved image |
| `asset_concurrency` | 4 | Concurrent asset-read slots per configured provider |

Zero numeric values select defaults; negatives fail. The endpoint suffix is
appended after trimming trailing slashes: do not put `/chat/completions` in
`base_url`. Model allowlists do not query `/models` or select a default model.

Custom headers reject case-insensitive duplicates, invalid names/values, and
plugin-owned headers: `Authorization`, `Content-Type`, `Accept`,
`OpenAI-Organization`, `OpenAI-Project`, and `User-Agent`. Header values cannot
contain invalid control characters. The TOML shape for additional headers is
`[providers.default_headers]` following its corresponding `[[providers]]`.

Missing configuration or an empty provider list constructs an empty live source.
Malformed TOML, unknown fields, and invalid provider records fail startup. API
keys are literal stored strings; this plugin does not perform environment-variable
substitution or credential-store lookup. Treat its state file as secret material.

## Live configuration operation

Operation `config`, group `model-openai-compatible`
(`/model-openai-compatible config`), accepts `{}` with no extra input properties.
It presents a structured provider list. Each row identifies an existing provider
through `source` or selects a new one, then supplies its identity, endpoint, model
allowlist, headers, and limits. Saving requires at least one provider.

API keys use explicit `keep`, `replace`, or `clear` actions. Existing secret values
are never form defaults; replacements are sensitive fields. Header rows similarly
use an existing source and `keep`/`replace`/`clear`; removing a row deletes that
header, while `clear` stores an empty header value. Renaming a provider can retain
its existing key through the selected source.

The operation validates the entire candidate, detects intervening persisted
changes (`ErrConfigConflict`), saves it, then publishes a new source snapshot.
It returns `{"providers":N,"restart_required":false}`. Calls already holding an
old provider entry continue using that entry; subsequent model runtime calls and
configuration choices observe the new entries. Direct edits are loaded on the
next construction rather than watched automatically.

## Protocol support and limits

The adapter maps system/user/assistant/tool messages, message names, tool-call
IDs, function definitions and JSON arguments. Function arguments become JSON
strings on the wire and are restored as `json.RawMessage` on return. Generation
fields include temperature in `[0,2]`, positive `max_tokens`, and optional stop
sequences. It does not send unmodeled provider-specific options such as arbitrary
reasoning controls or tool-choice policy.

User images may be HTTP/HTTPS URIs, inline data, or asset references. Remote URIs
are passed to the provider; this plugin does not fetch them. Inline/asset images
require an `image/*` MIME type and are encoded as data URLs. Asset size is checked
before opening, then the actual bytes must match the resolver's declared size.
Local file URIs, audio, video, file parts, images outside user messages, and
non-text response content are rejected with `content.ErrUnsupportedContent`.

Complete responses must contain exactly one choice at index 0, an assistant
message, a nonempty model, and a finish reason. Streaming requests set
`stream_options.include_usage = true`; endpoints must accept that shape. The
stream must terminate with a standalone `[DONE]` event and a completed choice.
Tool-call fragments are accumulated for the final response. `reasoning_content`
or its `reasoning` alias is emitted as transient reasoning events, never appended
to canonical content or replayed history; `reasoning_content` wins if both exist.

Absent usage remains unreported. When supplied, `prompt_tokens`,
`completion_tokens`, and `total_tokens` must all be present, nonnegative, and
consistent. HTTP failures return bounded `ProviderHTTPError` data including
status, request ID, body, and truncation status; malformed successes return
`ProviderProtocolError`. The configured API key is redacted from the error paths
that explicitly sanitize it; arbitrary custom header values and response content
are not covered by a general secret-redaction facility.

There are no automatic retries or whole-request timeout settings here; the
caller context and [HTTP plugin](../http-default/README.md) govern cancellation
and transport. The response body is closed on completion, error, and cancellation.

## Source and checks

See [openaicompat.go](openaicompat.go), [protocol.go](protocol.go),
[stream.go](stream.go), [source.go](source.go), and [setup.go](setup.go).
Run `go test ./...` in this module. Existing local tests cover HTTP/SSE mapping,
media and response limits, transient reasoning, secret-preserving edits, and
snapshot replacement in [openaicompat_test.go](openaicompat_test.go),
[reasoning_test.go](reasoning_test.go),
[setup_internal_test.go](setup_internal_test.go), and
[source_internal_test.go](source_internal_test.go).
See [CONTRIBUTING](../CONTRIBUTING.md) for workspace setup.

## Transient failures

The adapter marks dispatch/response-read network interruptions and HTTP 408,
429, 500, 502, 503, and 504 as retryable for callers that support retries.
It parses `Retry-After` (seconds or HTTP-date) as a bounded delay hint. Invalid
requests, decoding errors, and cancellation are not marked. In streaming mode,
the caller must additionally check that no events were delivered before replay.
A retry may still incur duplicate provider charges.
