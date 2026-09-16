# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Expose configuration as `/prompt-default config` using a stable plugin Group and local operation Name.
- Apply construction validation and effective defaults before commit, reject
  stale writes, and report restart requirements against the running state.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `prompt-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
