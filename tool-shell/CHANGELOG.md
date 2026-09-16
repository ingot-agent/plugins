# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Expose configuration as `/tool-shell config` using a stable plugin Group and local operation Name.
- Distinguish inherit-all, inherit-none, and selected environment inheritance,
  preserving nil and explicit empty configuration states.
- Preserve hidden environment values through stable source identities and
  explicit keep, replace, or clear actions, and normalize order for activation
  comparisons.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `tool-shell` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
