# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Require `usage.Counter` and replace byte watermarks with input-token budgets.
  Legacy byte/turn/anchor configuration requires explicit migration.
- Compact complete rounds, including the first round and completed rounds in
  the current turn; recent rounds are a soft retention preference.
- Keep frozen summary/state-delta segments and use token watermarks for rollup.
  Rollup may discard selected obsolete facts without rewriting retained values.
- Bound summary inputs with ordered UTF-8 fragment extraction and merging, sharing
  one model-call budget across all stages. Persist only complete round coverage.
- Write v2 checkpoints; ignore known v1 chains while preserving sequence order.
- Consume provider entries with required Complete and optional Stream callbacks;
  remove dependency on the former provider execution interfaces.
- Discover provider choices through live `model.ProviderSource` snapshots,
  validate selections at submission, and allow repairing removed providers.
- Expose configuration as `/context-compact config` using a stable plugin Group and local operation Name.
- Validate setup with the same normalization used at construction and present
  the available providers as a closed choice.
- Reject stale configuration writes and report restart requirements against
  the running configuration.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `context-compact` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
