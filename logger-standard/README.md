# logger-standard

`logger.standard` implements the [SDK](../../sdk/logging) `logging.Logger`
contract: leveled, structured records with operator-selected filtering,
rendering, destination and file retention. See the
[plugin documentation index](../docs/README.md) for composition and setup.

## Identity and capabilities

The directory/module suffix is `logger-standard`; the manifest name is
`logger.standard`. [The manifest](ingot.plugin.toml) declares component
`default` in package `.`, Ingot compatibility `>=0.3.0 <0.4.0`, and state
schema/reader versions `1/1`. [New](logger.go) has the signature
`New(ctx, deps)` and requires `invocation.Invocation` and `state.Scope`. It
exports `Logger logging.Logger` and `Operations []operation.Operation`.

Any component that declares a `logging.Logger` dependency receives this
implementation. The contract itself lives in the optional SDK, so a component
can be compiled against another implementation or a test double without this
module.

## Configuration

The plugin reads `config.toml` inside its assigned `state.Scope.Dir()`. A
missing file is the normal Unconfigured state and selects the defaults below;
unknown TOML fields are rejected rather than ignored.

```toml
level = "info"
format = "text"
output = "stderr"
path = "logs/ingot.log"
max_record_bytes = 65536
max_size_bytes = 33554432
max_backups = 3
```

| Field | Default | Meaning |
| --- | --- | --- |
| `level` | `info` | Minimum emitted level: `debug`, `info`, `warn` or `error` |
| `format` | `text` | `text` (one `key=value` line) or `json` (one object per line) |
| `output` | `stderr` | `stderr` (the Runtime Image's own stream) or `file` |
| `path` | `logs/ingot.log` | File destination; relative paths resolve inside the state scope |
| `max_record_bytes` | 64 KiB | Bounds one attribute value and one complete record |
| `max_size_bytes` | 32 MiB | Size at which the file destination rotates |
| `max_backups` | 3 | Rotated files kept as `<path>.1` (newest) through `<path>.N` |

Zero selects the default, not an unlimited value; negative sizes are rejected.
Rotation always applies to the file destination. To make rotation practically
unreachable, configure a `max_size_bytes` larger than any file you expect
instead of disabling it.

The operation named `config`, group `logger-standard` (shown by the bundled
host as `/logger-standard config`), accepts the JSON input `{}` with no extra
properties. It collects every setting through structured interaction, validates
and persists them, and applies them to the running Logger without a restart.
Its output repeats the effective values with `restart_required: false`.
Concurrent configuration changes fail with `ErrConfigConflict`.

## Behavior

Records below the effective level are discarded before their attributes are
materialized, so `Enabled` should guard expensive attribute construction.
`With` retains attributes on a child Logger that shares the same destination and
level policy; a later record that repeats a key wins, and the duplicate is
rendered once.

Every record is rendered with `time`, `level` and `msg`. When the calling
context carries `observation.Correlation`, `session_id`, `turn_id`, `round` and
`tool_call_id` are added automatically; a missing correlation adds nothing and
never drops the record. Text rendering quotes values that contain spaces,
quotes, `=` or control characters. JSON rendering is valid for arbitrary UTF-8
input, including control characters, and nested `logging.Group` values become
nested objects.

`max_record_bytes` bounds runaway output in two ways. A single text value is
truncated at a UTF-8 boundary with an `...(truncated N bytes)` marker, and an
attribute that does not fit the remaining record budget is skipped while later
smaller attributes still render; skipped attributes are counted in
`truncated_attrs`. Text records stay one line; JSON records stay parseable. This
is a safety bound, not a substitute for not logging payloads.

The sink is safe for concurrent use: writes, rotation and policy changes are
serialized, so a record is never interleaved with another. Writes are
synchronous; there is no background queue, sampling, rate limiting or
multi-destination fan-out. Filtering happens before formatting, so a disabled
level costs one atomic read.

With `output = "stderr"` the record stream is the process's own standard error,
which the Runtime Image already directs to its per-process log file; rotation
then belongs to the host. Rotated files are created with mode `0600` and the
directory with `0700`. A rotation or reopen failure is recorded and the sink
falls back to standard error rather than blocking the emitting component. If the
configured destination cannot be opened at construction, `New` fails.

`Close` releases the file destination and stops writing; it never closes the
process-wide standard error stream. It is safe to call more than once.

## Dependencies

This module imports `github.com/ingot-agent/sdk/logging`, introduced in the SDK
release after `v0.2.20`. Until that tag is published, the repository `go.work`
maps `github.com/ingot-agent/sdk` to the adjacent checkout for local
development. Remove that replacement once the release exists; CI validates the
module with `GOWORK=off`.

## Development checks

```sh
GOWORK=off go mod tidy -diff
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
```

The rotation, truncation and concurrency tests run entirely in temporary
directories and do not touch the process log.
