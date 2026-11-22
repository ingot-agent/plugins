# context-compact

`context.compact` keeps every model request inside a token budget. It shrinks
deterministically first and summarizes second, so a summarizer that is slow,
broken or unavailable costs recall rather than the turn. Original session
entries are never modified. Watermarks are **estimated input tokens**, not byte
counts or a guaranteed provider context-window limit. See the
[plugin documentation index](../docs/README.md) and the
[design document](../docs/context-compact-v2-design.md).

## Identity and capabilities

The directory is `context-compact`; [the manifest](ingot.plugin.toml)
names `context.compact`, with component `default` in `.`, compatible with Ingot
`>=0.3.0 <0.4.0`. `New(ctx, deps)` requires `model.Runtime`, `session.Store`
and `state.Scope`; `[]model.ProviderSource` supplies live configuration choices
and `ingotabi.Optional[interaction.ExecutionBinder]` publishes input-token
estimates. Exports are `contextwindow.Compactor` and `[]operation.Operation`.
Wiring this component into `agent.default` runs it before model requests.

Unchanged messages keep their opaque `Message.Replay` state byte-for-byte, so a
provider can still resume them. A message whose content is elided clears
`Replay`, because that state describes text that no longer appears. The local
estimator does not measure tokens hidden inside encrypted provider state.

## Context Snapshots

`context-compact.session-context/<sessionId>` is an in-memory Interaction Set
snapshot for the current Session, with `sessionId`, `inputTokens`, `accuracy`,
`source`, `provider`, and `model`. Matching Observation correlation also supplies
`turnId` and `roundIndex`. The count covers the final input request, including
tools and materialized compacted history, before model generation. It may shrink
after compaction and is not cumulative usage. Publication failures do not fail
compaction; without a binder, no snapshot is published. Reported failures go to
the injected `logging.Logger` (`Optional[logging.Logger]`); without a logging
provider they are recorded nowhere and compaction is unaffected. Runtime restart
clears the snapshot until another request is counted.

## Configuration

Settings live in this plugin's Runtime state scope at `config.toml`, not in the
build recipe. Missing files and zero numeric values select defaults:

```toml
provider = ""
model = ""
trigger_tokens = 800000
target_tokens = 250000
protect_recent_tokens = 100000
summary_max_tokens = 2048
```

`provider` and `model` independently inherit the invocation when empty. The
800k trigger and 250k target assume a *nominal* 1M-token window and leave room
for response generation and estimation error; configure smaller values for
smaller windows. `target_tokens` must be below `trigger_tokens`.

`protect_recent_tokens` is a **token budget**, not a round count: turns are
protected from the newest backwards until the budget is reached. A count would
be meaningless whenever the newest turns are short.

`summary_max_tokens` is the output budget of one summary call. It is separate
from the main model's output budget and is not a share of the context window.

Everything else the previous version exposed as a watermark is now either
derived or a fixed constant: summary input size, summary byte ceiling, elision
markers, and the reasoning effort used for summaries, which is fixed to `none`
because a summarizer that spends its output budget thinking returns no text.

`/context-compact config` edits these fields through Interaction, validates them
with the same normalization used at construction, persists atomically and
applies immediately (`restart_required:false`). Direct file edits are also
picked up without a restart (see *Configuration reload*).

## Counting

The built-in estimator keeps the historical `unicode-estimate-v1` algorithm:
ASCII text contributes about one token per four bytes (rounded up) and each
non-ASCII rune roughly one token. Message and tool framing add fixed costs.
Non-text content is skipped, so this is not a multimodal token count. The
identifier is unchanged, so configured watermarks keep the meaning they had
before the rewrite.

## Compaction contract

Compaction has two stages, and only the first is required to succeed.

**Stage one is deterministic.** The request is split into leading system
messages, complete user turns and an in-progress tail. Turns are reduced in
monotone steps, oldest first: tool output is replaced by a marker, then tool
call arguments, then the tool exchange itself. Only when nothing shallower is
left is a turn removed. Every step below removal preserves all user and
assistant text verbatim, so elision costs detail but never narration. Stage one
makes no model call, cannot fail, and is fast enough to run on every round: a
3.9 MB history shrinks to 30% of its size in about ten milliseconds.

**Stage two summarizes what stage one removed.** It is best effort. An empty
answer, a provider error, a timeout, a truncated answer or an answer in the
wrong shape costs recall, never the turn: the removed turns stay removed and
the summary is skipped. The answer is prose, not JSON — its consumer is another
model's prompt, so a structural format would add failure modes without adding
information.

The single error `Compact` returns is a structurally invalid history, which is
an upstream defect rather than a compaction outcome.

`protect_recent_tokens` is a token budget, not a round count. A tail of short
turns makes a count meaningless: in one measured session the newest four turns
totalled 28 tokens.

## Summary records

A successful summary appends one `context.compact.summary` entry carrying the
running summary, optional durable notes, the number of messages it covers, and
the digest of those messages. The newest entry whose digest still matches
current history is used; a record whose digest does not match is ignored and the
session is compacted from scratch, which is the correct response to history
having changed. There is no checkpoint chain, no revision counter and no
policy fingerprint, so changing a threshold never invalidates stored work.

Coverage advances only over turns a summary actually describes. When the request
budget cannot fit every removed turn, the rest stay uncovered and are summarized
later, so no turn is ever marked covered without appearing in a summary.

A session that has a summary stays compact even when it dips below the trigger:
otherwise recomputing elision from scratch each round would let the request
balloon back to full size and shrink again on the next round.

Original `agent.message` entries are never modified, so history, forks and
manual inspection keep every detail that elision and summarization removed.

## Configuration reload

`config.toml` is re-read whenever its modification time or size changes. A
broken edit keeps the previous configuration and logs a warning, so a typo
cannot disable compaction. There is no restart requirement, and `/context-compact config`
applies immediately.

## Source and checks

See [compact.go](compact.go), [summary.go](summary.go), [record.go](record.go),
[config.go](config.go) and the deterministic core in
[internal/engine](internal/engine). Run `GOWORK=off go test -race ./...` in this
module. Set `COMPACT_FIXTURE` to a JSON export of stored session messages to run
the real-history regressions in [internal/engine/realdata_test.go](internal/engine/realdata_test.go)
and [realdata_test.go](realdata_test.go).
