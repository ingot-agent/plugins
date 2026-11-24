# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

### Added

- Discover Agent Skills from plugin-owned runtime state without restarting.
- Carry a read-only built-in `skill-create` Skill that guides model-authored
  Skill creation without installing another state file.
- Expose `read_skill` and create-only `add_skill` tools.
- Expose the Skill catalog through a prompt contributor and runtime status
  through `/skill-runtime status`.
