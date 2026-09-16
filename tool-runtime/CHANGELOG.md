# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Expose configuration as `/tool-runtime config` using a stable plugin Group and local operation Name.
- Reject non-positive limits during setup, prevent stale writes, and report
  restart requirements against the running configuration.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `tool-runtime` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
