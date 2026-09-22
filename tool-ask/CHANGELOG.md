# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Apply ask limits to the running tool immediately and return
  `restart_required:false`.
- Expose configuration as `/tool-ask config` using a stable plugin Group and local operation Name.
- Reject non-positive limits during setup and prevent stale writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `tool-ask` from the Ingot Core repository into the official plugins
  repository. This migration does not intentionally change plugin behavior.
