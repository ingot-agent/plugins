# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Apply storage limits and I/O concurrency updates to the running store and
  return `restart_required:false`.
- Expose configuration as `/asset-local config` using a stable plugin Group and local operation Name.
- Reject stale or canceled configuration commits and preserve precise result
  schemas for effective limits.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `asset-local` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
