# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Expose configuration as `/context-compact config` using a stable plugin Group and local operation Name.
- Validate setup with the same normalization used at construction and present
  the available providers as a closed choice.
- Reject stale configuration writes and report restart requirements against
  the running configuration.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `context-compact` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
