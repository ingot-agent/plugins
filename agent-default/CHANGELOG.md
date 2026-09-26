# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Enable three built-in child types (`coder`, `explorer`, `reviewer`) when no
  `subagents.toml` exists and the composed tool runtime includes
  `submit_agent_result`; filter their tool allowlists by installed tools.
  An explicit config file still overrides or disables the defaults.
- Add the `session-tree` component for configured single-Turn child Sessions,
  persistent state recovery, ancestry authorization, queueing, cancellation,
  interruption, and bounded settlement.
- Dispatch accepted child tasks through the existing Agent loop and contribute
  each frozen child definition's system prompt.

### Changed

- Apply successful configuration updates to the running Agent immediately and
  return `restart_required:false`.
- Tell parent agents to replace, rather than resume, child Sessions interrupted
  by a runtime restart and to treat unknown external-writer state conservatively.
- Restrict every child execution to its configured tool set, hide
  `submit_agent_result` from root Sessions, and require a durable standalone
  submission before a child can complete.
- Allow a valid result submission on the final model round while rejecting
  mixed submissions and unconfigured tool calls before dispatch.
- Consume provider entries with required Complete and optional Stream callbacks;
  remove dependency on the former provider execution interfaces.
- Discover provider choices through live `model.ProviderSource` snapshots,
  validate selections at submission, and allow repairing removed providers.
- Expose configuration as `/agent-default config` using a stable plugin Group and local operation Name.
- Present the available providers as a closed choice and configure optional
  numeric overrides through explicit inherit or override actions.
- Reject stale configuration writes and revalidate provider selections at the
  commit boundary.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `agent-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
