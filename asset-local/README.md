# asset-local

`asset.local` stores immutable binary assets in local plugin state. It supplies
the storage used for attachments and for materializing inline agent messages.
See the [plugin documentation index](../docs/README.md) for composition and setup.

## Identity and capabilities

The directory/module suffix is `asset-local`; the manifest name is `asset.local`.
[The manifest](ingot.plugin.toml) declares component `default` in package `.`,
Ingot compatibility `>=0.3.0 <0.4.0`, and state schema/reader versions `1/1`.
[New](assetlocal.go) has the signature `New(ctx, deps)` and requires `state.Scope`.
It exports `Store asset.Store` and `Operations []operation.Operation`.
`asset.Store` embeds `asset.Resolver`, so the same export supplies both capabilities.

## Configuration

The plugin reads `config.toml` inside its assigned `state.Scope.Dir()`. These
settings are plugin-owned runtime state; they are not fields of the build recipe
`plugins.toml`. A missing file uses the defaults below. Unknown TOML fields,
malformed files, negative limits, and a non-absolute or empty state directory fail
construction.

```toml
max_object_bytes = 67108864
max_total_bytes = 10737418240
io_concurrency = 8
```

| Field | Default | Meaning |
| --- | --- | --- |
| `max_object_bytes` | 64 MiB | Maximum declared size of one `Put` |
| `max_total_bytes` | 10 GiB | Maximum total bytes in committed blobs |
| `io_concurrency` | 8 | Shared concurrent slots for writes and open readers |

Zero selects the default, not an unlimited value. All effective values must be
positive, and `max_object_bytes` must not exceed `max_total_bytes`.

The operation named `config`, group `asset-local` (shown by the bundled host as
`/asset-local config`), accepts the JSON input `{}` with no extra properties. It
collects all three integer settings through structured interaction, validates and
persists them, and updates the running store. Its output contains the submitted
`max_object_bytes`, `max_total_bytes`, `io_concurrency`, and
`restart_required: false`. A zero in that output still means the default. Lowering
total capacity below committed storage fails with `ErrCapacity`; concurrent
configuration changes fail with `ErrConfigConflict`.

Direct file edits are loaded at construction; use the operation for live updates.
The persistence implementation is in [config.go](config.go) and the operation in
[setup.go](setup.go).

## Storage and behavior

The state scope contains `config.toml`, `blobs/<first-two-hex-digits>/<sha256>`, and
`staging/`. References have the exact form `sha256:` followed by 64 lowercase
hexadecimal digits. File names and MIME types belong to content metadata, not to
the blob identity.

`Put` requires a reader and an exact declared byte count. It reads at most the
declared size plus one byte, rejects both shorter and longer bodies, hashes and
syncs a staging file, and publishes a hard link into the blob directory. The
filesystem must support hard links between these directories. Repeated identical
content resolves to the same reference without consuming capacity again; an
existing blob is rehashed before it is accepted as a duplicate.

On startup the store removes incomplete staging entries and counts regular blob
files. It refuses startup if stored bytes exceed configured capacity. Startup,
`Stat`, and `Open` do not rehash every blob; the store is not a continuous integrity
scanner. `Open` holds an I/O slot until the caller closes the returned reader.
Missing references preserve both `ErrNotFound` and `fs.ErrNotExist`; malformed
references return `ErrInvalidReference`.

There is no deletion or garbage-collection API. Deleting a session does not reclaim
its assets. Capacity accounting and locking are per store instance; a shared
multi-process writer/garbage collector is not implemented. The state directory
must be managed as private application storage.

## Development checks

Run `go test ./...` from this module in the supported workspace. Existing tests in
[assetlocal_test.go](assetlocal_test.go), [live_test.go](live_test.go), and
[m1_state_test.go](m1_state_test.go) cover immutable storage, limits, persistence,
and live configuration. See [CONTRIBUTING](../CONTRIBUTING.md) for workspace setup.
