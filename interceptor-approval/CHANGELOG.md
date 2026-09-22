# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Apply approval policy updates to the running interceptor immediately and
  return `restart_required:false`.
- Expose configuration as `/interceptor-approval config` using a stable plugin Group and local operation Name.
- Populate approval-rule forms with the saved rule list.
- Normalize rule order and reject stale configuration writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `interceptor-approval` from the Ingot Core repository into the
  official plugins repository. This migration does not intentionally change
  plugin behavior.
