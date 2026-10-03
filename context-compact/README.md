# context-compact

`context.compact` incrementally summarizes complete conversation rounds while
preserving original session entries. Its watermarks are **estimated input tokens**,
not byte counts or
a guaranteed provider context-window limit. See
the [plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory is `context-compact`; [the manifest](ingot.plugin.toml) names
`context.compact`, with component `default` in `.`, compatible with Ingot
`>=0.3.0 <0.4.0`. `New(ctx, deps)` requires `model.Runtime`, `session.Store`,
and `state.Scope`. It owns its Unicode estimator and bounded cache;
`model.RequestResolver` is optional and `[]model.ProviderSource` supplies live
configuration choices.
The optional `interaction.ExecutionBinder` publishes the final input-token
estimate as Session-scoped state after successful compaction or checkpoint reuse.
Exports are `contextwindow.Compactor` and `[]operation.Operation`. Wiring this
component into `agent.default` runs it before model requests.

## Context Snapshots

`context-compact.session-context/<sessionId>` is an in-memory Interaction Set
snapshot for the current Session, with `sessionId`, `inputTokens`, `accuracy`,
`source`, `provider`, and `model`. Matching Observation correlation also supplies
`turnId` and `roundIndex`. The count covers the final input request, including
tools and materialized compacted history, before model generation. It may shrink
after compaction and is not cumulative usage. Publication failures do not fail
compaction; without a binder, no snapshot is published. Runtime restart clears
the snapshot until another request is counted.

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
token_count_cache_entries = 1024
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
can resolve remaining defaults through the optional RequestResolver.
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

The built-in `unicode-estimate-v1` counts text, message framing, tool-call
arguments and tool-definition schemas. ASCII text contributes about one token
per four bytes (rounded up), other Unicode runes roughly one each. It preserves
the former usage plugin's effective algorithm and returns `accuracy=estimate`.
Non-text content is skipped; this is not an upper bound or a multimodal token
count. The bounded cache defaults to 1024 entries and shares concurrent work
for identical requests. Changing its capacity applies to subsequent counts.

If provider/model is not explicit, `model.RequestResolver` (normally exported
by `model.runtime`) must be wired; otherwise counting returns
`ErrInvalidRequest`. Errors do not silently select a different algorithm.
Excluding `estimate` from `allowed_accuracies` returns `ErrUnsupportedAccuracy`.
The independent `usage.default` plugin, tokenizer routes and setup operation
have been removed. Its old state files are no longer read.

Checkpoints are policy-bound: changing these watermarks, the counting source or
resolved model selection prevents reuse of an incompatible chain. Input usage
is not model output usage or a price estimate and is never persisted as actual
Session token usage.

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

`CompactionRequest` requires both `RootSessionID` and current `SessionID`.
Every fragment, segment and rollup call passes these identities unchanged to
the Runtime. Checkpoints belong to current; provider usage is settled by the
Runtime even if summary parsing later fails.

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
