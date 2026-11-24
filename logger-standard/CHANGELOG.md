# Changelog

## 0.1.0 - 2026-11-24

### Build

- Require Go 1.26.0 or newer. Go 1.24 and Go 1.25 are both out of support, so
  the previous 1.24 floor advertised a toolchain line that no longer receives
  security fixes. This is a build requirement only; no contract, configuration
  or runtime behavior changed.

- Initial `logger.standard` module implementing `logging.Logger` from the
  optional SDK contract.
- Level filtering (`debug`, `info`, `warn`, `error`), `text` and `json`
  rendering, and `stderr` or rotating-file destinations.
- Live `config` Operation with conflict detection; effective policy applies
  without restart.
- Bounded records: per-value UTF-8-safe truncation and dropped-attribute
  accounting prevent a single record from growing without limit.
- Automatic `session_id`, `turn_id`, `round` and `tool_call_id` attributes from
  `observation.Correlation`.
