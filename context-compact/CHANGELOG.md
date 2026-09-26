# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Default compaction to 800k input tokens with a 250k target for a nominal
  1M-token context; increase summary chunk/input budgets to 128k/256k so the
  default call limit can cover larger histories, and show effective defaults
  in the configuration form.
- Make `usage.Counter` optional and estimate character-based input tokens when absent; optionally resolve provider/model defaults through `model.RequestResolver`.
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
