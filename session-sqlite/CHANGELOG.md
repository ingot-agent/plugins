# Changelog

All notable changes to this plugin are documented in this file.

## Unreleased

### Added

- Add schema version 4 with an independent `sessions.totaltoken` INTEGER
  column, atomic distinct-target increments, and TotalToken metadata queries.
  New sessions and forks start at zero; increments keep UpdatedAt.
- Store namespaced Session metadata in schema version 3 with JSON indexes for
  child kind, parent, root, and state lookups.
- Implement atomic child creation, paged child reads, conditional metadata
  updates, recursive cancel or interrupt operations, and startup recovery.

### Changed

- Require SDK v0.2.15 for the published Session accounting contracts and
  validate independently of local workspace replacements.

- Initialize only empty databases or reopen schema 4. Remove legacy column
  migrations and set `min_reader_version` to 4; older development databases
  require manual deletion before startup.
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
