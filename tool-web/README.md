# tool-web

`tool.web` exposes a model-visible `web_search` tool and `web_fetch` tool backed
by the [DeepSeek Search and Fetch APIs](https://api-docs.deepseek.com/guides/search).
It also provides a live `config` Operation for the API key, endpoint, timeout
and response-size limit. See the [plugin documentation index](../docs/README.md).

## Identity and capabilities

[The manifest](ingot.plugin.toml) names the plugin `tool.web`, declares
component `default` in `.`, and supports Ingot `>=0.3.0 <0.4.0`. The module
directory is `tool-web`; the module path is
`github.com/ingot-agent/plugins/tool-web`.

[New](toolweb.go) takes `Dependencies{HTTP httpx.Client, State state.Scope}` and
exports:

- `Tools []tool.Tool`: `web_search` and `web_fetch`.
- `Operations []operation.Operation`: `config`, group `tool-web`.

`HTTP` is required and is normally provided by
[http-default](../http-default/README.md). `State` is the runtime-injected
plugin state scope. Construction reads its persisted configuration and the
environment fallback; it starts no background workers and creates no external
resources. Cleanup is `nil`.

## Configuration

The Plugin owns this file inside its assigned state scope, normally
`<runtime-home>/state/tool.web/config.toml`:

```toml
api_key = ""
base_url = "https://api.deepseek.com"
request_timeout_seconds = 60
max_response_bytes = 8388608
```

| Field | Default | Validation and behavior |
| --- | --- | --- |
| `api_key` | empty | Stored DeepSeek API key. Empty selects the `DEEPSEEK_API_KEY` environment fallback. Leading and trailing whitespace is removed; control characters are rejected. |
| `base_url` | `https://api.deepseek.com` | Absolute `http`/`https` URL with a host, no userinfo, query or fragment. The plugin appends `/v1/tools/search` and `/v1/tools/fetch`. A `/v1` or `/v1/tools` suffix is accepted; a full endpoint URL is rejected. |
| `request_timeout_seconds` | `60` | Total time budget for one tool call, including retries. `5` to `300`. |
| `max_response_bytes` | `8388608` (8 MiB) | Maximum DeepSeek response body. `65536` (64 KiB) to `67108864` (64 MiB). This bounds allocation, not the tool output shown to the model. |

The effective API key is the non-empty `api_key`, otherwise a non-empty
`DEEPSEEK_API_KEY`. The environment key is read when the plugin is constructed
or configuration is applied; changing the process environment normally requires
a Runtime restart. The configuration Operation can replace or clear the stored
key live. A cleared stored key falls back to the environment key.

A missing `config.toml` is valid and leaves the plugin unconfigured unless
`DEEPSEEK_API_KEY` is set. Malformed TOML and unknown fields fail construction.
The file contains the API key and is written with mode `0600`; the plugin creates
its state directory with mode `0700` when needed.

### Live configuration Operation

Operation `config`, group `tool-web` (the bundled host renders it as
`/tool-web config`), accepts `{}` with no input properties. It collects settings
through structured Interaction:

- API key action: `keep`, `replace`, or `clear` / use the environment. Replacing
  asks for the new key in a separate sensitive field, which the host must not
  echo back as a default.
- API base URL.
- Request timeout and maximum response bytes.

After validation, [setup.go](setup.go) compares the persisted file with the
snapshot read before Interaction, saves atomically, and publishes a new
immutable configuration snapshot. Tool calls started afterwards use the new
settings; calls already in flight keep their snapshot. The result is:

```json
{"configured":true,"api_key_source":"config","restart_required":false}
```

`api_key_source` is `config`, `environment`, or `none`. An intervening persisted
change returns `ErrConfigConflict` instead of overwriting it.

## Tools

A typical flow is `web_search` first, then `web_fetch` for the most relevant
result URL:

```text
web_search {"query":"DeepSeek search API","count":5}
web_fetch  {"url":"<a selected result URL>"}
```

`need_content=true` can inline content during Search, but Fetch gives the model
control over which page to read and returns the full extracted page for one URL.

### `web_search`

Calls `POST <base>/v1/tools/search`.

| Argument | Type | Default | Description |
| --- | --- | --- | --- |
| `query` | string | required | 1 to 128 Unicode characters, not all whitespace. Supports `site:`, `-site:`, `-term`, quoted phrases, `lang:`, `after:` and `before:` operators. |
| `count` | integer | `10` | 1 to 50 results. Billing is per successful request, not per result. |
| `ori_question` | string | omitted | Optional original user question, 1 to 128 characters, to improve query understanding. Blank is treated as omitted. |
| `need_content` | boolean | `false` | Ask DeepSeek to include page content. Content is truncated per result; prefer `web_fetch` for a selected page. |

The formatted result preserves DeepSeek's ranking and includes title, URL,
site name, publication date, authority tier, relevance score and the
model-oriented summary (or snippet when the summary is empty). An empty result
array is a normal successful result, not an error.

Argument JSON rejects unknown fields. The tool also validates the query and
count after schema validation so direct callers get the same behavior.

### `web_fetch`

Calls `POST <base>/v1/tools/fetch`.

| Argument | Type | Description |
| --- | --- | --- |
| `url` | string | Required absolute HTTP or HTTPS URL with a host, at most 8192 characters, without username or password. |

The result contains the page title (when available), the URL and the extracted
Markdown content. Empty extracted content, security-policy rejection, missing
pages, paywalls and unsupported content types are reported as unsuccessful
business results so the model can choose another source. They do not crash the
tool.

Both tools tell the model to treat returned web text as untrusted data. Prompt
injection inside a page is not neutralized by this plugin.

## Request behavior and limits

- Authentication is `Authorization: Bearer <key>`, with
  `Content-Type: application/json`.
- DeepSeek HTTP `408`, `429` and `5xx` responses and transport failures are
  retried twice (three attempts total). `Retry-After` is honored up to 10
  seconds; otherwise the delay is 500 ms and then 1 second. Other `4xx`
  responses are not retried.
- `401` reports missing or invalid credentials; `402` reports insufficient
  balance; `403` reports a Fetch security-policy rejection; `404` on Fetch
  reports unavailable or unextractable content; `429` reports the account rate
  limit after retries.
- Each call is bounded by `request_timeout_seconds`. A parent context deadline
  still wins.
- Successful responses are read up to `max_response_bytes`; larger bodies are
  reported as a business result. The search formatter keeps at most 16 KiB of
  content per result and 512 KiB of total output; Fetch content is kept to
  512 KiB. The selected [tool-runtime](../tool-runtime/README.md) may truncate
  the final tool result further (64 KiB by default).
- There is no image search, no `localization` field, and no per-domain or
  account-level rate-limit coordination beyond the retry policy. DeepSeek
  applies 100 QPS to Search and 25 QPS to Fetch per account.
- Search and Fetch are billed per successful request. Failed requests, empty
  search results and timeouts are not billed according to DeepSeek's published
  terms.

## Composition and activation

Add the module and rebuild the image:

```sh
ingot plugin add ../plugins/tool-web
ingot project resolve
ingot up -d -- web
```

The graph must include an `httpx.Client` provider. `web_search` and `web_fetch`
must remain unique tool names in the composed `tool.Runtime`; this plugin does
not rename them based on configuration.

On an unconfigured instance, both tools return a business result explaining how
to configure the key instead of failing construction, so the configuration
Operation remains reachable. API keys and request/response bodies are never
logged by this module.

## Security and privacy

- The stored API key is plaintext in the plugin state file, protected by
  filesystem permissions, not by encryption.
- `DEEPSEEK_API_KEY` is an alternative that avoids persisting the key. It is
  visible to the Runtime process environment.
- Search queries and fetched URLs are sent to DeepSeek. Page content is returned
  by DeepSeek and is untrusted.
- `base_url` can point at an HTTP endpoint or a proxy. Use HTTPS unless the
  endpoint is trusted and local.

## Source and checks

Implementation: [toolweb.go](toolweb.go), [client.go](client.go),
[format.go](format.go), [config.go](config.go) and [setup.go](setup.go).

```sh
GOWORK=off go mod tidy -diff
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
```

The tests use a fake `httpx.Client` and Interaction channel; no API key or
network access is required.
