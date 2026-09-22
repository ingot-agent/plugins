# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Dispatch through stable target interceptors so hook additions, removals,
  reordering, and target changes apply without restart.
- Expose configuration as `/interceptor-script config` using a stable plugin Group and local operation Name.
- Populate hook forms with saved hooks, arguments, and execution limits.
- Preserve hidden environment values and renamed hooks through stable source
  identities and explicit keep, replace, or clear actions.
- Validate parent-dependent environment sources without publishing an invalid
  closed union, and reject stale configuration writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `interceptor-script` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
