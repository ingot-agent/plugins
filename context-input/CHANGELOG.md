# Changelog

## Unreleased

- Provide SDK plugin-input writer and projector capabilities backed
  by ordinary `session.Store.Append`.
- Own input validation, the 64 KiB text limit, versioned JSON records, and escaped
  user-role XML envelopes, moved from the SDK helpers.
- Emit only `source="plugin"` in the XML envelope; keep plugin names in stored
  record metadata.
- Contribute the plugin-source explanation through `prompt.Contributor`.
- Preserve Store ordering and error semantics without automatic retries,
  scheduling, model invocation, or instruction-priority rules.

This module pins SDK v0.2.19 for plugin-input contracts and atomic batch
Store.Append, including its test stores. Validate independently with `GOWORK=off`.
