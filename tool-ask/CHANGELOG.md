# Changelog

All notable changes to this plugin are documented in this file.

## 0.2.0 - 2026-11-24

### Build

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

### Changed

- Apply ask limits to the running tool immediately and return
  `restart_required:false`.
- Expose configuration as `/tool-ask config` using a stable plugin Group and local operation Name.
- Reject non-positive limits during setup and prevent stale writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `tool-ask` from the Ingot Core repository into the official plugins
  repository. This migration does not intentionally change plugin behavior.
