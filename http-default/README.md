# http-default

`http.default` provides a shared HTTP client with a plugin-owned connection pool
and proxy policy. See the [plugin documentation index](../docs/README.md).

## Identity and capabilities

[The manifest](ingot.plugin.toml) names the plugin `http.default`, declares
component `default` in `.`, and supports Ingot `>=0.3.0 <0.4.0`.
The module directory is `http-default`. [New](httpdefault.go) takes `ctx` and
`Dependencies{State state.Scope}` and exports `Client httpx.Client` plus
`Operations []operation.Operation`.

## Configuration

The following is the plugin's `config.toml` within its assigned state scope,
not a section of the build recipe `plugins.toml`:

```toml
proxy_mode = "environment"
proxy_url = ""
max_idle_conns = 100
max_idle_conns_per_host = 10
idle_conn_timeout_seconds = 90
tls_handshake_timeout_seconds = 10
```

| Field | Default | Validation and behavior |
| --- | --- | --- |
| `proxy_mode` | `environment` | `environment`, `direct`, or `url`; empty also selects `environment` |
| `proxy_url` | empty | Required only with `url`; must be empty in other modes |
| `max_idle_conns` | 100 | Total idle connection limit |
| `max_idle_conns_per_host` | 10 | Idle connections per host |
| `idle_conn_timeout_seconds` | 90 | Idle connection lifetime |
| `tls_handshake_timeout_seconds` | 10 | TLS handshake timeout |

Numeric zero selects the default; negative values are invalid. `environment`
uses Go's `http.ProxyFromEnvironment`, including its environment-variable and
loopback behavior. `direct` disables proxy lookup. `url` requires an absolute
HTTP or HTTPS proxy URL without query or fragment; URL credentials are supported
by the underlying Go proxy transport. SOCKS URLs are not accepted by this config
validator.

Missing configuration is valid and uses defaults. Malformed TOML and unknown
fields fail startup. Direct file edits take effect on the next construction.

## Live configuration operation

Operation `config`, group `http-default` (`/http-default config` in the bundled
host), accepts `{}` with no additional input properties. It collects transport
settings using structured interaction. When retaining an existing explicit proxy,
the user selects `keep` or `replace`; replacement asks for a sensitive `proxy_url`
without displaying the old value. Selecting another proxy mode clears that URL.

After validation, [setup.go](setup.go) saves the configuration and swaps in a new
client/transport. New requests use the replacement; requests that already captured
the old client continue on it. Old idle connections are closed. The result is
`{"restart_required":false}`. An intervening persisted change returns
`ErrConfigConflict` instead of overwriting it.

## Request behavior and limits

`Do(ctx, request)` clones the HTTP request with the supplied context. Callers own
the response body and must close it. A nil context or request returns
`ErrInvalidRequest`. Transport failures are wrapped while preserving their causes.

The transport attempts HTTP/2, uses a 30-second dial timeout and keepalive, and a
1-second `Expect: 100-continue` timeout. There is no overall `http.Client.Timeout`;
callers must supply request deadlines when needed. This component does not add
application retries, response-size limits, authentication, or TLS verification
bypasses. Provider-specific request policy belongs to the provider plugins.
Cleanup closes idle connections.

## Source and checks

The implementation is [httpdefault.go](httpdefault.go), with persistent decoding
and atomic replacement in [config.go](config.go). Run `go test ./...` in this
module; [httpdefault_test.go](httpdefault_test.go),
[setup_internal_test.go](setup_internal_test.go), and
[m1_state_test.go](m1_state_test.go) cover proxy validation, requests, configuration,
and state. See [CONTRIBUTING](../CONTRIBUTING.md) for the workspace.
