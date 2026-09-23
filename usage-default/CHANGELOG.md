# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Use `unicode-estimate-v1` for every resolved provider and model without a
  route table. Existing routes remain readable but are ignored and removed
  when configuration is saved.
- Skip non-text content parts when estimating input tokens, while continuing
  to count text and message framing in multimodal requests.
- Expose only cache size in `/usage-default config`; apply changes immediately,
  reject stale writes, and return `restart_required:false`.
- Use `unicode-estimate-v1` for every resolved provider and model without a
  route table. Existing routes remain readable but are ignored and removed
  when configuration is saved.
- Skip non-text content parts when estimating input tokens, while continuing
  to count text and message framing in multimodal requests.
- Expose only cache size in `/usage-default config`; reject stale writes and
  report restart requirements against the running configuration.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `usage-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
