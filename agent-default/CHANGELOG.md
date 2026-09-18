# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Changed

- Consume provider entries with required Complete and optional Stream callbacks;
  remove dependency on the former provider execution interfaces.
- Discover provider choices through live `model.ProviderSource` snapshots,
  validate selections at submission, and allow repairing removed providers.
- Expose configuration as `/agent-default config` using a stable plugin Group and local operation Name.
- Present the available providers as a closed choice and configure optional
  numeric overrides through explicit inherit or override actions.
- Reject stale configuration writes and report restart requirements against
  the running configuration.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `agent-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
