# context-compact

`context.compact` incrementally summarizes complete conversation rounds while
preserving original session entries. Its watermarks are **input tokens** (exact,
upper-bound or estimated according to the selected counter), not byte counts or
a guaranteed provider context-window limit. See
the [plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory is `context-compact`; [the manifest](ingot.plugin.toml) names
`context.compact`, with component `default` in `.`, compatible with Ingot
`>=0.3.0 <0.4.0`. `New(ctx, deps)` requires `model.Runtime`, `session.Store`,
and `state.Scope`. `usage.Counter` and `model.RequestResolver` are optional ABI
capabilities; `[]model.ProviderSource` supplies live configuration choices.
Exports are `contextwindow.Compactor` and `[]operation.Operation`. Wiring this
component into `agent.default` runs it before model requests.

## Configuration

Settings live in this plugin's Runtime state scope at `config.toml`, not in the
build recipe. Missing files and zero numeric values select defaults:

```toml
provider = ""
model = ""
trigger_input_tokens = 800000
target_input_tokens = 250000
recent_rounds = 4
summary_chunk_tokens = 128000
summary_input_tokens = 256000
summary_max_tokens = 1024
rollup_max_tokens = 4096
summary_max_bytes = 65536
memory_trigger_tokens = 16384
memory_target_tokens = 8192
state_trigger_tokens = 4096
state_target_tokens = 2048
max_summary_passes = 8
allowed_accuracies = ["exact", "upper_bound", "estimate"]
```

The 800k trigger and 250k target assume a *nominal* 1M-token model window and
leave headroom for tool output, response generation, and estimation error. They
are not derived from provider metadata: configure smaller watermarks for models
with smaller windows. The target is for the **whole materialized input**, including
tool definitions, not just the summarized history. `recent_rounds` is a soft
preference; the compactor may consume those rounds if necessary to reach the
target. `summary_chunk_tokens` is the minimum desired size of selected complete
rounds, while `summary_input_tokens` bounds each summarizer request. These
128k/256k defaults allow large complete rounds to be summarized within the
eight-call budget; a single round larger than that input budget can still need
multiple evidence-extraction calls and may exhaust the limit. The summary
provider must also support a sufficiently large input window; use smaller
settings for models that cannot accept 256k tokens. The
memory/state trigger and target values control rollups of existing summaries.
`summary_max_bytes` is a UTF-8 output size guard; `max_summary_passes` limits
total summarizer calls including fragment extraction and rollups.

`provider`/`model` independently inherit the invocation when empty. The runtime
can resolve remaining defaults through a Counter or optional RequestResolver.
An explicit unsupported provider fails configuration validation. Negative
numeric values, unknown TOML fields, malformed config and unknown/empty
`allowed_accuracies` are rejected. Each token target must be smaller than its
trigger. Old byte-, turn- and anchor-based config fields are rejected with a
migration error. An explicitly saved value retains its meaning after changing
these built-in defaults; remove or set it to zero to adopt the new default.

`/context-compact config` accepts `{}` and uses Interaction to edit these fields.
It checks for conflicting persisted edits, saves atomically and publishes to new
calls immediately (`restart_required:false`); ongoing compactions retain their
snapshot. Direct file edits require reconstruction/restart.

## Counting and selection

When wired, `usage.Counter` counts the complete request, resolves provider/model
defaults, and supplies its accuracy (`exact`, `upper_bound`, or `estimate`). The
counter's errors and invalid results are returned rather than silently falling
back. Without a Counter, the compactor uses a built-in character estimate of
its canonical request projection: ASCII characters contribute about one token
per four characters (rounded up), other Unicode characters roughly one each.
The projection includes message and tool data but not the provider's exact wire
format. JSON escapes and inline media can distort the estimate; it is **not an
upper bound** and may undercount some languages, tokenizers or media. The
fallback returns `accuracy=estimate` with a stable source identity for
checkpoint validation. If provider/model is not explicit, a
`model.RequestResolver` (normally exported by `model.runtime`) must be wired;
otherwise counting returns `ErrInvalidCount` rather than guessing a selection.
The `allowed_accuracies` setting also applies to the fallback: excluding
`estimate` disables compaction without a more precise Counter.

Checkpoints are policy-bound: changing these watermarks, the counting source or
resolved model selection prevents reuse of an incompatible chain. Input usage
is not model output usage or a price estimate. A counter can itself report
`estimate`; this component never assumes every installed Counter is exact.

## Compaction contract

The compactor validates message/tool-call ordering and serializes work per
session. It retains leading system messages and prefers recent complete rounds;
it summarizes only complete eligible text rounds. Non-text media is a barrier to
summary coverage, not something silently dropped. If no eligible round remains,
media blocks progress, a summary does not reduce tokens, or the pass limit is
reached, it returns `ErrContextUncompactable` rather than truncating history.

Summaries run through `model.Runtime.Complete` at temperature `0`, no tools,
the configured output-token limit and **nil `Stop`** (compatible with the
Responses adapter's request validation). The protocol requires strict JSON
narrative summaries with `set`/`delete` state operations, or a rollup of old
summaries. Empty, malformed, oversized, non-text or tool-calling responses fail.
Model-specific limitations beyond `Stop` may still apply; real provider/model
summarization is not guaranteed by build-time validation.

Successful reduction appends version-2 `context.compact.checkpoint` entries,
including source and policy digests, sequence, coverage, state operations or
rollup snapshot, and summary provider/model. Checkpoints are reused only when
their chain, source and policy match. Original `agent.message` entries remain
available for history and forks. A later pass can fail after earlier valid
checkpoints were appended. Known version-1 chains are not reused.

## Source and checks

See [contextcompact.go](contextcompact.go), [compact.go](compact.go),
[count.go](count.go), [checkpoint.go](checkpoint.go) and [protocol.go](protocol.go).
Run `GOWORK=off go test -race ./...` in this module; the token, protocol,
compaction, configuration and fallback tests cover the local behavior.
