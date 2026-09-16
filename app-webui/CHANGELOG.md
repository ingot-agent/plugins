# Changelog

## Unreleased

### Changed

- Discover and invoke Operations through two-level Slash Commands with validated
  display names and unique internal routing identities.
- Edit configuration in dialogs with progressive Object/List navigation, single-line inputs, retained drafts, and fixed submit controls.
- Recover pending Operation interactions through the request drawer and move the Operation debugger into developer settings.
- Configure app.backend through Interaction requests and refresh the embedded frontend and browser coverage.
- Route duplicate and ungrouped Operations by internal identity, and render
  deeply nested fields without changing their Interaction semantics.
- Suppress compound Defaults that contain sensitive descendants.

- Moved the Vue frontend source, build tooling, and browser tests into the plugin module so the complete Web UI shares one release lifecycle.

## 0.1.0 - 2026-09-13

### Changed

- Migrated the `app-webui` official plugin into the standalone plugins repository.
