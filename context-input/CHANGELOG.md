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

This module requires the plugin-input contracts published in SDK v0.2.16 and
supports isolated validation with `GOWORK=off`.
