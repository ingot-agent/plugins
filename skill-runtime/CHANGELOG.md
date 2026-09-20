# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Discover Agent Skills from plugin-owned runtime state without restarting.
- Carry a read-only built-in `skill-create` Skill that guides model-authored
  Skill creation without installing another state file.
- Expose `read_skill` and create-only `add_skill` tools.
- Expose the Skill catalog through a prompt contributor and runtime status
  through `/skill-runtime status`.
