# model-runtime

`model.runtime` resolves model/provider selection and runs the complete and
streaming model interceptor chains. It consumes live provider sources from
adapter plugins. See the [plugin documentation index](../docs/README.md).

## Identity and capabilities

[The manifest](ingot.plugin.toml) names `model.runtime`, declares component
`default` in `.`, and supports Ingot `>=0.3.0 <0.4.0`. The directory/module suffix
is `model-runtime`. The constructor is `New(ctx, deps)`.

| Dependency field | Capability |
| --- | --- |
| `State` | `state.Scope` |
| `ProviderSources` | Ordered `[]model.ProviderSource` |
| `Interceptors` | Ordered `[]model.Interceptor` for `Complete` |
| `StreamInterceptors` | Ordered `[]model.StreamInterceptor` for `Stream` |

Exports are `Runtime model.Runtime`, `Streaming model.StreamingRuntime`,
`Resolver model.RequestResolver`, `Selection modelselection.Controller`, and
`Operations []operation.Operation`.
Collections may be empty but cannot contain nil elements. Construction does not
require a configured provider: a source may publish its first entries later.

## Defaults and live configuration

The following belongs in the plugin's own `config.toml` beneath its assigned
state scope. It is not a build-recipe `plugins.toml` table:

```toml
default_provider = "primary"
default_model = "your-model-id"
default_reasoning_effort = "" # Provider default; or an effort declared by this model
```

All three fields default to empty and must contain valid UTF-8. Missing
configuration is valid; malformed TOML and unknown fields fail startup. Request fields override
configured defaults independently. If the configured/request provider is empty
and exactly one live provider exists, that provider is selected automatically.
There is no automatic model selection from a provider's model allowlist.
`model.ReasoningEffortProviderDefault` explicitly suppresses
`default_reasoning_effort` for one request. The runtime converts it to an empty
effort before invoking the provider.

With zero providers, an unknown provider, or multiple providers and no selection,
the eventual selection fails with `model.ErrProviderNotFound`. An empty model
fails with `model.ErrModelNotFound`. An adapter may additionally enforce its own
model allowlist. A default provider removed by later provider configuration does
not silently fall back to another provider.

Operation `config`, group `model-runtime` (`/model-runtime config`), accepts `{}`
with no additional fields. It queries current sources, offers live provider
choices, and collects the default model and reasoning effort. `Automatic` is
available only when one provider exists. With no providers it returns `operation.ErrUnavailable`: configure
an adapter first. Saving rechecks providers and detects concurrent configuration
changes, persists the file, then updates the defaults with
`{"restart_required":false}`. Direct file edits require reconstruction.

## WebUI model selection

This plugin implements the public
[`app-webui/modelselection.Controller`](../app-webui/modelselection/selection.go)
contract from the published `app-webui v0.1.1` module. The contract remains owned
by WebUI: plugins can publish and implement their own capability SDKs without
adding interfaces to the official SDK. Importing this package does not require
a running WebUI component.

`Snapshot` returns the live provider/model/effort directory and the effective
runtime defaults. `Update` accepts only a declared model and a supported effort
(or `providerDefault`), checks the revision and persisted state for conflicts,
and atomically saves this plugin's `config.toml` before publishing new defaults.
It shares the configuration Operation's lock and state; updates through either
entry point invalidate stale edits through the other. The picker's
`providerDefault` is stored as an empty `default_reasoning_effort`, clearing any
previous explicit default. No Agent configuration is read or written.

Updates affect subsequent runtime calls with omitted selection fields. The
default Agent resolves and pins the selection at turn entry, so its running
turns retain their initial provider, model, and effort. Explicit request fields
remain supported for programmatic callers. With no valid default selection,
the picker still exposes the directory so configuration can be completed or
repaired. Providers with no declared models cannot be selected through the picker.

## Invocation behavior

Each `Complete`, `Stream`, and `ResolveRequest` call snapshots the provider sources
and default configuration. Source entries must have a nonempty UTF-8 name and a
`Complete` callback; names must be unique across all sources. The snapshot lasts
for that invocation, so replacing adapter configuration changes future calls
without redirecting an in-flight call. `Stream` is optional on a provider entry.

`ResolveRequest` returns an owned copy with defaults materialized and validates
the selection and basic content. It does not call providers or model interceptors,
and does not check an adapter's private model allowlist. `agent.default` uses
this capability to pin selection per turn; `usage.default` uses it to select a
counting route.

Complete and streaming interceptors are independent chains, executed in injection
order around their respective terminal. Interceptors may select another provider
within the invocation's snapshot. A missing stream callback returns
`model.ErrStreamingUnsupported`; this runtime does not itself retry as a complete
request. `agent.default` implements its own pre-output fallback.

At the provider boundary, a single complete or stream invocation can retry
transient errors explicitly marked by the provider adapter. It makes at most
three attempts (including the first) with bounded exponential jitter and honors
a bounded provider Retry-After hint. Cancellation interrupts the wait. The
selected provider snapshot and request are kept for all attempts; interceptors
run once around the invocation. Streaming retries only before the provider has
passed any event to its handler (including reasoning or part-start); after any
event, failure is returned without replay. Invalid responses, interceptor and
consumer errors, and unsupported streaming are not retried. A failed attempt
may have consumed provider resources; `agent.default` model accounting still
counts logical invocations, not underlying provider attempts. Retry-specific
observation is intentionally deferred.

Provider and interceptor responses are validated: the final role must be
assistant; provider/model identities must be present; usage counts must be
nonnegative, explicitly reported when present, and have a consistent total.
Malformed content or calls fail with `ErrInvalidResponse`. Streaming validates
part start/delta/end ordering and requires accumulated content to equal the final
response. Reasoning events use a separate transient stream and do not become
canonical response content. Consumer callback errors are retained even if an
interceptor suppresses them.

## Source and checks

See [runtime.go](runtime.go), [model_selection.go](model_selection.go),
[setup.go](setup.go), and [config.go](config.go).
Run `go test ./...` in this module. [runtime_test.go](runtime_test.go),
[stream_test.go](stream_test.go), [live_test.go](live_test.go),
[setup_internal_test.go](setup_internal_test.go), and
[model_selection_test.go](model_selection_test.go) cover selection, live snapshots,
stream validation, and setup. See [CONTRIBUTING](../CONTRIBUTING.md) for workspace
instructions.
