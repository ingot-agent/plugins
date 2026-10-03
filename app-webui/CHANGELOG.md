# Changelog

## Unreleased

### Changed

- Require SDK v0.2.15 for the published Session accounting contracts and
  validate independently of local workspace replacements.

- Show only cumulative Session tokens and current context totals in the
  right-sidebar card; omit Turn/Round details and followup header usage.
- Show cumulative Session tokens in the sidebar and child task
  details; restore totals from Session metadata and merge scoped Set snapshots.
- Resolve followup token roots on the server, initialize ordinary forks at
  zero, and clear deleted Session usage states while ignoring late updates.
- Remove Turn accounting and its unused summary component from the execution
  projection and frontend protocol; retain status, duration and failure details.

### Fixed

- Apply the saved tool-call visibility preference to history after refresh
  and hide tool-only messages when tool calls are hidden.
- Pass the selected or default Workspace directory to the native folder picker
  instead of the default Workspace display label.

## 0.1.1 - 2026-09-28

### Added

- Expose the optional `modelselection.Controller` capability so other plugins
  can provide live provider, model, and reasoning-effort choices to the Web UI.
- Restore command dialog state when a live event is missed, including pending
  interactions and completed operations.

### Changed

- Remove the Ingot logo and name from conversation messages while retaining
  live execution status, navigation branding, and the welcome screen.
- Apply the saved tool-call visibility preference to persisted history after
  refresh, including tool-only assistant messages, without hiding answers;
  remove the parent-message round margin for history with visible tool calls
  so card spacing stays consistent across mixed and tool-only rounds.

- Discover and invoke Operations through two-level Slash Commands with validated
  display names and unique internal routing identities.
- Edit configuration in dialogs with progressive Object/List navigation, single-line inputs, retained drafts, and fixed submit controls.
- Recover pending Operation interactions through the request drawer and move the Operation debugger into developer settings.
- Configure app.backend through Interaction requests and refresh the embedded frontend and browser coverage.
- Route duplicate and ungrouped Operations by internal identity, and render
  deeply nested fields without changing their Interaction semantics.
- Suppress compound Defaults that contain sensitive descendants.
- Open the host platform's native directory picker for Workspace selection and
  bind new or legacy unbound Sessions to a server-owned default Workspace when
  no directory is explicitly selected.

- Moved the Vue frontend source, build tooling, and browser tests into the plugin module so the complete Web UI shares one release lifecycle.

## 0.1.0 - 2026-09-13

### Changed

- Migrated the `app-webui` official plugin into the standalone plugins repository.
