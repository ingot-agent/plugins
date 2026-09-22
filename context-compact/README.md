# context-compact

`context.compact` incrementally summarizes eligible middle turns of a conversation
while retaining original session entries. It uses canonical request bytes as its
size metric. See the [plugin documentation index](../docs/README.md).

## Identity and capabilities

The directory is `context-compact`; [the manifest](ingot.plugin.toml) names
`context.compact`, declares component `default` in `.`, and supports Ingot
`>=0.3.0 <0.4.0`. `New(ctx, deps)` requires `Model model.Runtime`,
`Store session.Store`, and `State state.Scope`, and accepts
`ProviderSources []model.ProviderSource` for live configuration choices.
It exports `Compactor contextwindow.Compactor` and `Operations []operation.Operation`.
Wiring the compactor into `agent.default` makes it run before model requests.

## Configuration

The plugin reads its own `config.toml` within `state.Scope.Dir()`. These settings
are runtime state, not build recipe fields in `plugins.toml`.

```toml
provider = ""
model = ""
trigger_request_bytes = 524288
target_request_bytes = 262144
anchor_turns = 2
recent_turns = 4
summary_chunk_bytes = 65536
summary_max_tokens = 1024
summary_max_bytes = 65536
max_summary_chunks = 8
max_summary_passes = 8
```

| Field | Default | Meaning |
| --- | --- | --- |
| `provider`, `model` | empty | Independently inherit the request's provider/model; any remaining empty selection is resolved by `model.runtime` |
| `trigger_request_bytes` | 512 KiB | Start additional summarization at or above this size |
| `target_request_bytes` | 256 KiB | Stop when compacted request is at or below this size |
| `anchor_turns` | 2 | Preserve initial user-started turns |
| `recent_turns` | 4 | Preserve the last user-started turns, including the active turn |
| `summary_chunk_bytes` | 64 KiB | Desired minimum source chunk size, rounded to complete eligible turns |
| `summary_max_tokens` | 1024 | Output-token limit passed to each summarizer call |
| `summary_max_bytes` | 64 KiB | Maximum UTF-8 bytes in the returned narrative summary |
| `max_summary_chunks` | 8 | Active segment threshold at which existing summaries are rolled up |
| `max_summary_passes` | 8 | Maximum summarizer calls in one compaction, including rollups |

All numeric zero values select defaults; this also means `anchor_turns = 0` or
`recent_turns = 0` cannot disable preservation. Negative values are invalid. The
effective target must be smaller than the trigger. Missing configuration uses
these defaults: there is no separate enable flag, so a wired compactor can trigger
without a saved file. Unknown TOML fields and malformed files fail construction.

Operation `config`, group `context-compact` (`/context-compact config`), accepts
`{}` without extra fields. It collects the above settings, checks the current
provider choice, persists and applies a new configuration, and returns
`{"restart_required":false}`. Each compaction holds its own configuration
snapshot. Conflicting persisted edits return `ErrConfigConflict`; direct file
edits are read at construction.

## Compaction contract

The compactor validates message/tool-call ordering and serializes work per
session. It preserves leading system messages and configured anchor/recent turns;
the recent window includes the active incomplete turn. It summarizes only complete
eligible middle turns.
A turn containing non-text media is a barrier to advancing that summary segment;
the implementation does not silently discard images, audio, video, or files.

The size calculation covers the canonical projected model request, including
messages, tool definitions, and generation fields. It is neither an exact
provider wire-body size nor a token count. `usage.default` is not a dependency,
and this component does not implement token/context-window routing.

The summarizer runs through `model.Runtime.Complete` with temperature `0`, no tool
definitions, the configured output-token limit, and a strict JSON response
protocol. Segment responses contain `summary` and an `operations` array of
`set`/`delete` updates on canonical JSON-pointer paths. Rollup responses merge
summaries and must return an empty operations array. Empty, malformed, oversized,
non-text, or tool-calling summaries fail instead of being accepted as history.

Successful reduction appends version-1 `context.compact.checkpoint` entries to
the session store. Checkpoints contain source and policy digests, sequence and
parent sequence, covered message count, summary, state revision/operations (or
rollup snapshot), and summarizer provider/model. They are reused only when their
chain, source, and policy remain compatible. Original `agent.message` entries
remain available to history readers and session forks. A later pass can fail
after earlier valid checkpoints have already been appended.

If no eligible turn remains, media blocks progress, a summary does not reduce
size, or the pass limit is reached, the call returns `ErrContextUncompactable`.
The agent surfaces this failure; there is no silent truncation fallback.

The current summarizer sets a non-nil empty `Stop` slice. The
[Responses adapter](../model-openai-responses/README.md) rejects any non-nil
`Stop`, so it cannot currently serve as this compactor's summarizer without an
explicit compatible request transformation. Configure a Chat Completions provider
for compaction when the main model uses Responses.

## Source and checks

See [contextcompact.go](contextcompact.go), [compact.go](compact.go),
[messages.go](messages.go), [checkpoint.go](checkpoint.go), and
[protocol.go](protocol.go). Run `go test ./...` in this module; existing tests in
[contextcompact_test.go](contextcompact_test.go), [setup_test.go](setup_test.go),
and [providers_test.go](providers_test.go) exercise checkpoints, bounds, provider
choices, and live configuration. See [CONTRIBUTING](../CONTRIBUTING.md).
