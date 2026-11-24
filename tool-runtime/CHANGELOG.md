# Changelog

All notable changes to this plugin are documented in this file.

## 0.2.0 - 2026-11-24

### Build

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

### Changed

- Default tool text output to 64 KiB across all text parts. Oversized output
  returns a UTF-8-safe head and tail plus a truncation notice after all tool
  interceptors complete, instead of failing the invocation. The configured
  limit includes the notice and must be large enough to contain it. Inline
  media limits remain errors; discarded text is not saved automatically.
- Expose configuration as `/tool-runtime config` using a stable plugin Group and local operation Name.
- Reject non-positive limits during setup and prevent stale writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `tool-runtime` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
