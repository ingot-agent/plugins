# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Expose configuration as `/usage-default config` using a stable plugin Group and local operation Name.
- Populate route forms with the saved provider, model pattern, and profile mappings.
- Offer current providers as open suggestions for future-compatible routes,
  reject stale writes, and report restart requirements against the running
  configuration.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `usage-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
