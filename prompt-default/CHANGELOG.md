# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Apply prompt content and limit updates to the running renderer immediately
  and return `restart_required:false`.
- Expose configuration as `/prompt-default config` using a stable plugin Group and local operation Name.
- Apply construction validation and effective defaults before commit, and
  reject stale writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `prompt-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
