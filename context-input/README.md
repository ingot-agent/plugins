# context-input

Manifest name: **`context.input`**. A single component exports:

| Export | SDK capability | Purpose |
|---|---|---|
| `Inputs` | `agent.PluginInputWriter` | Validate and append one input to a Session |
| `Projector` | `agent.PluginInputProjector` | Recognize and project stored records |
| `Contributor` | `prompt.Contributor` | Explain the plugin marker in the system prompt |

The required dependency is `session.Store`. There are no settings, state files,
Operations, tools, background workers, or model calls. The component becomes
available when the Runtime Image includes this module and is rebuilt. A renderer
must consume the Contributor and an Agent must consume the Projector; the official
`prompt.default` and `agent.default` support this composition.

## Calling From A Plugin

Consumers import SDK contracts, without importing this implementation:

```go
type Dependencies struct {
    PluginInputs agent.PluginInputWriter
}

func appendContext(ctx context.Context, deps Dependencies, id session.ID, text string) error {
    return deps.PluginInputs.Append(ctx, id, agent.PluginInput{
        Plugin: "example.index",
        Text: text,
    })
}
```

Supply the target Session ID explicitly from the invocation. `Plugin` is the
manifest name supplied by the caller; it is not authenticated. Text must be
nonempty XML-compatible UTF-8 and no larger than 64 KiB. Plugin names must be
nonempty XML-compatible UTF-8 without control characters. Validation errors
occur before Store writes.

The plugin stores `agent.plugin_input` version 1 Entries with raw JSON fields
`plugin` and `text`. It projects each record to a text-only user message:

```text
<system source="plugin">
context text
</system>
```

The envelope contains only the `source` attribute. The plugin name remains in
the stored record metadata and is omitted from model messages.
Structured XML encoding escapes text. Other Entry kinds are
unrecognized; malformed records, unknown fields, multiple JSON values and
unsupported versions return an error. `Project` never writes and returns owned
message data. Applications can display pending attachments from their own input
data. Errors are local to this implementation:
`ErrInvalidPluginInput`, `ErrUnsupportedPluginInputVersion`, `ErrInvalidConfig`.

## Persistence And Timing

`Inputs.Append` validates, encodes, and calls Store.Append once. Success means
commit; it does not promise model receipt, a particular Turn/Round, or a response.
Errors may leave commit status unknown. There is no retry, deduplication, turn
gate, or instruction-priority policy. Two successful appends are two records.
Restart, Fork, Archive, and Delete follow the existing Store lifecycle.

The official Agent orders records during history projection, buffering plugin
messages inside unfinished tool rounds until completion or recovery. The durable
Entries reconstruct that buffer after a restart. This plugin does not own those
Agent rules. WebUI appends file notices before starting the Agent, so they precede
the corresponding user message; notice/user writes are independent.

## Validation

The writer and projector interfaces are published in SDK v0.2.16, pinned in
`go.mod`. Run isolated checks against that release with `GOWORK=off`; no local
SDK checkout or replacement is required. This plugin remains unreleased until
its own module tag is published.

```powershell
$env:GOWORK = 'off'
go mod tidy -diff
go vet ./...
go test -race ./...
```

Tests cover input/record validation, Unicode and XML boundaries, owned values,
recognition errors, source contribution, Store error propagation and no retries.
