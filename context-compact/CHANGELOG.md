# Changelog

All notable changes to this plugin are documented in this file.

## 0.2.0 - 2026-11-24

### Build

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

**Rewrite.** Compaction now has a deterministic stage that always succeeds and a
summarizing stage that may fail without affecting it. The previous checkpoint
chain, JSON summary protocol and rollup machinery are removed. See the
[design document](../docs/context-compact-v2-design.md).

- Replace checkpoint chains with one summary record per session: running
  summary, optional notes, covered message count and the digest of those
  messages. The newest record whose digest still matches history is used; a
  record with a stale digest is ignored instead of failing. Configuration is no
  longer part of any fingerprint, so changing a threshold never invalidates
  stored work.
- Reduce turns in four monotone steps, oldest first: tool output, then tool call
  arguments, then the whole tool exchange, and only then removal. Every step
  below removal preserves all user and assistant text verbatim. Removal happens
  only after every remaining turn is already fully elided, so a summary is
  produced exactly for turns whose narrative is gone.
- Eliminate the error path for summarization. Empty answers, provider errors,
  timeouts, truncated answers, tool-calling answers and invalid UTF-8 all
  degrade to "drop without summary" and never fail the turn. `Compact` returns
  an error only for a structurally invalid history.
- Summarize prose instead of JSON. The previous strict decode (`EOF`,
  `invalid character '{'`, `did not reach ... targets`,
  `discards nonexistent path ...`) turned ordinary model imperfection into a
  failed turn.
- Fix rounds to run from one user message to the next instead of ending when the
  assistant stops requesting tools. An interrupted turn ends without a final
  assistant message, and the old rule excluded it from every round: 265
  messages and 41,208 tokens of one real session vanished from the compacted
  request with no error. A regression test now asserts that rounds plus the
  in-progress tail account for every message.
- Keep the in-progress turn as an untouched tail. It ends with a user message or
  with unmatched tool calls, and dropping it would remove the work the model is
  continuing.
- Protect recent work by token budget (`protect_recent_tokens`) instead of by
  round count (`recent_rounds`). In one measured session the newest four turns
  totalled 28 tokens, so the count protected nothing.
- Advance coverage only over turns a summary actually describes.
- Give every removed turn space in the summary request instead of skipping the
  ones that do not fit. The budget is divided fairly among the turns still to be
  written and an oversized turn receives a proportional excerpt, so a removed
  turn is never left out of the summary that replaces it.
- Keep a session compact once it has a summary, so the request cannot balloon
  back to full size when it dips below the trigger.
- Re-read `config.toml` whenever its modification time or size changes. A broken
  edit keeps the previous configuration and logs a warning, so a typo cannot
  disable compaction. The previous version read the file only during
  construction, which made every tuning attempt appear to be ignored until the
  runtime restarted.
- Fix summary reasoning effort to `none`, with one retry using the provider
  default when the provider rejects it. Inheriting a runtime default of `max`
  made reasoning models consume the entire output budget and return no text.
- Reduce configuration from 17 fields to 6: `provider`, `model`,
  `trigger_tokens`, `target_tokens`, `protect_recent_tokens`,
  `summary_max_tokens`. Removed: `summary_chunk_tokens`,
  `summary_input_tokens`, `rollup_max_tokens`, `summary_max_bytes` (now a
  constant), `memory_*_tokens`, `state_*_tokens`, `max_summary_passes`,
  `allowed_accuracies`, `recent_rounds`, `token_count_cache_entries`. Old files
  with those keys are rejected with a decode error rather than silently
  reinterpreted.
- Keep the estimator identity `unicode-estimate-v1` byte-for-byte, so existing
  watermarks keep their meaning.
- Add `internal/engine`, a deterministic shrinking core with no model calls,
  persistence or I/O, covered by property tests over random histories and by
  regressions on a real 3.9 MB session. On that session it fits the 250k target
  using 30% of the original tokens with no turn removed and no model call.
- Reduce non-test code from 2735 to 1512 lines.

Requires published SDK v0.2.19 for `model.Message.Replay` and the atomic batch
Store.Append contract used by test stores; validate with `GOWORK=off`.

### Changed

- Require SDK v0.2.15 for the published Session accounting contracts and
  validate independently of local workspace replacements.

- Publish final request context estimates through optional Session-bound
  Interaction Set snapshots, including accuracy and matching Turn correlation.
- Default compaction to 800k input tokens with a 250k target for a nominal
  1M-token context; increase summary chunk/input budgets to 128k/256k so the
  default call limit can cover larger histories, and show effective defaults
  in the configuration form.
- Embed the `unicode-estimate-v1` context counter with a bounded configurable
  cache and resolved provider/model defaults; remove external Counter injection
  and the separate fallback estimate path.
- Forward root/current Session IDs to every fragment, segment and rollup call
  so ModelRuntime accounts for compaction's provider-reported usage.
- Leave summary `Stop` nil so the Responses adapter accepts compaction requests.
- Replace byte watermarks with input-token budgets. Legacy byte/turn/anchor
  configuration requires explicit migration.
- Compact complete rounds, including the first round and completed rounds in
  the current turn; recent rounds are a soft retention preference.
- Keep frozen summary/state-delta segments and use token watermarks for rollup.
  Rollup may discard selected obsolete facts without rewriting retained values.
- Bound summary inputs with ordered UTF-8 fragment extraction and merging, sharing
  one model-call budget across all stages. Persist only complete round coverage.
- Write v2 checkpoints; ignore known v1 chains while preserving sequence order.
- Apply compaction policy updates to the running compactor immediately and
  return `restart_required:false`.
- Consume provider entries with required Complete and optional Stream callbacks;
  remove dependency on the former provider execution interfaces.
- Discover provider choices through live `model.ProviderSource` snapshots,
  validate selections at submission, and allow repairing removed providers.
- Expose configuration as `/context-compact config` using a stable plugin Group and local operation Name.
- Validate setup with the same normalization used at construction and present
  the available providers as a closed choice.
- Reject stale configuration writes and revalidate provider selections at the
  commit boundary.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `context-compact` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
