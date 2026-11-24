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

### Added

- Support prefix rules (`*` at the end of `rules[].tool`) so a generated tool
  set such as an MCP gateway can be approved as a group. Exact names win, then
  the longest matching prefix, then `default_action`.

### Changed

- Apply approval policy updates to the running interceptor immediately and
  return `restart_required:false`.
- Expose configuration as `/interceptor-approval config` using a stable plugin Group and local operation Name.
- Populate approval-rule forms with the saved rule list.
- Normalize rule order and reject stale configuration writes.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `interceptor-approval` from the Ingot Core repository into the
  official plugins repository. This migration does not intentionally change
  plugin behavior.
