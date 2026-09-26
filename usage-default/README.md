# usage-default

`usage.default` estimates input tokens for every resolved provider and model
using `unicode-estimate-v1`. See the
[plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory is `usage-default`; [the manifest](ingot.plugin.toml) names
`usage.default`, declares component `default` in `.`, and supports Ingot
`>=0.3.0 <0.4.0`. `New(ctx, deps)` requires `Resolver model.RequestResolver` and
`State state.Scope`. Exports are `Counter usage.Counter` and
`Operations []operation.Operation`.

## Configuration

The plugin's optional `config.toml` lives in its assigned Runtime state scope,
not in the build recipe `plugins.toml`:

```toml
cache_entries = 1024
```

Zero or a missing file selects the default capacity of 1024; negative values
fail startup. Existing `routes` in saved configuration are accepted but ignored.
The `/usage-default config` operation manages only `cache_entries`. It clears
the completed-result cache, applies the new capacity immediately, returns
`{"restart_required":false}`, and rejects concurrent persisted edits with
`ErrConfigConflict`. Saving also removes any legacy routes from the state file.

## Estimation

`CountInput` clones and validates the invocation, calls the resolver to fill in
provider/model defaults, and reports the resolved provider and model alongside
`InputTokens`, `Accuracy: estimate`, and `Source: unicode-estimate-v1`. It does
not invoke a model endpoint or run model interceptors.

The estimator counts ASCII text at approximately four bytes per token and each
non-ASCII rune as one token. It includes role/name/call identifiers, tool
arguments, and tool schemas with fixed chat framing allowances. It is not a
model tokenizer or a guaranteed upper bound. Image, audio, video, and file
parts are skipped; text in the same message and its framing are still counted.
Invalid content structure remains an error. This can underestimate multimodal
requests because media token costs are not included.

Results estimate input only; they do not replace provider-reported `model.Usage`,
count generated output, or calculate price.

## Cache and checks

Successful results are stored in a bounded in-memory LRU cache. The key covers
the profile source and resolved request after removing non-text parts, so media
bytes are not hashed. Concurrent requests for the same key share one
computation; a waiting caller can cancel its wait. Failed counts are not
cached. Cleanup closes the counter and clears its cache; later counting returns
`ErrClosed`.

See [usagedefault.go](usagedefault.go), [count.go](count.go), and
[profile.go](profile.go). Run `go test -race ./...` in this module for local
checks. See [CONTRIBUTING](../CONTRIBUTING.md).
