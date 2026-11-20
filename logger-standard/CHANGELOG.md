# Changelog

## Unreleased

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
