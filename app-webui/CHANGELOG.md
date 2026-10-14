# Changelog

## Unreleased

- Integrated clipboard uploads, file previews and retry status with SendMessage
  scheduling and revision-ordered runtime projections.

- Moved scheduling and authoritative results to Agent SendMessage/Control; restored ordered multi-input progress across refresh and reconnect.

### Changed

- Retract failed model attempts, display retry status and retain retry state across reconnects without removing earlier output or tool activity.
- Pin published SDK v0.2.19 and the matching agent-default test dependency at
  commit f4095a4931d9. Standalone Go and browser checks use GOWORK=off without
  sibling SDK/ABI checkouts or the obsolete agent-default v0.1.0 implementation.

### Added

- Paste clipboard files and screenshots into the composer. Upload bytes through
  `POST /api/files/upload`, retain durable file-notice paths when a writer is
  available, and support image-only Assets without one. Track upload status,
  enforce size limits, cancel removed uploads and preserve per-session drafts.
- Preview local file links and file attachments in a resizable, read-only pane,
  with syntax highlighting, line references, Markdown/source modes and a mobile
  full-screen view. Resolve relative paths against the originating Session or
  previewed document. Add host text-application opening and path copying.
- Add `POST /api/files/preview` and `POST /api/files/open` independently of Asset
  Store and file-input capabilities. Preview UTF-8 and BOM-marked UTF-16 text up
  to 1 MiB and 20,000 lines; read the current file on demand.
- Configure the host text editor through `backend.text_editor_command` and
  `/app-webui config`, with editor suggestions and a `${file_path}` command
  template. Preserve quoted executable paths and file argument boundaries;
  reject malformed templates before saving. Changes apply on the next open
  without restarting the server; an empty template uses the system text app.
- Select host-local files through `POST /api/files/select` using the native
  multi-file picker on Windows, macOS and Linux. Return original absolute paths
  and server-derived metadata; keep non-image files in place without copying.
- Append a file notice before starting the Agent, so it precedes the user
  message, including an empty user message for file-only sends. The HTTP app
  calls the injected `agent.PluginInputWriter` once and starts no Turn if the
  append fails. Validation happens in the writer; stored records are formatted
  during history projection. Requires SDK v0.2.16, a supporting Agent and
  `context.input`; user/notice writes are independent
  and use no grouped history format.

### Changed

- Consume coalesced `agent.runtime.progress` increments separately from affected
  Turn/queue snapshots, sharing revision guards with bootstrap/reconciliation.
  Diagnostic tool observations no longer double-append transcript progress.
- Coalesce diagnostic model/tool progress too, preserving part/channel boundaries
  and flushing before terminal events, so bursts do not evict SSE subscribers.
- Read one Agent snapshot per `/api/state` response for consistent input/queue
  revisions and to avoid duplicate full-state cloning.
- Consume the current complete Turn snapshots and `agent.runtime.updated`
  events directly; remove old invocation/delta event handling, aggregate-text
  reconstruction and inferred SDK Turn identities from the frontend.
- Display pending attachments directly from the selected file list and return
  the complete submission snapshot when accepting an input.
- Use native file selection in the composer. Send only PNG/JPEG/GIF/WebP images
  as model media; route all selected file paths through an `app.backend` plugin
  notice. File selection remains available without an Asset Store. Drag-and-drop
  file input is not supported by this workflow.
- Recognize source-only plugin envelopes and associate uploaded files using the
  notification body rather than a plugin-name attribute.
- Require SDK v0.2.16 for the published plugin-input and Session accounting
  contracts, and validate independently of local workspace replacements.

- Show only cumulative Session tokens and current context totals in the
  right-sidebar card; omit Turn/Round details and followup header usage.
- Show cumulative Session tokens in the sidebar and child task
  details; restore totals from Session metadata and merge scoped Set snapshots.
- Resolve followup token roots on the server, initialize ordinary forks at
  zero, and clear deleted Session usage states while ignoring late updates.
- Remove Turn accounting and its unused summary component from the execution
  projection and frontend protocol; retain status, duration and failure details.

### Fixed

- Recover stuck running/queued displays by reconciling active input snapshots
  every ten seconds and on page resume, using revisions without skipping SSE
  events or resubmitting commands. Reconnect stalled streams with a heartbeat
  timeout that follows the server interval.
- Show lifecycle status, duration and running indicators once per SDK Turn,
  including when several inputs join at a Round boundary or the page reloads.
  Keep input content and rejected-input errors in their original order.

- Preserve line breaks, blank lines and indentation in plain-text tool results,
  including live output and reloaded history; wrap long lines without rendering
  output as HTML or Markdown.
- Keep image selection and sending available with an Asset Store when
  `agent.PluginInputWriter` is absent. Omit file notices for these image-only
  turns and reject unsupported local files during selection and submission.
- Keep the selected slash-command group or operation visible when navigating
  with the arrow keys, including wrapping between the first and last options.
  Scroll only the option list and preserve focus in the composer.
- Display locally selected files as attachment rows on their user message,
  including file-only sends, pending input and refreshed history. Preserve mixed
  image/file order and existing image preview; local file rows show metadata only.
- Hide plugin context envelopes in chat and followup windows, including pending
  input and refreshed history. Preserve the full context and original history
  indexes; ordinary text and empty user messages remain visible.
- Avoid provider rejection of newly selected non-image files by supplying local
  paths instead of file/audio/video media. Keep user/notice ordering after refresh,
  restart and Fork; existing persisted non-image media is not migrated.
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
