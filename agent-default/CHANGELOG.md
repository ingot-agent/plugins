# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Decode versioned `agent.plugin_input` records from Session storage and project
  escaped, source-tagged user messages. Defer inputs inside tool rounds until
  their results are complete, and rebuild history after interrupted recovery.
  Appends do not change the current Turn snapshot or define business priority.
  Requires the unreleased SDK PluginInput API during branch development.
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

- Require SDK v0.2.15 for the published Session accounting contracts and
  validate independently of local workspace replacements.

- Forward root/current Session IDs through model and compaction calls. Nested
  children inherit the topmost token root independently of lifecycle ownership.
- Remove Turn accounting and per-model aggregation; retain execution status,
  duration and failure details.
- Delegate model selection to model-runtime; remove Agent provider/model/effort
  settings and direct provider-directory dependencies. Retired configuration
  keys are ignored on load and removed on save.
- Capture model-runtime's effective selection through the optional request
  resolver once per turn, preserving it across complete and streaming rounds.

- Apply successful configuration updates to the running Agent immediately and
  return `restart_required:false`.
- Tell parent agents to replace, rather than resume, child Sessions interrupted
  by a runtime restart and to treat unknown external-writer state conservatively.
- Restrict every child execution to its configured tool set, hide
  `submit_agent_result` from root Sessions, and require a durable standalone
  submission before a child can complete.
- Allow a valid result submission on the final model round while rejecting
  mixed submissions and unconfigured tool calls before dispatch.
- Expose configuration as `/agent-default config` using a stable plugin Group and local operation Name.
- Configure optional numeric overrides through explicit inherit or override actions.
- Reject stale configuration writes at the commit boundary.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `agent-default` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
