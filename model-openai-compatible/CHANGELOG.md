# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Replace static provider exports with a live `model.ProviderSource`; saved
  provider configuration applies to subsequent calls without restarting,
  while existing calls retain their original provider instances.
- Expose provider invocation callbacks directly in `model.ProviderEntry`,
  removing the old provider interfaces and streaming type assertions.
- Expose configuration as `/model-openai-compatible config` using a stable plugin Group and local operation Name.
- Populate provider and model lists from saved configuration without exposing API keys.
- Cover headers and all provider resource limits, and manage API keys and
  sensitive header values through explicit keep, replace, or clear actions.
- Preserve hidden values across provider renames, apply full construction
  validation, and reject stale configuration writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `model-openai-compatible` from the Ingot Core repository into the
  official plugins repository. This migration does not intentionally change
  plugin behavior.
