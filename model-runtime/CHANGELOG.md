# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Configure provider, model, and reasoning-effort defaults from live provider
  capability directories and validate every resolved request against them.

### Changed

- Invoke the required `Complete` and optional `Stream` functions carried by
  each provider entry, without separate provider interfaces or type assertions.
- Replace static named Provider injection with live `model.ProviderSource`
  snapshots. Default provider/model configuration now applies to subsequent
  calls without restarting; running calls retain their original snapshot.
- Refresh provider choices on each configuration interaction and allow stale
  defaults or conflicting dynamic names to be repaired after startup.
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
