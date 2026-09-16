# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Expose configuration as `/http-default config` using a stable plugin Group and local operation Name.
- Configure proxy mode as a closed choice and manage credential-bearing proxy
  URLs through explicit keep or replace actions without exposing stored values.
- Apply construction validation before commit, reject stale writes, and report
  restart requirements against the running configuration.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `http-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
