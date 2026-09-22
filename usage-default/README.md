# usage-default

`usage.default` estimates model input tokens using explicitly configured
provider/model routes and local counting profiles. See the
[plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory is `usage-default`; [the manifest](ingot.plugin.toml) names
`usage.default`, declares component `default` in `.`, and supports Ingot
`>=0.3.0 <0.4.0`. `New(ctx, deps)` requires `Resolver model.RequestResolver` and
`State state.Scope`, and accepts `ProviderSources []model.ProviderSource` for
configuration suggestions. Exports are `Counter usage.Counter` and
`Operations []operation.Operation`.

## Configuration and routing

This example is the plugin's `config.toml` in its assigned state scope, not a
table of the build recipe `plugins.toml`. Replace `primary` with a configured
provider name.

```toml
cache_entries = 1024

[[routes]]
provider = "primary"
model_pattern = "deepseek-v4-flash"
profile = "deepseek-v4-flash-api-default-thinking-v1"

[[routes]]
provider = "primary"
model_pattern = ".*"
profile = "unicode-estimate-v1"
```

`cache_entries` defaults to 1024; zero selects the default and negatives fail.
Each route requires nonempty UTF-8 `provider`, `model_pattern`, and `profile`.
Patterns use Go's regular-expression syntax. Provider names match exactly, and
the regex match must span the entire model name. Routes are checked in declaration
order; the first full match wins. There are no implicit routes or profile
fallbacks. Profile names must be one of the two built-ins below.

Missing configuration or an empty route list is valid at construction. Counting
then returns `usage.ErrUnsupportedModel` until a matching route is configured.
Malformed TOML and unknown fields fail startup.

Operation `config`, group `usage-default` (`/usage-default config`), accepts `{}`
without extra properties. It collects a list of route objects and cache capacity.
Current providers are suggestions; names for future providers are allowed. The
operation requires at least one route, validates regexes and profile names,
persists and publishes the configuration, clears the completed-result cache, and
returns `{"restart_required":false}`. Each count snapshots its route table;
intervening persisted edits fail with `ErrConfigConflict`. Direct edits are read
when the plugin is constructed.

## Built-in profiles and accuracy

| Profile | Strategy | Reported accuracy |
| --- | --- | --- |
| `unicode-estimate-v1` | Text heuristic plus fixed chat/tool framing | `estimate` |
| `deepseek-v4-flash-api-default-thinking-v1` | Embedded V4 tokenizer, local prompt serialization, and 79-token hosted high-effort framing adjustment | `estimate` |

The Unicode profile counts ASCII text at approximately four bytes per token and
each non-ASCII rune as one token. It includes role/name/call identifiers, tool
arguments, and tool schemas with fixed framing allowances. It is not a model
tokenizer or guaranteed upper bound.

The DeepSeek profile targets the hosted V4 Flash API's default thinking behavior.
Its provider-owned prefix may change independently of the tokenizer, so it is
explicitly an estimate even when calibration vectors match. It is not a generic
DeepSeek profile or a promise of billing equality. The compressed tokenizer is
embedded at build time and is never downloaded during counting; provenance,
checksum, and license are documented in [assets/README.md](assets/README.md).

## Counting and cache behavior

`CountInput` clones and validates the invocation, calls the resolver to materialize
provider/model defaults, validates the resolved request, and selects a route.
It does not invoke a model endpoint or run model interceptors. All non-text media
is rejected with `usage.ErrUnsupportedModel`, including URI and asset references;
their token cost is not silently treated as zero.

Results contain `InputTokens`, `Accuracy`, `Source`, `Provider`, and `Model`.
They estimate input only; they do not replace provider-reported `model.Usage`,
count generated output, calculate price, or drive `context.compact` thresholds.

Successful results are stored in a bounded in-memory LRU cache. The key includes
the profile source and the complete resolved request. Concurrent requests for the
same key share one computation; a waiting caller can cancel its wait. Failed
counts are not cached. Cleanup closes the counter and clears its cache; later
counting can return `ErrClosed`.

## Source and checks

See [usagedefault.go](usagedefault.go), [count.go](count.go), [profile.go](profile.go),
and [deepseek_profile.go](deepseek_profile.go). Run `go test ./...` in this module
for local checks. [deepseek_live_test.go](deepseek_live_test.go) runs external
calibration only when `DEEPSEEK_API_KEY` is set; it sends requests to the provider.
The local profile vectors and route/cache behavior are covered in
[deepseek_profile_test.go](deepseek_profile_test.go) and
[usagedefault_test.go](usagedefault_test.go). See [CONTRIBUTING](../CONTRIBUTING.md).
