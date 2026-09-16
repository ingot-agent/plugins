# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Expose configuration as `/model-runtime config` using a stable plugin Group and local operation Name.
- Allow an unconfigured multi-provider runtime to expose setup, and constrain
  the configured provider to the injected provider set.
- Reject stale configuration writes and report restart requirements against
  the running configuration.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `model-runtime` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
