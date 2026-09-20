# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Store namespaced Session metadata in schema version 3 with JSON indexes for
  child kind, parent, root, and state lookups.
- Implement atomic child creation, paged child reads, conditional metadata
  updates, recursive cancel or interrupt operations, and startup recovery.

### Changed

- Attach a stable `runtime_restarted` diagnostic when startup recovery
  interrupts resumeless queued or working child Sessions without overwriting
  an existing diagnostic.
- Generate depth-prefixed `cN_` Session IDs, keep child Sessions out of the
  normal root list, and clear agent metadata when forking.
- Maintain each parent's direct child ID list transactionally and prevent
  deletion of nodes that still have descendants.

## 0.1.0 - 2026-09-13

### Changed

- Migrated `session-sqlite` from the Ingot Core repository into the official
  plugins repository. This migration does not intentionally change plugin
  behavior.
