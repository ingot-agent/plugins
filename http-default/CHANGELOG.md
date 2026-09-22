# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Replace the running HTTP transport after a successful configuration commit
  and return `restart_required:false`.
- Expose configuration as `/http-default config` using a stable plugin Group and local operation Name.
- Configure proxy mode as a closed choice and manage credential-bearing proxy
  URLs through explicit keep or replace actions without exposing stored values.
- Apply construction validation before commit and reject stale writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `http-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
